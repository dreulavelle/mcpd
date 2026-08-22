package mcpremote

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spoked/mcpd/internal/mcpservers"
	"github.com/spoked/mcpd/internal/plugins"
)

// maxDescription bounds what a remote server can put in front of a model.
//
// The description is the server's own text and goes straight into the tool
// catalogue every call is chosen from. A server offering thirty tools with an
// essay each would crowd out everything else a model can see, which is a
// denial of service against the conversation rather than against this host.
const maxDescription = 4096

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
	deps     plugins.Deps

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
	endpoint, headers, err := opts.Document.Resolve(opts.Values)
	if err != nil {
		return nil, err
	}

	redact := mcpservers.NewRedactor(opts.Document.Secrets(opts.Values))

	p := &Plugin{
		name:  opts.Instance,
		doc:   opts.Document,
		tools: opts.Tools,
		// Held for diagnostics only, and never put in a health message: the
		// URL may carry a token that a variable substituted into it.
		endpoint: endpoint,
		conn: newConn(endpoint, headers, &mcp.Implementation{
			Name:    "mcpd",
			Title:   "mcpd",
			Version: opts.Document.Version,
		}),
		budget: newBudget(opts.RequestsPerSecond),
		redact: redact,
		deps:   opts.Deps,
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
	// map[string]any rather than json.RawMessage for the arguments: a byte
	// slice reflects to an array of integers and marshals as an object, and
	// the registry refuses that shape at registration. It costs nothing here
	// -- the published schema is used verbatim either way, and MCP tool
	// arguments are an object by definition.
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
	if len(d) > maxDescription {
		d = d[:maxDescription] + "\n\n(truncated by mcpd)"
	}
	return d
}

// call invokes one remote tool.
func (p *Plugin) call(ctx context.Context, name string, args map[string]any) (any, error) {
	if err := p.budget.wait(ctx); err != nil {
		return nil, err
	}

	session, err := p.conn.session(ctx)
	if err != nil {
		p.setHealth(plugins.Unhealthy(p.redact.Error(err)))
		return nil, fmt.Errorf("%s is not reachable: %s", p.name, p.redact.Error(err))
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		// An error out of CallTool is the conversation breaking, not the tool
		// refusing -- a tool's own failure arrives inside a result. So the
		// session is dropped and the next call dials again.
		p.conn.drop()
		p.setHealth(plugins.Unhealthy(p.redact.Error(err)))
		return nil, fmt.Errorf("calling %s on %s failed: %s", name, p.name, p.redact.Error(err))
	}

	p.setHealth(plugins.Healthy())
	if result.IsError {
		return nil, fmt.Errorf("%s reported: %s", name, p.redact.String(textOf(result)))
	}
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
		p.setHealth(plugins.Unhealthy(msg))
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
func (p *Plugin) Check(ctx context.Context) plugins.Health {
	if err := p.conn.ping(ctx); err != nil {
		h := plugins.Unhealthy(p.redact.Error(err))
		p.setHealth(h)
		return h
	}
	h := plugins.Healthy()
	p.setHealth(h)
	return h
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
		p.setHealth(plugins.Unhealthy(p.redact.Error(err)))
		return nil, fmt.Errorf("%s is not reachable: %s", p.name, p.redact.Error(err))
	}

	var out []mcpservers.Tool
	seen := map[string]bool{}
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			p.conn.drop()
			p.setHealth(plugins.Unhealthy(p.redact.Error(err)))
			return nil, fmt.Errorf("listing the tools of %s failed: %s",
				p.name, p.redact.Error(err))
		}
		if seen[tool.Name] {
			// One name, two descriptors: there is no way to tell which one a
			// call would reach, so neither is trustworthy.
			return nil, fmt.Errorf("%s offered two tools called %q", p.name, tool.Name)
		}
		seen[tool.Name] = true

		snapshot, err := snapshotOf(p.name, tool)
		if err != nil {
			return nil, err
		}
		out = append(out, snapshot)
	}

	p.setHealth(plugins.Healthy())
	return out, nil
}

// snapshotOf turns what the wire gave us into a row we can store.
func snapshotOf(prefix string, tool *mcp.Tool) (mcpservers.Tool, error) {
	descriptor := mcpservers.Descriptor{
		Name:        tool.Name,
		Title:       tool.Title,
		Description: tool.Description,
	}
	for _, part := range []struct {
		value any
		into  *json.RawMessage
	}{{tool.InputSchema, &descriptor.InputSchema}, {tool.Annotations, &descriptor.Annotations}} {
		if part.value == nil {
			continue
		}
		encoded, err := json.Marshal(part.value)
		if err != nil {
			return mcpservers.Tool{}, fmt.Errorf("tool %q: %w", tool.Name, err)
		}
		*part.into = encoded
	}

	hash, err := mcpservers.HashDescriptor(descriptor)
	if err != nil {
		return mcpservers.Tool{}, err
	}
	return mcpservers.Tool{
		Name:       tool.Name,
		Descriptor: descriptor,
		Hash:       hash,
		Problem:    mcpservers.Inspect(prefix, descriptor),
	}, nil
}

// Endpoint reports where this plugin connects, with any variable substitution
// already applied. For diagnostics; it is never put in a health message.
func (p *Plugin) Endpoint() string { return p.endpoint }

var (
	_ plugins.Plugin  = (*Plugin)(nil)
	_ plugins.Starter = (*Plugin)(nil)
	_ plugins.Stopper = (*Plugin)(nil)
	_ plugins.Checker = (*Plugin)(nil)
)
