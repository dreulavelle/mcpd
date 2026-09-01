package tunnel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
)

// Group owns every configured tunnel.
//
// There is one tunnel per connector, because a tunnel forwards to exactly one
// MCP endpoint: the control plane routes by tunnel id and the client binds a
// single URL. Serving one plugin per connector therefore means one tunnel per
// plugin, each bound to that plugin's endpoint.
//
// The alternative -- a single tunnel on the aggregate endpoint -- is still
// available and is what an empty plugin name means. Both can run at once: a
// general connector alongside a narrow one is a reasonable arrangement, and
// nothing here assumes otherwise.
type Group struct {
	log *slog.Logger

	// OnFailure is told when any tunnel in the group stops serving. Set once
	// by the composition root, before Apply; nil where nobody is listening.
	//
	// It lives here rather than on Config because it is a property of the
	// host rather than of any one tunnel, and every manager the group builds
	// should reach the same place.
	OnFailure func(plugin, tunnelID, reason string)

	mu       sync.RWMutex
	managers map[string]*Manager
	order    []string
	// started reports whether the host has reached the point of serving.
	//
	// Before that, Apply only configures. An HTTP-bound tunnel client probes
	// mcpd as it starts, and a probe that lands before mcpd is answering fails
	// and stays failed -- so the composition root can describe its tunnels
	// without connecting them, and the lifecycle connects them when there is
	// something to connect to.
	started bool
}

// NewGroup returns an empty group.
func NewGroup(log *slog.Logger) *Group {
	return &Group{log: log, managers: make(map[string]*Manager)}
}

// Key names a tunnel within the group.
//
// The tunnel id, because a plugin no longer identifies a tunnel: two ChatGPT
// workspaces sharing one integration is two tunnels serving the same plugin,
// and keying on the plugin name made the second replace the first in the
// running group as well as in the settings.
//
// This used to key on the plugin, on the reasoning that a tunnel id is a value
// the operator is in the middle of editing. That stopped being true when
// tunnels were made from the Tunnels page rather than pasted in: the id comes
// from the tunnel that was just created and does not change afterwards.
func Key(tunnelID string) string {
	if tunnelID == "" {
		return aggregateKey
	}
	return tunnelID
}

// aggregateKey names the tunnel serving every plugin a caller is granted.
const aggregateKey = "*"

// Apply reconciles the running tunnels with the configuration given.
//
// Tunnels that disappeared are stopped, new ones are started, and changed ones
// are restarted. Reconciling rather than rebuilding matters because restarting
// a tunnel drops a connector for as long as it takes to reconnect: an operator
// who pasted an id for one plugin should not disturb the others.
func (g *Group) Apply(ctx context.Context, configs []Config, factory ServerFactory) error {
	wanted := make(map[string]Config, len(configs))
	order := make([]string, 0, len(configs))
	for _, cfg := range configs {
		key := Key(cfg.TunnelID)
		if _, dup := wanted[key]; dup {
			// Two tunnels serving one plugin is ordinary now. One tunnel id
			// used twice is not: the control plane allows a single client per
			// tunnel, so the two would compete for the same commands.
			return fmt.Errorf("tunnel: tunnel %s is configured twice", cfg.TunnelID)
		}
		wanted[key] = cfg
		order = append(order, key)
	}

	g.mu.Lock()
	existing := make(map[string]*Manager, len(g.managers))
	for k, m := range g.managers {
		existing[k] = m
	}
	g.mu.Unlock()

	var errs []error

	// Remove first, so a tunnel id moved from one plugin to another is not
	// briefly running twice. The control plane allows only one client per
	// tunnel, and the second would fight the first.
	for key, m := range existing {
		if _, keep := wanted[key]; keep {
			continue
		}
		if err := m.Stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("tunnel: stop %s: %w", key, err))
		}
		g.mu.Lock()
		delete(g.managers, key)
		g.mu.Unlock()
	}

	for _, key := range order {
		cfg := wanted[key]
		if m, ok := existing[key]; ok {
			if m.SameAs(cfg) {
				continue
			}
			if err := m.Reconfigure(ctx, cfg); err != nil {
				errs = append(errs, fmt.Errorf("tunnel: %s: %w", describe(cfg.Plugin), err))
			}
			continue
		}

		m := NewManager(cfg, factory, g.log.With("tunnel", key))
		m.onFailure = g.OnFailure
		g.mu.Lock()
		g.managers[key] = m
		g.mu.Unlock()

		if g.started && cfg.Enabled && cfg.TunnelID != "" {
			if err := m.Start(ctx); err != nil {
				// One tunnel failing must not stop the others: a mistyped id
				// for one plugin should cost that plugin's connector, not
				// every connector.
				errs = append(errs, fmt.Errorf("tunnel: %s: %w", describe(cfg.Plugin), err))
			}
		}
	}

	g.mu.Lock()
	g.order = order
	g.mu.Unlock()

	return errors.Join(errs...)
}

// Rebuild restarts the tunnel serving one plugin, so that it picks up a
// plugin that has been rebuilt underneath it.
//
// Apply cannot do this. It compares configurations and skips a tunnel whose
// own config is unchanged, which is exactly the case here: editing an
// Observium token changes the plugin's settings and nothing about the tunnel.
// But a tunnel builds its own MCP server at start, and that server holds the
// plugin instance it was built from -- so a remounted plugin leaves the tunnel
// serving the old one, with the old credential.
//
// The failure that came from this is worth recording, because it reads as
// something else entirely: after the operator replaced a revoked API token,
// the dashboard worked and the connector did not. Same mcpd, same plugin, same
// moment -- one path had the new credential and the other was still presenting
// the token that had just been revoked, and the connector reported that
// Observium had rejected mcpd's credentials, which was true and not the fault
// of anything the operator could see.
//
// A rebuild drops the connector for as long as it takes to reconnect. That is
// the cost of the credential actually changing, and it is smaller than the
// alternative of a connector that authenticates with a secret nobody can
// correct.
func (g *Group) Rebuild(ctx context.Context, plugin string, factory ServerFactory) error {
	// Every tunnel serving it, not one. A plugin shared by two ChatGPT
	// workspaces has a tunnel each, and rebuilding only the first would leave
	// the second answering with the settings it started with -- which is the
	// same silent staleness this whole change exists to remove.
	var keys []string
	g.mu.RLock()
	for key, m := range g.managers {
		if m.Config().Plugin == plugin {
			keys = append(keys, key)
		}
	}
	g.mu.RUnlock()
	sort.Strings(keys)

	var errs []error
	for _, key := range keys {
		if err := g.rebuildOne(ctx, key, factory); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// rebuildOne restarts the tunnel under one key.
func (g *Group) rebuildOne(ctx context.Context, key string, factory ServerFactory) error {
	g.mu.RLock()
	existing, ok := g.managers[key]
	g.mu.RUnlock()
	if !ok {
		// No tunnel serves this plugin. Not an error: most plugins have no
		// connector of their own.
		return nil
	}

	cfg := existing.Config()
	if err := existing.Stop(ctx); err != nil {
		return fmt.Errorf("tunnel: stop %s: %w", key, err)
	}

	m := NewManager(cfg, factory, g.log.With("tunnel", key))
	m.onFailure = g.OnFailure
	g.mu.Lock()
	g.managers[key] = m
	g.mu.Unlock()

	if !g.started || !cfg.Enabled || cfg.TunnelID == "" {
		return nil
	}
	if err := m.Start(ctx); err != nil {
		return fmt.Errorf("tunnel: %s: %w", describe(cfg.Plugin), err)
	}
	g.log.InfoContext(ctx, "tunnel rebuilt for a changed plugin",
		"tunnel", key, "plugin", cfg.Plugin)
	return nil
}

// Status reports every tunnel, in configuration order.
func (g *Group) Status() []Status {
	g.mu.RLock()
	defer g.mu.RUnlock()

	out := make([]Status, 0, len(g.order))
	for _, key := range g.order {
		if m := g.managers[key]; m != nil {
			out = append(out, m.Status())
		}
	}
	return out
}

// Lookup returns one tunnel by plugin name, or nil.
func (g *Group) Lookup(plugin string) *Manager {
	g.mu.RLock()
	defer g.mu.RUnlock()

	// The keys are tunnel ids now, so a plugin has to be searched for. Stable
	// order, because a plugin served by two tunnels would otherwise answer
	// with whichever the map happened to yield.
	var found []string
	for key, m := range g.managers {
		if m.Config().Plugin == plugin {
			found = append(found, key)
		}
	}
	if len(found) == 0 {
		return nil
	}
	sort.Strings(found)
	return g.managers[found[0]]
}

// Start brings up every configured tunnel, and marks the group live so that
// tunnels configured from here on start as soon as they are applied.
func (g *Group) Start(ctx context.Context) error {
	g.mu.Lock()
	g.started = true
	g.mu.Unlock()

	var errs []error
	for _, m := range g.all() {
		if !m.Enabled() {
			continue
		}
		if err := m.Start(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Stop disconnects every tunnel.
func (g *Group) Stop(ctx context.Context) error {
	var errs []error
	for _, m := range g.all() {
		if err := m.Stop(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Enabled reports whether any tunnel is configured.
func (g *Group) Enabled() bool {
	for _, m := range g.all() {
		if m.Enabled() {
			return true
		}
	}
	return false
}

func (g *Group) all() []*Manager {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]*Manager, 0, len(g.order))
	for _, key := range g.order {
		if m := g.managers[key]; m != nil {
			out = append(out, m)
		}
	}
	return out
}

// describe names a tunnel the way an operator refers to it.
func describe(plugin string) string {
	if plugin == "" {
		return "the connector for everything"
	}
	return "the " + plugin + " connector"
}
