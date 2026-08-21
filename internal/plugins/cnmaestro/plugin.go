package cnmaestro

import (
	"context"
	"sync"
	"time"

	"github.com/spoked/mcpd/internal/plugins"
)

// Plugin is the cnMaestro integration.
type Plugin struct {
	deps   plugins.Deps
	cfg    Config
	client *Client

	// configured reports whether credentials were supplied. A plugin without
	// them still mounts, so its settings form has somewhere to live.
	configured bool

	mu      sync.RWMutex
	lastErr error
	checked time.Time
}

// New constructs the plugin from resolved settings.
func New(deps plugins.Deps, cfg Config) (*Plugin, error) {
	cfg.withDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	clientID, secret := cfg.ClientID, cfg.ClientSecret
	configured := cfg.Configured()

	http := deps.HTTP
	if http != nil && cfg.Timeout > 0 {
		// A copy, because the host's client is shared and this plugin's
		// timeout is its own business.
		clone := *http
		clone.Timeout = cfg.Timeout
		http = &clone
	}

	// The credential is not kept on the config the plugin holds, so a dump of
	// it -- a log line, an error, the settings page -- cannot carry one.
	cfg.ClientID, cfg.ClientSecret = "", ""

	return &Plugin{
		deps:       deps,
		cfg:        cfg,
		configured: configured,
		client:     NewClient(http, cfg, clientID, secret, deps.Log, deps.Now),
	}, nil
}

// Descriptor implements plugins.Plugin.
func (p *Plugin) Descriptor() plugins.Descriptor {
	return plugins.Descriptor{
		Name:    "cnmaestro",
		Version: "0.1.0",
		Title:   "Cambium cnMaestro",
		Description: "Reads a Cambium cnMaestro estate: networks, devices, and " +
			"the state of each. Read-only. It will not run commands on a " +
			"device, reboot one, or disconnect clients, and those endpoints " +
			"are refused rather than simply unimplemented.",
	}
}

// Start implements plugins.Starter.
//
// It obtains a token, which is the cheapest way to establish that the
// credentials work and to learn which regional host to use. Doing it at
// startup means a wrong client id is a message on the dashboard rather than a
// confusing failure inside the first tool call an assistant makes.
func (p *Plugin) Start(ctx context.Context) error {
	if !p.configured {
		// Not an error the host should die on. The plugin is mounted, its
		// settings form is on the Plugins page, and Check says what is
		// missing -- which is the whole path someone follows to fix it.
		p.deps.Log.Info("cnmaestro has no credentials yet; " +
			"add them on the Plugins page")
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()

	if _, _, err := p.client.tokens.token(ctx); err != nil {
		p.note(err)
		return err
	}
	p.note(nil)
	p.deps.Log.Info("cnmaestro ready", "api_host", p.client.APIHost(),
		"managed_account", p.cfg.ManagedAccount)
	return nil
}

// Check implements plugins.Checker.
//
// It reports what the last real call found rather than making one of its own.
// A health check that calls upstream on a schedule spends an estate's rate
// limit to answer a question the next tool call answers for free, and readiness
// that depends on someone else's uptime reports mcpd as broken when it is not.
func (p *Plugin) Check(_ context.Context) plugins.Health {
	p.mu.RLock()
	err, checked := p.lastErr, p.checked
	p.mu.RUnlock()

	switch {
	case !p.configured:
		return plugins.Degraded("no API credentials yet — add a client ID and " +
			"secret below, then restart")
	case checked.IsZero():
		return plugins.Degraded("has not reached cnMaestro yet")
	case err != nil:
		return plugins.Degraded("last call to cnMaestro failed: " + err.Error())
	}
	return plugins.Healthy()
}

// note records the outcome of a call for the health report.
func (p *Plugin) note(err error) {
	p.mu.Lock()
	p.lastErr, p.checked = err, p.deps.Now()
	p.mu.Unlock()
}
