// Package echo is a minimal reference plugin.
//
// It exists to exercise the host end to end — registration, endpoint routing,
// per-plugin authorization, tool dispatch — without depending on an external
// system. It is the template a real integration follows, and it is what the
// host's integration tests run against.
package echoplugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/spoked/mcpd/internal/operations"
	"github.com/spoked/mcpd/internal/plugins"
)

// Plugin is the echo integration.
type Plugin struct {
	deps  plugins.Deps
	cfg   Config
	start time.Time

	mu           sync.RWMutex
	currentLabel string
}

// New constructs the plugin.
func New(deps plugins.Deps, cfg Config) *Plugin {
	return &Plugin{deps: deps, cfg: cfg, start: deps.Now()}
}

// Descriptor implements plugins.Plugin.
func (p *Plugin) Descriptor() plugins.Descriptor {
	return plugins.Descriptor{
		Name:    "echo",
		Version: "1.0.0",
		Title:   "Echo",
		Description: "A test integration for checking that a connection works. " +
			"It has one read tool and one harmless change you can practise " +
			"approving. It touches nothing outside mcpd.",
	}
}

// EchoInput is the argument to the echo tool.
type EchoInput struct {
	Message string `json:"message" jsonschema:"the text to echo back"`
}

// EchoOutput is the echo tool's result.
type EchoOutput struct {
	Message string `json:"message"`
	Length  int    `json:"length"`
}

// StatusInput takes no arguments.
type StatusInput struct{}

// StatusOutput reports host-visible state.
type StatusOutput struct {
	Plugin   string `json:"plugin"`
	Version  string `json:"version"`
	Label    string `json:"label"`
	UptimeMS int64  `json:"uptime_ms"`
}

// Register implements plugins.Plugin.
func (p *Plugin) Register(_ context.Context, r *plugins.Registry) error {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_echo",
		Title: "Echo a message back",
		Description: "Returns the supplied message unchanged, with its length. " +
			"Use this to verify that the connection and credentials work.",
		Idempotent: true,
	}, func(_ context.Context, in EchoInput) (EchoOutput, error) {
		if strings.TrimSpace(in.Message) == "" {
			return EchoOutput{}, fmt.Errorf("message must not be empty")
		}
		return EchoOutput{Message: in.Message, Length: len(in.Message)}, nil
	})

	plugins.Mutation(r, plugins.MutationSpec{
		Action:      "label.set",
		Title:       "Set the echo label",
		Description: "Changes the label reported by echo_get_status.",
		Risk:        operations.RiskLow,
		Reversible:  true,
		// Observe reads the same field Apply writes, so comparing it against
		// the plan's desired state genuinely confirms the outcome.
		Verifiable: true,
	}, &labelHandler{p: p})

	plugins.Tool(r, plugins.ToolSpec{
		Name:        "get_status",
		Title:       "Get plugin status",
		Description: "Reports this plugin's version and how long it has been running.",
		Idempotent:  true,
	}, func(_ context.Context, _ StatusInput) (StatusOutput, error) {
		d := p.Descriptor()
		return StatusOutput{
			Plugin:   d.Name,
			Version:  d.Version,
			Label:    p.label(),
			UptimeMS: p.deps.Now().Sub(p.start).Milliseconds(),
		}, nil
	})

	if p.cfg.BenchmarksEnabled {
		p.registerBenchmark(r)
	}
	return nil
}

// --- measuring the host itself ----------------------------------------------

// Payload sizes, and what each one is for.
//
// Named rather than a free integer, because the argument decides how much goes
// into somebody's context and a number field invites a model to pick one. The
// values are placed against the result-size histogram's boundaries so a call
// lands in a bucket that was chosen rather than one either side of it.
var payloadSizes = map[string]int{
	// Below every interesting boundary: what a call costs when the answer is
	// not the cost.
	"tiny": 512,
	// An ordinary listing.
	"small": 8_000,
	// The share a two-collection composite answer gets.
	"medium": 20_000,
	// Exactly the ceiling a plugin builds against.
	"budget": plugins.MaxResultBytes,
	// Deliberately past it. This is the one that demonstrates the failure
	// rather than avoiding it: the client cuts the reply mid-JSON, and the
	// overflow bucket is where it shows up.
	"over": plugins.MaxResultBytes + plugins.MaxResultBytes/2,
}

// BenchmarkPayloadInput chooses how large an answer to build.
type BenchmarkPayloadInput struct {
	Size string `json:"size" jsonschema:"how large the answer should be: tiny, small, medium, budget, or over. over deliberately exceeds what may be sent and will be cut by the client"`
}

// BenchmarkPayloadOutput is the answer, and what it actually measured.
type BenchmarkPayloadOutput struct {
	Size string `json:"size"`
	// Bytes is the exact encoded length of this result, envelope included.
	Bytes int    `json:"bytes"`
	Note  string `json:"note,omitempty"`
	// Filler carries the weight and means nothing. Last, so a reader of the
	// raw JSON meets the numbers first.
	Filler string `json:"filler"`
}

func (p *Plugin) registerBenchmark(r *plugins.Registry) {
	// get_, because it returns one thing. The verb vocabulary is closed and a
	// diagnostic is not a reason to widen it -- "benchmark" lives in the noun,
	// where it warns without inventing a fifth verb every plugin then sees.
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_benchmark_payload",
		Title: "Get a benchmark payload",
		Description: "Returns a result of the size you ask for, to measure what " +
			"this host costs. Echo has no upstream, so the time a call takes is " +
			"mcpd's own overhead rather than any far end's. For measuring, not " +
			"for checking a connection — use echo_get_echo for that.",
		Idempotent: true,
	}, func(_ context.Context, in BenchmarkPayloadInput) (BenchmarkPayloadOutput, error) {
		target, err := payloadSize(in.Size)
		if err != nil {
			return BenchmarkPayloadOutput{}, err
		}
		return buildPayload(in.Size, target)
	})
}

// payloadSize resolves the named size, refusing anything else.
//
// Refused rather than defaulted: a caller who asked for a size this does not
// know did not mean "whatever you like", and silently answering with the
// smallest would make a benchmark report a number nobody asked for.
func payloadSize(name string) (int, error) {
	n, ok := payloadSizes[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return 0, fmt.Errorf(
			"size must be one of tiny, small, medium, budget or over; got %q", name)
	}
	return n, nil
}

// buildPayload returns a result whose encoded length is exactly target.
//
// The envelope is measured rather than estimated: the field names, the size
// word and the digits of Bytes all take room, and a filler sized by subtracting
// a guess would land a call in the bucket next to the one it asked for --
// which is the one thing a tool built to exercise buckets must not do.
func buildPayload(size string, target int) (BenchmarkPayloadOutput, error) {
	out := BenchmarkPayloadOutput{Size: size, Bytes: target}
	if target > plugins.MaxResultBytes {
		out.Note = "deliberately past the ceiling; the client will cut this reply"
	}
	empty, err := json.Marshal(out)
	if err != nil {
		return BenchmarkPayloadOutput{}, err
	}
	// Every filler byte is one JSON byte: plain ASCII needs no escaping.
	if n := target - len(empty); n > 0 {
		out.Filler = strings.Repeat("x", n)
	}
	return out, nil
}

// Check implements plugins.Checker. The echo plugin has no upstream
// dependency, so it is healthy whenever the process is.
func (p *Plugin) Check(context.Context) plugins.Health { return plugins.Healthy() }

// --- a mutation, so the approval path has something to exercise -------------

// SetLabelParams changes the plugin's stored label.
type SetLabelParams struct {
	Label string `json:"label" jsonschema:"the new label"`
}

// LabelState is the observable state the mutation acts on.
type LabelState struct {
	Label string `json:"label"`
}

// labelHandler implements the three-phase mutation contract against the
// plugin's own in-memory state. It is a stand-in for a real upstream API, and
// exists so the approval, execution and verification path can be exercised
// without one.
type labelHandler struct {
	p *Plugin
}

// Plan reads current state and describes the change. It mutates nothing, and
// runs both at proposal and again immediately before Apply.
func (h *labelHandler) Plan(_ context.Context, params SetLabelParams) (plugins.Plan[LabelState], error) {
	if strings.TrimSpace(params.Label) == "" {
		return plugins.Plan[LabelState]{}, fmt.Errorf("label must not be empty")
	}
	current := h.p.label()
	return plugins.Plan[LabelState]{
		Before:  LabelState{Label: current},
		Desired: LabelState{Label: params.Label},
		Preconditions: map[string]any{
			"label": current,
		},
		Changes: []operations.Change{
			{Field: "label", From: current, To: params.Label},
		},
		Impact:   "Changes the label reported by echo_get_status. Harmless; it exists to exercise the approval path.",
		Rollback: SetLabelParams{Label: current},
	}, nil
}

// Apply performs the change. It is called at most once per attempt, only by
// the executor, only after the state machine granted a claim.
func (h *labelHandler) Apply(_ context.Context, params SetLabelParams, _ plugins.Plan[LabelState]) (plugins.ApplyResult, error) {
	h.p.setLabel(params.Label)
	return plugins.ApplyResult{UpstreamRef: "echo-local"}, nil
}

// Observe re-reads state for verification.
func (h *labelHandler) Observe(_ context.Context, _ SetLabelParams) (LabelState, error) {
	return LabelState{Label: h.p.label()}, nil
}

func (p *Plugin) label() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.currentLabel == "" {
		return "default"
	}
	return p.currentLabel
}

func (p *Plugin) setLabel(v string) {
	p.mu.Lock()
	p.currentLabel = v
	p.mu.Unlock()
}
