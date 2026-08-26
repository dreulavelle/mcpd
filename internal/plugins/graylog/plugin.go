package graylog

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/spoked/mcpd/internal/plugins"
)

// Plugin is the Graylog integration.
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
		// timeout is its own business -- and it is longer than most, because a
		// search over a wide window is slow rather than broken.
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

	// One cache per instance, so two configured installations cannot see each
	// other's answers even by accident. Nil when caching is switched off,
	// which makes "no caching" cost nothing rather than cost a lookup.
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
		client:     NewClient(httpClient, cfg, token, user, pass, deps.Log, now, cache, observe),
	}, nil
}

// Descriptor implements plugins.Plugin.
func (p *Plugin) Descriptor() plugins.Descriptor {
	return plugins.Descriptor{
		Name:    "graylog",
		Version: "0.1.0",
		Title:   "Graylog",
		Description: "Reads a Graylog installation: log messages, counts and " +
			"summaries over them, the alerts and rules built on them, and " +
			"whether Graylog itself is well. Read-only. It will not create, " +
			"edit or delete anything, and every request is checked against a " +
			"list of read endpoints rather than merely being a method nothing " +
			"writes with.\n\n" +
			"Searches always cover a bounded window and say which one they " +
			"covered. Results come back as columns and positional rows rather " +
			"than as objects, which is what makes a page of log lines fit in " +
			"a conversation.",
	}
}

// Register implements plugins.Plugin.
//
// Seven tools, grouped by the question somebody asks rather than by the
// endpoint that answers it. Graylog's API has hundreds of routes, and a model
// asked "what does the log say" should not be choosing between nine tools that
// each answer part of it.
//
// The grouping is for that, not for context. A tool list is a real
// per-conversation cost -- these seven are about four thousand tokens, paid
// whether they are called or not -- but grouping does not reduce it: the
// composite results carry output schemas larger by about what the extra tool
// entries would have cost. TestToolList_StaysWithinItsContextBudget in
// internal/app measures it and fails if it grows.
func (p *Plugin) Register(_ context.Context, r *plugins.Registry) error {
	p.registerSearchTools(r)
	p.registerEventTools(r)
	p.registerSystemTools(r)
	return nil
}

// Start implements plugins.Starter.
//
// One call to /api/system: the cheapest authenticated request there is. It
// proves the address resolves, TLS works, the credential is accepted and the
// response is the API's JSON rather than a sign-in page, which are four things
// a wrong configuration could be. Doing it at startup makes a wrong token a
// message on the dashboard rather than a confusing failure inside the first
// tool call an assistant makes.
func (p *Plugin) Start(ctx context.Context) error {
	if !p.configured {
		// Not an error the host should die on. The plugin is mounted, its
		// settings form is on the Plugins page, and Check says what is
		// missing -- which is the whole path someone follows to fix it.
		p.deps.Log.Info("graylog is not configured yet; add its address and " +
			"an access token on the Plugins page")
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()

	info, err := p.client.Probe(ctx)
	if err != nil {
		p.note(err)
		return err
	}
	p.note(nil)

	// The version is logged because it is the single most useful thing to know
	// when an endpoint answers 404 later: this integration is written against
	// the API as it stands in 7.x, and the same call against an older
	// installation fails in a way that looks like a permissions problem.
	p.deps.Log.Info("graylog ready",
		"reading", p.client.Describe(),
		"version", info.Version,
		"processing", info.IsProcessing)
	if !info.IsProcessing {
		p.deps.Log.Warn("graylog reports that it is not processing messages, " +
			"so searches will not see anything arriving now")
	}
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
			"either an access token or a username and password below, then restart")
	case checked.IsZero():
		return plugins.Degraded("has not reached Graylog yet")
	case err != nil:
		return plugins.Degraded("last call to Graylog failed: " + err.Error())
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
		return fmt.Errorf("graylog: not configured yet — set its address and " +
			"an access token on the Plugins page")
	}
	return nil
}

// note records the outcome of a call for the health report.
func (p *Plugin) note(err error) {
	p.mu.Lock()
	p.lastErr, p.checked = err, p.deps.Now()
	p.mu.Unlock()
}
