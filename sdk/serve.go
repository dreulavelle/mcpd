package sdk

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// Indeterminate marks an outcome a mutation could not establish.
//
// Wrap it from Apply whenever a write may or may not have landed: a timeout, a
// dropped connection, a 5xx. It is the single most important error in this
// package. Returning an ordinary error tells the host the write did not
// happen, and the host may then allow a retry that applies the change twice.
//
//	if err := post(ctx, req); err != nil {
//		if isAmbiguous(err) {
//			return sdk.ApplyResult{}, fmt.Errorf("write may have landed: %w", sdk.Indeterminate)
//		}
//		return sdk.ApplyResult{}, err
//	}
var Indeterminate = errors.New("upstream outcome indeterminate")

// NotFound reports a missing upstream resource.
var NotFound = errors.New("not found")

// invalidParams builds a parameter error.
func invalidParams(format string, args ...any) error {
	return &codedError{code: "INVALID_PARAMS", msg: fmt.Sprintf(format, args...)}
}

type codedError struct {
	code string
	msg  string
}

func (e *codedError) Error() string { return e.msg }

// codeFor maps an error to its wire code.
func codeFor(err error) string {
	var coded *codedError
	switch {
	case errors.As(err, &coded):
		return coded.code
	case errors.Is(err, Indeterminate):
		return "INDETERMINATE"
	case errors.Is(err, NotFound):
		return "NOT_FOUND"
	default:
		return "UPSTREAM_FAILED"
	}
}

// wire types, mirroring the host's protocol package.

type request struct {
	ID     uint64          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type response struct {
	ID     uint64          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *wireError      `json:"error,omitempty"`
}

type wireError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type describeResult struct {
	Protocol    string               `json:"protocol"`
	Name        string               `json:"name"`
	Version     string               `json:"version"`
	Title       string               `json:"title"`
	Description string               `json:"description"`
	Tools       []toolDescriptor     `json:"tools"`
	Mutations   []mutationDescriptor `json:"mutations"`
}

type toolDescriptor struct {
	Name        string          `json:"name"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	Idempotent  bool            `json:"idempotent"`
}

type mutationDescriptor struct {
	Action      string          `json:"action"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	Risk        string          `json:"risk"`
	Reversible  bool            `json:"reversible"`
}

type callToolParams struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type mutationParams struct {
	Action string          `json:"action"`
	Params json.RawMessage `json:"params"`
	Plan   json.RawMessage `json:"plan,omitempty"`
}

// Serve runs the plugin until the host closes its input.
//
// It never returns normally: the process exits when stdin closes, which is how
// the host stops a plugin. Registration errors are reported on stderr and exit
// non-zero, because a plugin that half-registered is worse than one that
// refuses to start.
func Serve(p *Plugin) {
	if err := p.validate(); err != nil {
		fmt.Fprintln(os.Stderr, "plugin registration failed:", err)
		os.Exit(1)
	}
	if err := p.serve(os.Stdin, os.Stdout); err != nil && !errors.Is(err, io.EOF) {
		fmt.Fprintln(os.Stderr, "plugin stopped:", err)
		os.Exit(1)
	}
}

func (p *Plugin) validate() error {
	p.mu.RLock()
	defer p.mu.RUnlock()

	errs := append([]error(nil), p.errs...)
	if len(p.tools) == 0 && len(p.mutations) == 0 {
		errs = append(errs, errors.New("plugin registers no tools or mutations"))
	}
	if len(errs) == 0 {
		return nil
	}

	msgs := make([]string, len(errs))
	for i, e := range errs {
		msgs[i] = e.Error()
	}
	sort.Strings(msgs)
	return errors.New(strings.Join(msgs, "\n  - "))
}

// serve is the request loop, separated from Serve so tests can drive it.
func (p *Plugin) serve(in io.Reader, out io.Writer) error {
	reader := bufio.NewReaderSize(in, 64<<10)
	writer := bufio.NewWriter(out)
	ctx := context.Background()

	for {
		line, err := readLine(reader)
		if err != nil {
			return err
		}
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}

		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			// A frame that cannot be parsed has no id to answer, so there is
			// nothing to reply to. Reporting it on stderr is the only signal
			// available.
			fmt.Fprintln(os.Stderr, "plugin: unreadable request frame:", err)
			continue
		}

		resp := p.dispatch(ctx, req)

		if req.Method == "shutdown" {
			_ = writeFrame(writer, resp)
			return nil
		}
		if err := writeFrame(writer, resp); err != nil {
			return err
		}
	}
}

func writeFrame(w *bufio.Writer, resp response) error {
	encoded, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	if _, err := w.Write(append(encoded, '\n')); err != nil {
		return err
	}
	// Flushed per frame: the host reads synchronously and would otherwise wait
	// on a buffer that never fills.
	return w.Flush()
}

func readLine(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err != nil {
			return nil, err
		}
		buf = append(buf, chunk...)
		if !isPrefix {
			return buf, nil
		}
	}
}

// dispatch routes one request. A panic in plugin code becomes an error
// response rather than taking the process down mid-conversation.
func (p *Plugin) dispatch(ctx context.Context, req request) (resp response) {
	resp.ID = req.ID
	defer func() {
		if v := recover(); v != nil {
			resp.Result = nil
			resp.Error = &wireError{
				Code:    "INTERNAL",
				Message: fmt.Sprintf("plugin panicked handling %s: %v", req.Method, v),
			}
		}
	}()

	result, err := p.handle(ctx, req)
	if err != nil {
		resp.Error = &wireError{Code: codeFor(err), Message: err.Error()}
		return resp
	}
	resp.Result = result
	return resp
}

func (p *Plugin) handle(ctx context.Context, req request) (json.RawMessage, error) {
	switch req.Method {
	case "describe":
		return json.Marshal(p.describe())

	case "call_tool":
		var params callToolParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, invalidParams("could not decode call_tool params: %v", err)
		}
		p.mu.RLock()
		tool := p.tools[params.Name]
		p.mu.RUnlock()
		if tool == nil {
			return nil, invalidParams("no such tool %q", params.Name)
		}
		return tool.invoke(ctx, params.Args)

	case "plan", "apply", "observe":
		var params mutationParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, invalidParams("could not decode %s params: %v", req.Method, err)
		}
		p.mu.RLock()
		mutation := p.mutations[params.Action]
		p.mu.RUnlock()
		if mutation == nil {
			return nil, invalidParams("no such mutation %q", params.Action)
		}

		switch req.Method {
		case "plan":
			plan, err := mutation.plan(ctx, params.Params)
			if err != nil {
				return nil, err
			}
			return json.Marshal(plan)
		case "apply":
			result, err := mutation.apply(ctx, params.Params, params.Plan)
			if err != nil {
				return nil, err
			}
			return json.Marshal(map[string]any{
				"upstream_ref": result.UpstreamRef,
				"async":        result.Async,
			})
		default:
			return mutation.observe(ctx, params.Params)
		}

	case "health":
		p.mu.RLock()
		fn := p.healthFn
		p.mu.RUnlock()
		if fn == nil {
			return json.Marshal(Healthy())
		}
		return json.Marshal(fn(ctx))

	case "shutdown":
		p.mu.RLock()
		fn := p.shutdownFn
		p.mu.RUnlock()
		if fn != nil {
			if err := fn(ctx); err != nil {
				return nil, err
			}
		}
		return json.Marshal(map[string]bool{"ok": true})

	default:
		return nil, invalidParams("unknown method %q", req.Method)
	}
}

func (p *Plugin) describe() describeResult {
	p.mu.RLock()
	defer p.mu.RUnlock()

	out := describeResult{
		Protocol: protocolVersion, Name: p.name, Version: p.version,
		Title: p.title, Description: p.description,
	}
	for _, name := range p.order {
		t := p.tools[name]
		out.Tools = append(out.Tools, toolDescriptor{
			Name: t.spec.Name, Title: t.spec.Title,
			Description: t.spec.Description, InputSchema: t.inputSchema,
			Idempotent: t.spec.Idempotent,
		})
	}
	for _, action := range p.mutOrder {
		m := p.mutations[action]
		out.Mutations = append(out.Mutations, mutationDescriptor{
			Action: m.spec.Action, Title: m.spec.Title,
			Description: m.spec.Description, InputSchema: m.inputSchema,
			Risk: string(m.spec.Risk), Reversible: m.spec.Reversible,
		})
	}
	return out
}
