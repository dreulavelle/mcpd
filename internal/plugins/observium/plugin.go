package observium

import (
	"context"
	"sync"
	"time"

	"github.com/spoked/mcpd/internal/plugins"
)

// Plugin is the Observium integration.
type Plugin struct {
	deps   plugins.Deps
	cfg    Config
	client *Client

	// configured reports whether an address and a credential were supplied. A
	// plugin without them still mounts, so its settings form has somewhere to
	// live.
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
	token, user, pass := cfg.Token, cfg.Username, cfg.Password
	configured := cfg.Configured()

	httpClient := deps.HTTP
	if httpClient != nil && cfg.Timeout > 0 {
		// A copy, because the host's client is shared and this plugin's
		// timeout is its own business.
		clone := *httpClient
		clone.Timeout = cfg.Timeout
		httpClient = &clone
	}

	// Credentials are not kept on the config the plugin holds, so a dump of it
	// -- a log line, an error, the settings page -- cannot carry one.
	cfg.Token, cfg.Username, cfg.Password = "", "", ""

	now := deps.Now
	if now == nil {
		now = time.Now
	}

	// One cache per instance, so two configured accounts cannot see each
	// other's answers even by accident. Nil when both classes are switched
	// off, which makes "no caching" cost nothing rather than cost a lookup.
	var cache *readCache
	if cfg.StateCacheSeconds > 0 || cfg.InventoryCacheSeconds > 0 {
		cache = newReadCache(deps.Instance, cfg, now, deps.Cache)
	}

	instance := deps.Instance
	observe := func(outcome string, d time.Duration) {}
	if deps.Upstream != nil {
		observe = func(outcome string, d time.Duration) {
			deps.Upstream.UpstreamRequest(instance, outcome, d)
		}
	}

	return &Plugin{
		deps:       deps,
		cfg:        cfg,
		configured: configured,
		client:     NewClient(httpClient, cfg, token, user, pass, deps.Log, now, cache, observe),
	}, nil
}

// Descriptor implements plugins.Plugin.
func (p *Plugin) Descriptor() plugins.Descriptor {
	return plugins.Descriptor{
		Name:    "observium",
		Version: "0.1.0",
		Title:   "Observium",
		Description: "Reads an Observium network monitoring estate: devices, " +
			"interfaces, sensors, capacity, topology and alerts. Read-only. It " +
			"will not add, modify or delete a device, and those endpoints are " +
			"refused rather than simply unimplemented -- a device deleted " +
			"through the API takes its recorded history with it. " +
			"Observium keeps its time series in RRD and does not serve them as " +
			"data, so there is no tool that returns a trend: the graph tool " +
			"returns image links for a person to open.",
	}
}

// Start implements plugins.Starter.
//
// One device, one page: the cheapest authenticated call there is. It proves
// the address resolves, TLS works, the credential is accepted and the response
// is the API's JSON rather than a sign-in page, which are four things a wrong
// configuration could be. Doing it at startup makes a wrong token a message on
// the dashboard rather than a confusing failure inside the first tool call an
// assistant makes.
func (p *Plugin) Start(ctx context.Context) error {
	if !p.configured {
		// Not an error the host should die on. The plugin is mounted, its
		// settings form is on the Plugins page, and Check says what is
		// missing -- which is the whole path someone follows to fix it.
		p.deps.Log.Info("observium is not configured yet; add its address and " +
			"an API token on the Plugins page")
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()

	if err := p.client.Probe(ctx); err != nil {
		p.note(err)
		return err
	}
	p.note(nil)
	p.deps.Log.Info("observium ready", "reading", p.client.Describe())
	return nil
}

// Check implements plugins.Checker.
//
// It reports what the last real call found rather than making one of its own.
// A health check that polls upstream on a schedule spends an installation's
// capacity to answer a question the next tool call answers for free, and
// readiness that depends on someone else's uptime reports mcpd as broken when
// it is not.
func (p *Plugin) Check(_ context.Context) plugins.Health {
	p.mu.RLock()
	err, checked := p.lastErr, p.checked
	p.mu.RUnlock()

	switch {
	case !p.configured:
		return plugins.Degraded("not configured yet — set the address and " +
			"either an API token or a username and password below, then restart")
	case checked.IsZero():
		return plugins.Degraded("has not reached Observium yet")
	case err != nil:
		return plugins.Degraded("last call to Observium failed: " + err.Error())
	}
	return plugins.Healthy()
}

// note records the outcome of a call for the health report.
func (p *Plugin) note(err error) {
	p.mu.Lock()
	p.lastErr, p.checked = err, p.deps.Now()
	p.mu.Unlock()
}
