package plugins

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spoked/mcpd/internal/auth"
)

// Mounted is a registered, started plugin together with the MCP server that
// serves it.
type Mounted struct {
	Descriptor Descriptor
	Server     *mcp.Server
	Registry   *Registry
	// Required reports whether a failure in this plugin should fail host
	// startup, as opposed to marking only this plugin unhealthy.
	Required bool

	plugin Plugin
	mu     sync.RWMutex
	health Health
}

// Health returns the plugin's most recent health report.
func (m *Mounted) Health() Health {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.health
}

func (m *Mounted) setHealth(h Health) {
	m.mu.Lock()
	m.health = h
	m.mu.Unlock()
}

// Manager owns the lifecycle of every registered plugin.
type Manager struct {
	log        *slog.Logger
	middleware ToolMiddleware
	version    string

	approvals ApprovalService
	inline    InlinePolicy

	mu      sync.RWMutex
	mounted map[string]*Mounted

	aggMu      sync.RWMutex
	aggregates map[string]*mcp.Server
	order      []string // registration order, for reverse-order shutdown
}

// NewManager returns an empty manager. middleware is the host gate applied to
// every tool call; version identifies the host in MCP handshakes.
func NewManager(log *slog.Logger, version string, middleware ToolMiddleware, approvals ApprovalService, inline InlinePolicy) *Manager {
	if middleware == nil {
		// A nil gate would mean unauthenticated tool access, so refuse to
		// operate rather than defaulting to permissive.
		middleware = func(context.Context, string, auth.Capability) error {
			return errors.New("plugins: no authorization middleware configured")
		}
	}
	return &Manager{
		log:        log,
		version:    version,
		middleware: middleware,
		approvals:  approvals,
		inline:     inline,
		mounted:    make(map[string]*Mounted),
	}
}

// Register mounts a plugin: validates its descriptor, collects its tools and
// mutations, and builds the MCP server that will serve its endpoint.
//
// Registration is explicit and happens at startup. Nothing is discovered by
// scanning, and no init() side effects are involved, so the set of plugins a
// binary can serve is exactly the set named in the composition root.
func (m *Manager) Register(ctx context.Context, p Plugin, instance string, required bool) error {
	d := p.Descriptor()

	// The configured name wins over the one the plugin declares. A plugin
	// knows what it is; only the host knows which of it this is, and the name
	// is what the endpoint, the tool prefix, and every operation record are
	// keyed on. Overriding centrally means a plugin author cannot get it
	// wrong by returning a constant.
	if instance != "" {
		d.Name = instance
	}
	if err := d.Validate(); err != nil {
		return err
	}

	m.mu.Lock()
	if _, dup := m.mounted[d.Name]; dup {
		m.mu.Unlock()
		return fmt.Errorf("plugins: %q is registered twice", d.Name)
	}
	m.mu.Unlock()

	reg := newRegistry(d)
	if err := p.Register(ctx, reg); err != nil {
		return fmt.Errorf("plugins: %s registration failed: %w", d.Name, err)
	}
	if err := reg.err(); err != nil {
		return err
	}
	if len(reg.tools) == 0 && len(reg.mutations) == 0 &&
		len(reg.resources) == 0 && len(reg.prompts) == 0 {
		return fmt.Errorf("plugins: %s registered nothing", d.Name)
	}

	srv := mcp.NewServer(&mcp.Implementation{
		Name:    d.Name,
		Title:   d.Title,
		Version: d.Version,
	}, &mcp.ServerOptions{
		Instructions: d.Description,
		Logger:       m.log.With("plugin", d.Name),
	})
	// The SDK panics on a malformed tool definition rather than returning an
	// error. A plugin -- especially an out-of-process one the operator dropped
	// in -- must not be able to take the host down that way, so registration
	// is recovered and reported as a failed mount.
	if err := attachAll(srv, reg, m.middleware, m.approvals, m.inline); err != nil {
		return fmt.Errorf("plugins: %s: %w", d.Name, err)
	}

	mounted := &Mounted{
		Descriptor: d,
		Server:     srv,
		Registry:   reg,
		Required:   required,
		plugin:     p,
		health:     Health{State: HealthyState, CheckedAt: time.Now()},
	}

	m.mu.Lock()
	m.mounted[d.Name] = mounted
	m.order = append(m.order, d.Name)
	m.mu.Unlock()

	m.invalidateAggregates()

	m.log.Info("plugin registered",
		"plugin", d.Name,
		"version", d.Version,
		"endpoint", d.Endpoint(),
		"tools", len(reg.tools),
		"mutations", len(reg.mutations),
		"required", required)
	return nil
}

// Start brings every registered plugin up, in registration order.
//
// A required plugin that fails to start fails host startup. An optional one is
// marked unhealthy and the host continues, so an outage in a single upstream
// system does not take down unrelated integrations.
func (m *Manager) Start(ctx context.Context) error {
	for _, name := range m.names() {
		mp := m.Lookup(name)
		starter, ok := mp.plugin.(Starter)
		if !ok {
			continue
		}
		if err := starter.Start(ctx); err != nil {
			if mp.Required {
				return fmt.Errorf("plugins: required plugin %s failed to start: %w", name, err)
			}
			mp.setHealth(Unhealthy(fmt.Sprintf("start failed: %v", err)))
			m.log.Error("optional plugin failed to start; continuing without it",
				"plugin", name, "error", err)
			continue
		}
		mp.setHealth(Healthy())
	}
	return nil
}

// Shutdown stops every plugin in reverse registration order, so a plugin that
// depends on one registered before it is stopped first.
func (m *Manager) Shutdown(ctx context.Context) error {
	names := m.names()
	var errs []error
	for i := len(names) - 1; i >= 0; i-- {
		mp := m.Lookup(names[i])
		stopper, ok := mp.plugin.(Stopper)
		if !ok {
			continue
		}
		if err := stopper.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("plugins: %s shutdown: %w", names[i], err))
		}
	}
	return errors.Join(errs...)
}

// CheckHealth refreshes health for every plugin implementing Checker.
func (m *Manager) CheckHealth(ctx context.Context) map[string]Health {
	out := make(map[string]Health)
	for _, name := range m.names() {
		mp := m.Lookup(name)
		if checker, ok := mp.plugin.(Checker); ok {
			mp.setHealth(checker.Check(ctx))
		}
		out[name] = mp.Health()
	}
	return out
}

// AggregateServer returns one MCP server exposing every named plugin.
//
// It exists so a single endpoint can serve everything a caller is granted,
// rather than requiring one connection per integration. Tool names already
// carry their plugin prefix, so combining them cannot collide.
//
// Servers are cached by the exact set of plugins, because building one
// re-registers every tool and a request must not pay that. The set is the
// cache key rather than the principal: two credentials granted the same
// plugins get the same server, and a credential whose grants change gets a
// different one.
func (m *Manager) AggregateServer(names []string) (*mcp.Server, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("plugins: no plugins to aggregate")
	}
	key := aggregateKey(names)

	m.aggMu.RLock()
	cached := m.aggregates[key]
	m.aggMu.RUnlock()
	if cached != nil {
		return cached, nil
	}

	m.aggMu.Lock()
	defer m.aggMu.Unlock()
	// Re-check: another request may have built it while we waited.
	if cached := m.aggregates[key]; cached != nil {
		return cached, nil
	}

	srv, err := m.BuildServer(names)
	if err != nil {
		return nil, err
	}

	if m.aggregates == nil {
		m.aggregates = make(map[string]*mcp.Server)
	}
	m.aggregates[key] = srv
	m.log.Info("built aggregate endpoint", "plugins", names)
	return srv, nil
}

// BuildServer returns a server that is nobody else's.
//
// AggregateServer caches by plugin set, which is right when identity arrives
// with each request. It is wrong when the identity is attached to the server
// itself: a caller that wraps the result in middleware carrying a principal
// would be writing that principal into an instance other callers share, and
// adding another layer every time it reconnected. The first identity attached
// would then answer for everyone, and changing it would appear to do nothing.
//
// A tunnel is exactly that caller, so it gets its own.
func (m *Manager) BuildServer(names []string) (*mcp.Server, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("plugins: no plugins to serve")
	}

	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "mcpd",
		Title:   "mcpd",
		Version: m.version,
	}, &mcp.ServerOptions{
		Instructions: m.aggregateInstructions(names),
		Logger:       m.log.With("endpoint", "aggregate"),
	})

	for _, name := range names {
		mounted := m.Lookup(name)
		if mounted == nil {
			continue
		}
		if err := attachAll(srv, mounted.Registry, m.middleware, m.approvals, m.inline); err != nil {
			return nil, fmt.Errorf("plugins: aggregate %s: %w", name, err)
		}
	}
	return srv, nil
}

// aggregateInstructions tells a model what it is looking at, since the tool
// list spans several systems rather than one.
func (m *Manager) aggregateInstructions(names []string) string {
	var b strings.Builder
	b.WriteString("This endpoint manages several systems. Tool names are prefixed " +
		"with the system they belong to." + "\n\n")
	for _, name := range names {
		mounted := m.Lookup(name)
		if mounted == nil {
			continue
		}
		fmt.Fprintf(&b, "- %s: %s\n", name, mounted.Descriptor.Description)
	}
	b.WriteString("\nChanges are never applied directly. A tool that changes " +
		"something records a proposal and returns an operation id; a person " +
		"must approve it before anything happens.")
	return b.String()
}

// aggregateKey builds a stable cache key from a plugin set.
func aggregateKey(names []string) string {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	return strings.Join(sorted, "\x00")
}

// invalidateAggregates drops cached servers, so a plugin registered after one
// was built is not missing from it.
func (m *Manager) invalidateAggregates() {
	m.aggMu.Lock()
	m.aggregates = nil
	m.aggMu.Unlock()
}

// Lookup returns a mounted plugin, or nil.
func (m *Manager) Lookup(name string) *Mounted {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.mounted[name]
}

// Names returns every registered plugin name, sorted.
func (m *Manager) Names() []string {
	names := m.names()
	sort.Strings(names)
	return names
}

// names returns registration order without sorting.
func (m *Manager) names() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]string(nil), m.order...)
}

// All returns every mounted plugin, in registration order.
func (m *Manager) All() []*Mounted {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Mounted, 0, len(m.order))
	for _, n := range m.order {
		out = append(out, m.mounted[n])
	}
	return out
}

// attachAll wires every tool and mutation onto an MCP server, converting a
// panic from the SDK into an error.
func attachAll(srv *mcp.Server, reg *Registry, mw ToolMiddleware, approvals ApprovalService, inline InlinePolicy) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("tool registration failed: %v", v)
		}
	}()

	for _, t := range reg.tools {
		t.attach(srv, mw)
	}
	for _, res := range reg.resources {
		res.attach(srv, mw)
	}
	for _, pr := range reg.prompts {
		pr.attach(srv, mw)
	}

	// Mutations become propose tools, and any endpoint with a mutation also
	// gets the operation lifecycle tools. Without an approval service there is
	// nowhere for a proposal to go, so registering one would produce a tool
	// that cannot work.
	if len(reg.mutations) > 0 {
		if approvals == nil {
			return fmt.Errorf("registers mutations but no approval service is configured")
		}
		for _, mu := range reg.mutations {
			mu.attach(srv, mw, approvals, inline)
		}
		attachApprovalTools(srv, reg.descriptor.Name, approvals, mw)
	}
	return nil
}
