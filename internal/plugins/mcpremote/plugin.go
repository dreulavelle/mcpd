package mcpremote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spoked/mcpd/internal/mcpservers"
	"github.com/spoked/mcpd/internal/plugins"
)

// Bounds on what a remote server can make this host store and show.
//
// None of these is a guess at what a legitimate server does; they are the
// point past which one has stopped being legitimate. A discovery is written in
// a single transaction against the one SQLite writer, and a description goes
// straight into the tool catalogue a model chooses from -- so an unbounded
// catalogue is a denial of service against the conversation, and an unbounded
// transaction is one against every other writer in the process.
const (
	// maxDescription bounds the text a model reads when choosing.
	maxDescription = 4096
	// maxTitle bounds the label a person reads.
	maxTitle = 256
	// maxInputSchema bounds one tool's published schema. Anything larger is
	// not something a model was going to fill in correctly anyway.
	maxInputSchema = 64 << 10
	// maxTools bounds one server's catalogue. tools/list is paginated, so
	// without this a server can page forever.
	maxTools = 1000
	// maxSnapshotBytes bounds the whole discovery, since the per-tool caps
	// multiplied by maxTools is still more than belongs in one transaction.
	maxSnapshotBytes = 8 << 20
)

// minCheckBudget is the least remaining time worth starting a dial for.
//
// Below it, a health check reports what it last observed rather than beginning
// a connection it cannot wait for. The readiness probe's budget is shared
// across every plugin, so spending it on a handshake nobody will read the
// result of takes it from the checks that could have answered.
const minCheckBudget = 250 * time.Millisecond

// Options is what the host supplies to build one remote server.
type Options struct {
	// Instance is the plugin name, which is also the endpoint path segment
	// and the prefix on every tool.
	Instance string
	// Document is the parsed server.json.
	Document *mcpservers.Document
	// Tools is the enabled part of the stored snapshot. It is the whole of
	// what Register mounts: nothing here reaches the network to find out what
	// exists, because boot must not depend on a third party being up.
	Tools []mcpservers.Tool
	// Values are the resolved settings, keyed by bare field key.
	Values map[string]string
	// RequestsPerSecond is the per-server budget. Zero is unbounded.
	RequestsPerSecond int
	// Deps is the standard plugin dependency set.
	Deps plugins.Deps
}

// Plugin is a remote MCP server mounted as one of this host's plugins.
type Plugin struct {
	name     string
	doc      *mcpservers.Document
	tools    []mcpservers.Tool
	endpoint string
	conn     *conn
	budget   budget
	redact   *mcpservers.Redactor
	// credentials is the narrow set, used only to decide whether the server
	// echoed something we sent it back into its catalogue.
	credentials *mcpservers.Redactor
	deps        plugins.Deps

	mu     sync.RWMutex
	health plugins.Health
}

// New builds a remote server plugin.
//
// Everything structural is settled here: the address is resolved from the
// document and the settings, the headers are assembled, the snapshot is taken
// as given. Nothing is dialled -- that is Start's job, and a plugin whose
// upstream is unreachable still has to mount so that its tools exist and its
// form can be corrected.
func New(opts Options) (*Plugin, error) {
	if opts.Document == nil {
		return nil, fmt.Errorf("mcpremote: %s has no server.json", opts.Instance)
	}
	// The redactor is built before the thing it has to redact. Resolve
	// substitutes variables into the URL and then re-checks the result, and
	// those refusals quote the whole address with %q -- so a secret variable
	// holding a scheme-less host produces an error carrying the credential.
	// That error is the one the host records as the reason a plugin will not
	// mount, which reaches the Plugins page and the /discover response.
	redact := mcpservers.NewRedactor(opts.Document.SensitiveValues(opts.Values))
	// A second, narrower set, for a different question. See CredentialValues:
	// this one decides whether the server echoed a credential back, which must
	// not fire on an operator's region slug turning up in a description.
	credentials := mcpservers.NewRedactor(opts.Document.CredentialValues(opts.Values))

	endpoint, headers, err := opts.Document.Resolve(opts.Values)
	if err != nil {
		return nil, errors.New(redact.Error(err))
	}

	// The connection pins the configured headers to the configured address, so
	// it has to be built from both together.
	conn, err := newConn(endpoint, headers, &mcp.Implementation{
		Name:    "mcpd",
		Title:   "mcpd",
		Version: opts.Document.Version,
	})
	if err != nil {
		return nil, errors.New(redact.Error(err))
	}

	p := &Plugin{
		name:  opts.Instance,
		doc:   opts.Document,
		tools: opts.Tools,
		// Held for diagnostics only, and never put in a health message: the
		// URL may carry a token that a variable substituted into it.
		endpoint:    endpoint,
		conn:        conn,
		budget:      newBudget(opts.RequestsPerSecond),
		redact:      redact,
		credentials: credentials,
		deps:        opts.Deps,
		health: plugins.Health{State: plugins.DegradedState,
			Message: "not connected yet", CheckedAt: opts.Deps.Now()},
	}
	return p, nil
}

// Descriptor implements plugins.Plugin.
//
// The runtime is what marks this as untrusted everywhere downstream: the
// registry refuses a mutation from it, the tool-name rule becomes the MCP
// specification's rather than this host's convention, and attachment is per
// tool so one malformed descriptor costs one tool.
func (p *Plugin) Descriptor() plugins.Descriptor {
	return plugins.Descriptor{
		Name:        p.name,
		Version:     p.doc.Version,
		Title:       p.doc.DisplayTitle(),
		Description: p.instructions(),
		Runtime:     plugins.RuntimeMCP,
	}
}

// instructions tell a model what it is looking at, and what it will not find.
func (p *Plugin) instructions() string {
	var b strings.Builder
	b.WriteString(p.doc.Description)
	b.WriteString("\n\nThese tools are served by a remote MCP server that mcpd " +
		"connects to on your behalf. Every one of them is read-only: this server " +
		"cannot change anything through mcpd, so a request to modify something " +
		"here has to be taken somewhere else.")
	return b.String()
}

// Register implements plugins.Plugin.
//
// It reads the snapshot it was constructed with and nothing else. This is the
// load-bearing part of the design: the alternative -- calling tools/list here
// -- would mean a host that comes up with no tools whenever the far end is
// having a bad morning, and a model that reasonably concludes the integration
// was removed.
func (p *Plugin) Register(_ context.Context, r *plugins.Registry) error {
	if len(p.tools) == 0 {
		// Said plainly rather than mounted empty. A server whose tools are all
		// still pending is the ordinary state right after an import, and the
		// host's own "registered nothing" error is the honest report.
		return fmt.Errorf("no tools of %s are enabled yet; discover them and "+
			"enable the ones you want", p.name)
	}

	for _, tool := range p.tools {
		if tool.Problem != "" {
			// Belt and braces: the store refuses to enable a tool with a
			// recorded problem. If one is here anyway, the reason it cannot be
			// mounted has not changed.
			p.deps.Log.Warn("skipping a remote tool that cannot be mounted",
				"plugin", p.name, "tool", tool.Name, "reason", tool.Problem)
			continue
		}
		p.registerTool(r, tool)
	}
	return nil
}

func (p *Plugin) registerTool(r *plugins.Registry, tool mcpservers.Tool) {
	name := tool.Name
	spec := plugins.ToolSpec{
		Name:        name,
		Title:       tool.Descriptor.Title,
		Description: description(tool),
		// The name is passed through exactly as upstream published it. The
		// registry relaxes its own pattern for this runtime rather than this
		// runtime rewriting the name, because a rewritten name is one the far
		// end does not answer to.
		InputSchema: tool.Descriptor.InputSchema,
		// Read-only by construction: this runtime cannot register a mutation,
		// and the registry enforces that rather than trusting the claim.
		Idempotent: true,
		// Rate limiting for a remote server is per server, not per tool --
		// they are all one process at the far end. See KeyRequestsPerSecond.
		RateLimit: 0,
	}

	// The output schema is deliberately not forwarded. Publishing one tells a
	// client the structured result conforms to it, and nothing here validates
	// that it does; a promise this host cannot keep is worse than no promise.
	//
	// map[string]any rather than json.RawMessage for the arguments. Either
	// would register now that the registry skips the derived-schema check when
	// a schema is supplied, but this end wants a decoded object anyway: the
	// call is re-encoded onto the wire by the MCP client, so there is no byte
	// fidelity to preserve, and MCP tool arguments are an object by
	// definition.
	plugins.Tool(r, spec, func(ctx context.Context, args map[string]any) (any, error) {
		return p.call(ctx, name, args)
	})
}

// description is what the model reads when choosing.
func description(tool mcpservers.Tool) string {
	d := strings.TrimSpace(tool.Descriptor.Description)
	if d == "" {
		// The registry requires one, and it is right to: a tool with no
		// description is a tool a model picks by guessing at its name.
		d = "The server published no description for " + tool.Name + "."
	}
	// A safety net rather than the bound: Discover already truncates before
	// anything is stored. It stays because a snapshot written by an older
	// build has not been through that.
	return truncate(d, maxDescription)
}

// truncate cuts text to at most n bytes without splitting a rune.
//
// Byte-slicing third-party text produces invalid UTF-8, which then travels
// into JSON, into the tool catalogue, and into whatever the model does with a
// replacement character it did not expect.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\u2026 (truncated by mcpd)"
}

// call invokes one remote tool.
func (p *Plugin) call(ctx context.Context, name string, args map[string]any) (any, error) {
	if err := p.budget.wait(ctx); err != nil {
		return nil, err
	}

	session, err := p.conn.session(ctx)
	if err != nil {
		p.setHealth(plugins.Unhealthy(unreachable(p.name, p.redact.Error(err))))
		return nil, fmt.Errorf("%s is not reachable: %s", p.name, p.redact.Error(err))
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		// An error out of CallTool is the conversation breaking, not the tool
		// refusing -- a tool's own failure arrives inside a result. So the
		// session is dropped and the next call dials again.
		//
		// The handle this call used, not whatever is current: by the time a
		// slow failure lands, another caller may already have replaced it.
		p.conn.drop(session)
		p.setHealth(plugins.Unhealthy(unreachable(p.name, p.redact.Error(err))))
		return nil, fmt.Errorf("calling %s on %s failed: %s", name, p.name, p.redact.Error(err))
	}

	p.setHealth(plugins.Healthy())
	if result.IsError {
		return nil, fmt.Errorf("%s reported: %s", name, p.redact.String(textOf(result)))
	}

	// A successful result is passed through as it came. Not an oversight: it
	// is the hot path, it can be large, and scanning every byte of every
	// response for a credential the far end already holds buys little. What it
	// would prevent is a server reflecting our own token back to a caller that
	// is already authorized for this plugin -- a smaller step than the one the
	// server took by having the token at all. Error text and stored
	// descriptors are different, because those persist and are read by people
	// and models that never called anything.
	if result.StructuredContent != nil {
		return result.StructuredContent, nil
	}
	return map[string]any{"text": textOf(result)}, nil
}

// textOf flattens a result's content into something a model can read.
func textOf(result *mcp.CallToolResult) string {
	var parts []string
	for _, c := range result.Content {
		if t, ok := c.(*mcp.TextContent); ok && t.Text != "" {
			parts = append(parts, t.Text)
		}
	}
	if len(parts) == 0 {
		return "(the server returned no readable content)"
	}
	return strings.Join(parts, "\n")
}

// Start implements plugins.Starter.
//
// It attempts a connection and reports the result as health, and it does not
// return an error when that connection fails.
//
// That is a deliberate departure from how a compiled-in plugin behaves. For
// one of those, a Start that fails usually means a credential is wrong, and
// refusing to take up new settings while leaving the working ones in place is
// the kind thing to do. A remote MCP server being down is not a configuration
// error -- it is Tuesday -- and treating it as one would mean an operator
// correcting a header while the far end is unreachable is told their change
// "did not start", with the old value silently still in force.
func (p *Plugin) Start(ctx context.Context) error {
	if err := p.conn.ping(ctx); err != nil {
		msg := p.redact.Error(err)
		p.setHealth(plugins.Unhealthy(unreachable(p.name, msg)))
		p.deps.Log.Warn("remote MCP server is not reachable; its tools are mounted "+
			"from the last snapshot and will fail until it returns",
			"plugin", p.name, "error", msg)
		return nil
	}
	p.setHealth(plugins.Healthy())
	return nil
}

// Shutdown implements plugins.Stopper.
func (p *Plugin) Shutdown(context.Context) error { return p.conn.close() }

// Check implements plugins.Checker.
//
// It reports what it last observed when there is not enough of the caller's
// budget left to establish anything new. The readiness probe is served
// unauthenticated and runs every plugin's check in turn on one shared
// deadline, so a remote server that is black-holing packets must cost that
// probe its share and no more.
func (p *Plugin) Check(ctx context.Context) plugins.Health {
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < minCheckBudget {
		return p.Health()
	}
	if err := p.conn.ping(ctx); err != nil {
		h := plugins.Unhealthy(unreachable(p.name, p.redact.Error(err)))
		p.setHealth(h)
		return h
	}
	h := plugins.Healthy()
	p.setHealth(h)
	return h
}

// Health implements plugins.HealthReporter: the state last observed, with no
// round trip. Start records one, so the host has an answer at boot without
// dialling an unreachable address a second time.
func (p *Plugin) Health() plugins.Health {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.health
}

// unreachable is what a person reads when this server cannot be dialled.
//
// The transport error is the far end's own words, or the network's, so it is
// quoted rather than run into the sentence -- run in, it reads as something
// mcpd is claiming rather than something it was told.
func unreachable(name, detail string) string {
	if detail == "" {
		return name + " could not be reached."
	}
	return fmt.Sprintf("%s could not be reached. It said: \u201c%s\u201d", name, detail)
}

func (p *Plugin) setHealth(h plugins.Health) {
	p.mu.Lock()
	p.health = h
	p.mu.Unlock()
}

// Discover asks the server what it offers, and returns descriptors ready to be
// snapshotted.
//
// This is the only place that calls tools/list, and it is driven by an
// administrator rather than by startup. Each descriptor is inspected here
// rather than at mount time, so the reason a tool cannot be used is recorded
// beside it and an operator looking for a tool that never appeared finds it
// with the reason attached.
func (p *Plugin) Discover(ctx context.Context) ([]mcpservers.Tool, error) {
	session, err := p.conn.session(ctx)
	if err != nil {
		p.setHealth(plugins.Unhealthy(unreachable(p.name, p.redact.Error(err))))
		return nil, fmt.Errorf("%s is not reachable: %s", p.name, p.redact.Error(err))
	}

	var out []mcpservers.Tool
	var total int
	seen := map[string]bool{}
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			p.conn.drop(session)
			p.setHealth(plugins.Unhealthy(unreachable(p.name, p.redact.Error(err))))
			return nil, fmt.Errorf("listing the tools of %s failed: %s",
				p.name, p.redact.Error(err))
		}
		if seen[tool.Name] {
			// One name, two descriptors: there is no way to tell which one a
			// call would reach, so neither is trustworthy. The name is the
			// server's own text, so it goes out redacted like the rest of it.
			return nil, fmt.Errorf("%s offered two tools called %q",
				p.name, p.redact.String(tool.Name))
		}
		seen[tool.Name] = true

		// Refused whole rather than truncated. Dropping the tail would make
		// the next discovery report the dropped tools as withdrawn, and the
		// one after that as new, forever -- a diff that lies is worse than a
		// discovery that fails and says why.
		if len(out) == maxTools {
			return nil, fmt.Errorf("%s offers more than %d tools, which is more "+
				"than this host will take from one server", p.name, maxTools)
		}

		snapshot, size, err := snapshotOf(p.name, tool, p.credentials)
		if err != nil {
			return nil, err
		}
		total += size
		if total > maxSnapshotBytes {
			return nil, fmt.Errorf("the tool descriptions of %s come to more than "+
				"%d bytes, which is more than this host will store for one server",
				p.name, maxSnapshotBytes)
		}
		out = append(out, snapshot)
	}

	p.setHealth(plugins.Healthy())
	return out, nil
}

// snapshotOf turns what the wire gave us into a row we can store, and reports
// how many bytes of it we agreed to keep.
//
// The descriptor is stored exactly as the server published it, and that is
// load-bearing rather than lazy. descriptor_hash is computed from what is
// stored, and descriptor_hash is the guard on every classification -- so if
// this rewrote the text using anything resolved from settings, the hash would
// become a function of the operator's configuration as well as the server's
// output. Editing an unrelated non-secret field would then change the hashes
// on the next discovery, trip the demotion in Snapshot, and silently
// un-approve every affected tool. The hash has to be a pure function of what
// the far end said.
//
// What the credential set is used for instead is a judgement. If the server
// echoed something credential-shaped back into its own catalogue, the tool is
// recorded with the reason and cannot be enabled -- the same rule as an
// oversized schema, and for the same reason: this is not a formatting problem
// to paper over, it is something an operator has to see.
//
// Text is truncated, because a description that is too long is still a usable
// tool with a shorter description. A schema that is too long is not: it cannot
// be shortened without changing what it validates, so the tool is kept with
// the reason recorded and can never be enabled. Either way the row is stored
// rather than dropped, so an operator looking for a tool that never appeared
// finds it with the reason beside it.
func snapshotOf(prefix string, tool *mcp.Tool, credentials *mcpservers.Redactor) (mcpservers.Tool, int, error) {
	descriptor := mcpservers.Descriptor{
		Name:  tool.Name,
		Title: truncate(tool.Title, maxTitle),
		// Detection runs on the server's text before it is shortened. A
		// credential straddling the truncation boundary would otherwise leave
		// an unmatched prefix behind and go unnoticed.
		Description: truncate(tool.Description, maxDescription),
	}
	echoed := credentials.Found(tool.Name) ||
		credentials.Found(tool.Title) ||
		credentials.Found(tool.Description)

	var problem string
	for _, part := range []struct {
		value any
		into  *json.RawMessage
		limit int
		label string
	}{
		{tool.InputSchema, &descriptor.InputSchema, maxInputSchema, "input schema"},
		{tool.Annotations, &descriptor.Annotations, maxInputSchema, "annotations"},
	} {
		if part.value == nil {
			continue
		}
		encoded, err := json.Marshal(part.value)
		if err != nil {
			return mcpservers.Tool{}, 0, fmt.Errorf("tool %q: %w", tool.Name, err)
		}
		if credentials.Found(string(encoded)) {
			echoed = true
		}
		if len(encoded) > part.limit {
			// Left unset rather than stored truncated: half a schema is not a
			// schema, and storing one would let Inspect judge the tool
			// mountable on something the server never published.
			problem = fmt.Sprintf("the tool's %s is %d bytes, past the %d this "+
				"host will store", part.label, len(encoded), part.limit)
			continue
		}
		*part.into = encoded
	}

	switch {
	case echoed:
		// Said first, because it means something is wrong at the other end
		// rather than merely unusable here.
		problem = "this server echoed back a value configured for it -- a " +
			"credential it was sent -- inside its own tool catalogue. Nothing " +
			"legitimate needs to do that, and a tool that does is not one to " +
			"put in front of a model"

		// And the text is scrubbed before it is stored, because storing it
		// verbatim would put the credential in a plaintext column.
		//
		// Scrubbing only here is what keeps the hash honest. A descriptor that
		// did not echo anything is stored and hashed exactly as published, so
		// descriptor_hash stays a pure function of the far end's output and an
		// operator editing an unrelated setting cannot move it. This branch is
		// exempt because a tool with a problem can never be enabled: the guard
		// in Snapshot demotes only what was enabled, so a hash that shifts
		// here has no approval to take with it.
		descriptor.Name = credentials.String(descriptor.Name)
		descriptor.Title = credentials.String(descriptor.Title)
		descriptor.Description = credentials.String(descriptor.Description)
		// Dropped rather than scrubbed, for the same reason an oversized one
		// is: a schema with holes in it is not a schema, and this tool is not
		// going to be called anyway.
		descriptor.InputSchema, descriptor.Annotations = nil, nil

	case problem == "":
		problem = mcpservers.Inspect(prefix, descriptor)
	}

	hash, err := mcpservers.HashDescriptor(descriptor)
	if err != nil {
		return mcpservers.Tool{}, 0, err
	}
	size := len(descriptor.Name) + len(descriptor.Title) + len(descriptor.Description) +
		len(descriptor.InputSchema) + len(descriptor.Annotations)
	return mcpservers.Tool{
		Name:       descriptor.Name,
		Descriptor: descriptor,
		Hash:       hash,
		Problem:    problem,
	}, size, nil
}

var (
	_ plugins.Plugin  = (*Plugin)(nil)
	_ plugins.Starter = (*Plugin)(nil)
	_ plugins.Stopper = (*Plugin)(nil)
	_ plugins.Checker = (*Plugin)(nil)
	// Asserted, because losing it is silent and expensive: Manager.Start would
	// fall back to recording Healthy, and a server whose upstream is down
	// would show a green dot for as long as it took the first health check to
	// come round. That is the thing Start returning its finding exists to
	// prevent, and it would fail as a wrong value rather than a build error.
	_ plugins.HealthReporter = (*Plugin)(nil)
)
