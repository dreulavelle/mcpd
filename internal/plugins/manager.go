package plugins

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
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

	mu      sync.RWMutex
	mounted map[string]*Mounted
	order   []string // registration order, for reverse-order shutdown
}

// NewManager returns an empty manager. middleware is the host gate applied to
// every tool call; version identifies the host in MCP handshakes.
func NewManager(log *slog.Logger, version string, middleware ToolMiddleware) *Manager {
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
		mounted:    make(map[string]*Mounted),
	}
}

// Register mounts a plugin: validates its descriptor, collects its tools and
// mutations, and builds the MCP server that will serve its endpoint.
//
// Registration is explicit and happens at startup. Nothing is discovered by
// scanning, and no init() side effects are involved, so the set of plugins a
// binary can serve is exactly the set named in the composition root.
func (m *Manager) Register(ctx context.Context, p Plugin, required bool) error {
	d := p.Descriptor()
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
	if len(reg.tools) == 0 && len(reg.mutations) == 0 {
		return fmt.Errorf("plugins: %s registered no tools or mutations", d.Name)
	}

	srv := mcp.NewServer(&mcp.Implementation{
		Name:    d.Name,
		Title:   d.Title,
		Version: d.Version,
	}, &mcp.ServerOptions{
		Instructions: d.Description,
		Logger:       m.log.With("plugin", d.Name),
	})
	for _, t := range reg.tools {
		t.attach(srv, m.middleware)
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
