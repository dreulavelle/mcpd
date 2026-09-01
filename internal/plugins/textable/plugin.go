package textable

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/spoked/mcpd/internal/plugins"
)

// Plugin is the Textable integration.
type Plugin struct {
	deps   plugins.Deps
	cfg    Config
	client *Client

	// configured reports whether an address and a key were supplied. A plugin
	// without them still mounts, so its settings form has somewhere to live.
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
	key := cfg.APIKey
	configured := cfg.Configured()

	httpClient := deps.HTTP
	if httpClient != nil && cfg.Timeout > 0 {
		// A copy, because the host's client is shared and this plugin's timeout
		// is its own business.
		clone := *httpClient
		clone.Timeout = cfg.Timeout
		httpClient = &clone
	}

	// The credential is not kept on the config the plugin holds, so a dump of
	// it -- a log line, an error, the settings page -- cannot carry one.
	cfg.APIKey = ""

	now := deps.Now
	if now == nil {
		now = time.Now
	}

	// One cache per instance, so two configured tenants cannot see each other's
	// answers even by accident. Nil when caching is switched off, which makes
	// "no caching" cost nothing rather than cost a lookup.
	var cache *readCache
	if cfg.CacheSeconds > 0 {
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
		client:     NewClient(httpClient, cfg, key, deps.Log, now, cache, observe),
	}, nil
}

// Descriptor implements plugins.Plugin.
func (p *Plugin) Descriptor() plugins.Descriptor {
	return plugins.Descriptor{
		Name:    "textable",
		Version: "0.1.0",
		Title:   "Textable",
		Description: "Reads a Textable business-SMS instance: the users who " +
			"send from it, the organizations they belong to, a user's contacts, " +
			"and the drip campaigns and canned responses that send " +
			"automatically. Read-only. It will not create, edit or delete " +
			"anything, and every request is checked against a list of read " +
			"endpoints rather than merely being a method nothing writes with.\n\n" +
			"What it can see is decided by the key, not by mcpd. Contacts, " +
			"drip campaigns and canned responses are always the key owner's " +
			"own; listings of users and organizations need a key belonging to " +
			"an admin and then cover the whole instance. Every tool says which " +
			"of the two it is.",
	}
}

// Register implements plugins.Plugin.
//
// Seven tools in two groups, split by where an answer comes from rather than by
// what it is about. The directory tools are served by one cached call to the
// tenant report and are how a caller finds an id at all; the detail tools each
// read one thing by the id they were given.
func (p *Plugin) Register(_ context.Context, r *plugins.Registry) error {
	p.registerDirectoryTools(r)
	p.registerDetailTools(r)
	return nil
}

// Start implements plugins.Starter.
//
// Two calls, in this order, because they fail differently and the difference is
// the whole value of probing at all. /health is unauthenticated: it proves the
// address resolves, TLS works and the thing answering is Textable rather than a
// gateway. /api/canned-responses then proves the key is accepted. Run together
// they would produce one message covering four causes; run apart they produce
// the one that is true.
func (p *Plugin) Start(ctx context.Context) error {
	if !p.configured {
		// Not an error the host should die on. The plugin is mounted, its
		// settings form is on the Plugins page, and Check says what is missing
		// -- which is the whole path someone follows to fix it.
		p.deps.Log.Info("textable is not configured yet; add its address and " +
			"a service account token on the Plugins page")
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()

	health, err := p.client.Probe(ctx)
	if err != nil {
		p.note(err)
		return err
	}
	if err := p.client.ProbeAuth(ctx); err != nil {
		p.note(err)
		return err
	}
	p.note(nil)

	// No identifier for the credential is logged, because a service account
	// token has none that is not the token: unlike a user key, whose account
	// uid sits in front of a colon and is safe to print, this is one opaque
	// string all the way through. What the connector can see is decided by the
	// token's scopes, which are visible in Textable rather than from here.
	p.deps.Log.Info("textable ready",
		"reading", p.client.Describe(),
		"version", health.Version,
		"release", health.ReleaseID)
	if !health.ok() {
		p.deps.Log.Warn("textable reports that it is not well, so reads may be "+
			"slow or incomplete", "status", health.Status)
	}
	return nil
}

// Check implements plugins.Checker.
//
// It reports what the last real call found rather than making one of its own. A
// health check that polls upstream on a schedule spends a tenant's capacity to
// answer a question the next tool call answers for free, and readiness that
// depends on someone else's uptime reports mcpd as broken when it is not.
func (p *Plugin) Check(_ context.Context) plugins.Health {
	p.mu.RLock()
	err, checked := p.lastErr, p.checked
	p.mu.RUnlock()

	switch {
	case !p.configured:
		return plugins.Degraded("not configured yet — set the address and a " +
			"service account token below, then restart")
	case checked.IsZero():
		return plugins.Degraded("has not reached Textable yet")
	case err != nil:
		return plugins.Degraded("last call to Textable failed: " +
			plugins.Explain(err).Error())
	}
	return plugins.Healthy()
}

// ready refuses a tool call on an instance nobody has finished configuring.
//
// Checked in every tool rather than once at the transport, because the message
// is the point: a model told "not configured yet" stops and says so, and one
// handed a connection error tries three more tools first.
func (p *Plugin) ready() error {
	if !p.configured {
		return fmt.Errorf("textable: not configured yet — set its address and " +
			"a service account token on the Plugins page")
	}
	return nil
}

// note records the outcome of a call for the health report.
func (p *Plugin) note(err error) {
	p.mu.Lock()
	p.lastErr, p.checked = err, p.deps.Now()
	p.mu.Unlock()
}

// limitOf resolves a caller's requested ceiling against the instance's.
//
// A caller asking for more than the operator allows gets the operator's number
// rather than an error: the request is reasonable, the answer is simply bounded,
// and the result says so.
func (p *Plugin) limitOf(requested int) int {
	if requested <= 0 || requested > p.cfg.MaxItems {
		return p.cfg.MaxItems
	}
	return requested
}
