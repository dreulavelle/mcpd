package bookstack

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/spoked/mcpd/internal/plugins"
)

// Plugin is the BookStack integration: one instance, one knowledge base.
type Plugin struct {
	deps   plugins.Deps
	cfg    Config
	client *Client

	// configured reports whether an address and both halves of the token were
	// supplied. A plugin without them still mounts, so its settings form has
	// somewhere to live.
	configured bool

	mu      sync.RWMutex
	lastErr error
	checked time.Time
	system  SystemInfo
}

// New constructs the plugin from resolved settings.
func New(deps plugins.Deps, cfg Config) (*Plugin, error) {
	cfg.withDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	configured := cfg.Configured()

	httpClient := deps.HTTP
	if httpClient != nil && cfg.Timeout > 0 {
		// A copy, because the host's client is shared and this plugin's
		// timeout is its own business.
		clone := *httpClient
		clone.Timeout = cfg.Timeout
		httpClient = &clone
	}

	now := deps.Now
	if now == nil {
		now = time.Now
	}
	// Written back, so every use of deps.Now below goes through the same clock
	// a test injected rather than through the wall.
	deps.Now = now

	instance := deps.Instance
	observe := func(outcome string, d time.Duration) {}
	if deps.Upstream != nil {
		observe = func(outcome string, d time.Duration) {
			deps.Upstream.UpstreamRequest(instance, outcome, d)
		}
	}

	p := &Plugin{deps: deps, configured: configured}
	if configured {
		client, err := NewClient(httpClient, cfg, deps.Log, now, observe)
		if err != nil {
			return nil, err
		}
		p.client = client
	}

	// The token is not kept on the config the plugin holds, so a dump of it --
	// a log line, an error, the settings page -- cannot carry one. The client
	// has what it needs already.
	cfg.TokenID = ""
	cfg.TokenSecret = ""
	p.cfg = cfg
	return p, nil
}

// Descriptor implements plugins.Plugin.
func (p *Plugin) Descriptor() plugins.Descriptor {
	return plugins.Descriptor{
		Name:    p.deps.Instance,
		Version: "0.1.0",
		Title:   "BookStack",
		Description: "Your knowledge base: search it, read shelves, books, " +
			"chapters and pages, and propose changes to them. Reading happens " +
			"straight away; anything that changes the knowledge base is shown " +
			"in full and has to be approved before it is applied.",
	}
}

// Start implements plugins.Starter.
//
// One read of /api/system: the only endpoint that authenticates without
// touching anybody's content. It settles the address, the TLS, whether the
// token is accepted, and what version is on the other end, and it reads no
// page -- which is the right thing for a check that runs on every start.
func (p *Plugin) Start(ctx context.Context) error {
	if !p.configured {
		// Not an error the host should die on. The plugin is mounted, its
		// settings form is on the Plugins page, and Check says what is
		// missing -- which is the whole path somebody follows to fix it.
		p.deps.Log.Info("bookstack is not configured yet; add the address and an " +
			"API token on the Plugins page")
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()

	info, err := p.client.Probe(ctx)
	p.note(err, info)
	if err != nil {
		return err
	}
	p.deps.Log.Info("bookstack ready",
		"instance", p.client.Describe(), "version", info.Version, "name", info.AppName)
	return nil
}

// Check implements plugins.Checker.
//
// It reports what the last real call found rather than making one of its own.
// A health check that polls upstream on a schedule spends the instance's
// throttle budget to answer a question nobody asked.
func (p *Plugin) Check(_ context.Context) plugins.Health {
	if !p.configured {
		return plugins.Degraded("not configured yet — add the BookStack address " +
			"and an API token ID and secret")
	}

	p.mu.RLock()
	err, checked := p.lastErr, p.checked
	p.mu.RUnlock()

	switch {
	case checked.IsZero():
		return plugins.Degraded("has not reached BookStack yet")
	case err != nil:
		// Explained rather than passed through: the failure an operator meets
		// first is a revoked token or a user whose role lost a permission, and
		// the stock wording for both is a bare status code.
		return plugins.Degraded("last call to BookStack failed: " +
			plugins.Explain(err).Error())
	}
	return plugins.Healthy()
}

// note records the outcome of a call for the health report.
//
// An absent resource is not a failure. "There is no page 4102" is a successful
// round trip that proves the address, the token and the permission; recording
// it as the last error would show the integration as degraded because
// somebody asked about a page that had been deleted.
func (p *Plugin) note(err error, info SystemInfo) {
	if isNotFound(err) {
		err = nil
	}
	p.mu.Lock()
	p.lastErr, p.checked = err, p.deps.Now()
	if info.Version != "" {
		p.system = info
	}
	p.mu.Unlock()
}

// noted records an outcome without a system read.
func (p *Plugin) noted(err error) { p.note(err, SystemInfo{}) }

// ready refuses a call on an instance nobody has finished configuring.
//
// Checked in every tool rather than once at the transport, because the message
// is the point: a model told "not configured yet" stops and says so, and one
// handed a connection error tries three more tools first.
func (p *Plugin) ready() error {
	if !p.configured {
		return fmt.Errorf("bookstack: not configured yet — add the address and an " +
			"API token ID and secret on the Plugins page")
	}
	return nil
}

// Register implements plugins.Plugin.
func (p *Plugin) Register(_ context.Context, r *plugins.Registry) error {
	p.registerContentTools(r)
	p.registerSearchTools(r)
	p.registerPeopleTools(r)
	p.registerActivityTools(r)
	p.registerMutations(r)
	return nil
}
