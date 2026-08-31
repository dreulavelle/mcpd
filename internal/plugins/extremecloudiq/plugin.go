package extremecloudiq

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/spoked/mcpd/internal/plugins"
)

// Plugin is the ExtremeCloud IQ integration.
type Plugin struct {
	deps   plugins.Deps
	cfg    Config
	client *Client

	// configured reports whether a token was supplied. A plugin without one
	// still mounts, so its settings form has somewhere to live.
	configured bool

	mu      sync.RWMutex
	lastErr error
	checked time.Time
	// expires is when the token stops working, where the API said so. Held
	// rather than re-fetched: it is a property of the credential, it does not
	// change while the process runs, and the health report is the one place
	// somebody will see it before it bites.
	expires time.Time
}

// New constructs the plugin from resolved settings.
func New(deps plugins.Deps, cfg Config) (*Plugin, error) {
	cfg.withDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	token := cfg.APIToken
	configured := cfg.Configured()

	httpClient := deps.HTTP
	if httpClient != nil && cfg.Timeout > 0 {
		// A copy, because the host's client is shared and this plugin's
		// timeout is its own business.
		clone := *httpClient
		clone.Timeout = cfg.Timeout
		httpClient = &clone
	}

	// The credential is not kept on the config the plugin holds, so a dump of
	// it -- a log line, an error, the settings page -- cannot carry one.
	cfg.APIToken = ""

	now := deps.Now
	if now == nil {
		now = time.Now
	}
	// Written back, so every use of deps.Now below -- the health report, the
	// token expiry, a tool resolving a window -- goes through the same clock a
	// test injected rather than through the wall.
	deps.Now = now

	// One cache per instance, so two configured accounts cannot see each
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
		client:     NewClient(httpClient, cfg, token, deps.Log, now, cache, observe),
	}, nil
}

// Descriptor implements plugins.Plugin.
func (p *Plugin) Descriptor() plugins.Descriptor {
	return plugins.Descriptor{
		Name:    "extremecloudiq",
		Version: "0.1.0",
		Title:   "ExtremeCloud IQ",
		// Read twice over: it is the lede on this plugin's page in the
		// dashboard, and it is the instructions a client hands the model on
		// connect. Short enough for the first, specific enough for the second.
		Description: "Access points, switches, clients, alerts, and what is " +
			"going wrong with any of them. Read-only — every request is checked " +
			"against a list of read endpoints, so nothing here can onboard, " +
			"reboot, reconfigure or delete. Windowed reads say which window they " +
			"covered; listings say how many rows exist behind the ones returned.",
	}
}

// Register implements plugins.Plugin.
//
// Fourteen tools, grouped by the question somebody asks rather than by the
// endpoint that answers it. This API has 293 GET reads and about sixty more
// reached by POST, and a model asked "is the Springfield wireless working"
// should not be choosing between forty of them.
//
// They divide in two. Nine say what exists and what state it is in -- devices,
// clients, alerts, locations, policies. Five say what is *wrong*: which
// devices are not coping, which clients are failing and at which step, which
// sites to look at, and what the platform noticed by itself. The second group
// is the one somebody reaches for during an incident, and it is deliberately
// separate: "is this access point up" and "is this access point coping" are
// different questions with different answers, and a tool that blurred them
// would answer the first when asked the second.
//
// The grouping is for that, not for context. A tool list is a real
// per-conversation cost, paid whether the tools are called or not, but
// grouping does not reduce it: a composite result carries an output schema
// larger by about what the extra tool entries would have cost.
// TestToolList_StaysWithinItsContextBudget in internal/app measures it and
// fails if it grows.
func (p *Plugin) Register(_ context.Context, r *plugins.Registry) error {
	p.registerDeviceTools(r)
	p.registerClientTools(r)
	p.registerAlertTools(r)
	p.registerEstateTools(r)
	p.registerHealthTools(r)
	p.registerForensicsTools(r)
	return nil
}

// Start implements plugins.Starter.
//
// One call to /auth/apitoken/info: the cheapest authenticated request there
// is, and the only one that proves the credential without reading a row of
// anybody's estate. It settles four things a wrong configuration could be --
// the address does not resolve, TLS fails, the token is refused, or something
// that is not the API answered -- and leaves only the fifth, a token whose
// scopes do not cover a particular read, to be discovered by the read.
func (p *Plugin) Start(ctx context.Context) error {
	if !p.configured {
		// Not an error the host should die on. The plugin is mounted, its
		// settings form is on the Plugins page, and Check says what is
		// missing -- which is the whole path someone follows to fix it.
		p.deps.Log.Info("extremecloudiq is not configured yet; add an API " +
			"token on the Plugins page")
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

	// The data centre is logged because it is the thing that explains an
	// otherwise baffling report later: the address is regionless and routes to
	// wherever the account lives, so two tokens configured against the same
	// address can be reading two entirely different estates.
	fields := []any{
		"reading", p.client.Describe(),
		"account", info.UserName,
		"role", info.Role,
	}
	if info.DataCenter != "" {
		fields = append(fields, "data_centre", info.DataCenter)
	}
	if issued, ok := info.Issued(); ok {
		fields = append(fields, "token_issued", issued.UTC().Format(time.RFC3339))
	}
	if expiry, ok := info.Expiry(p.deps.Now()); ok {
		p.setExpiry(expiry)
		fields = append(fields, "token_expires", expiry.UTC().Format(time.RFC3339))
	} else if info.IsSession(p.deps.Now()) {
		// Said once, at Info, so the absence of an expiry is explained rather
		// than looking like something that failed to be read.
		fields = append(fields, "token_expiry", "not reported; the API describes "+
			"a per-request session whose window slides, not this key's own expiry")
	}
	if len(info.Scopes) > 0 {
		fields = append(fields, "scopes", strings.Join(info.Scopes, ","))
	}
	p.deps.Log.Info("extremecloudiq ready", fields...)

	// An expiry is only useful before it happens. Afterwards the API answers
	// 401, which is indistinguishable from a revoked token, and somebody
	// spends an afternoon on it.
	if expiry, ok := info.Expiry(p.deps.Now()); ok {
		if left := expiry.Sub(p.deps.Now()); left < tokenExpiryWarning {
			p.deps.Log.Warn("the ExtremeCloud IQ API token expires soon; when "+
				"it does, every call here is refused with the same 401 a "+
				"revoked token gets",
				"expires", expiry.UTC().Format(time.RFC3339),
				"in", humanSeconds(int(left/time.Second)))
		}
	}
	return nil
}

// Check implements plugins.Checker.
//
// It reports what the last real call found rather than making one of its own.
// A health check that polls upstream on a schedule spends an account's metered
// API budget to answer a question the next tool call answers for free, and
// readiness that depends on someone else's uptime reports mcpd as broken when
// it is not.
func (p *Plugin) Check(_ context.Context) plugins.Health {
	p.mu.RLock()
	err, checked, expires := p.lastErr, p.checked, p.expires
	p.mu.RUnlock()

	switch {
	case !p.configured:
		return plugins.Degraded("not configured yet — add an API token below, " +
			"then restart")
	case checked.IsZero():
		return plugins.Degraded("has not reached ExtremeCloud IQ yet")
	case err != nil:
		// Explained rather than passed through: the failure an operator meets
		// first is a certificate their own inspecting proxy signed, and the
		// stock wording for that names an unknown authority and no way to fix
		// it.
		return plugins.Degraded("last call to ExtremeCloud IQ failed: " +
			plugins.Explain(err).Error())
	}
	// Working, with a credential that will stop working. Said on the dashboard
	// rather than only in a startup log nobody re-reads.
	if !expires.IsZero() {
		if left := expires.Sub(p.deps.Now()); left <= 0 {
			return plugins.Degraded("the API token expired on " +
				expires.UTC().Format(time.RFC3339) + "; issue a new one in " +
				"Extreme Platform ONE, under your profile's API keys")
		} else if left < tokenExpiryWarning {
			return plugins.Degraded("working, but the API token expires in " +
				humanSeconds(int(left/time.Second)) + ", on " +
				expires.UTC().Format(time.RFC3339))
		}
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
		return fmt.Errorf("extremecloudiq: not configured yet — add an API " +
			"token on the Plugins page")
	}
	return nil
}

// note records the outcome of a call for the health report.
func (p *Plugin) note(err error) {
	p.mu.Lock()
	p.lastErr, p.checked = err, p.deps.Now()
	p.mu.Unlock()
}

// setExpiry records when the credential stops working.
func (p *Plugin) setExpiry(at time.Time) {
	p.mu.Lock()
	p.expires = at
	p.mu.Unlock()
}
