package bandwidth

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/spoked/mcpd/internal/plugins"
)

// Plugin is the Bandwidth integration.
type Plugin struct {
	deps   plugins.Deps
	cfg    Config
	client *Client

	// configured reports whether a credential and an account were supplied. A
	// plugin without them still mounts, so its settings form has somewhere to
	// live.
	configured bool

	mu       sync.RWMutex
	lastErr  error
	checked  time.Time
	accounts []string
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

	client := NewClient(httpClient, cfg, deps.Log, now, observe)

	// The credential is not kept on the config the plugin holds, so a dump of
	// it -- a log line, an error, the settings page -- cannot carry one. The
	// token source has what it needs already.
	cfg.ClientID = ""
	cfg.ClientSecret = ""

	return &Plugin{
		deps:       deps,
		cfg:        cfg,
		client:     client,
		configured: configured,
	}, nil
}

// Descriptor returns the plugin's identity.
func (p *Plugin) Descriptor() plugins.Descriptor {
	return plugins.Descriptor{
		Name:    p.deps.Instance,
		Version: "1.0.0",
		Title:   "Bandwidth",
		Description: "Calls, messages, numbers, port-ins, 10DLC registration " +
			"and E911, across every Bandwidth account this credential " +
			"reaches. Read-only.",
	}
}

// Register declares the plugin's tools.
func (p *Plugin) Register(_ context.Context, r *plugins.Registry) error {
	p.registerAccountTools(r)
	p.registerVoiceTools(r)
	p.registerMessagingTools(r)
	p.registerNumberTools(r)
	p.registerPortingTools(r)
	p.registerInventoryTools(r)
	p.registerEstateTools(r)
	p.registerTenDLCTools(r)
	p.registerInsightsTools(r)
	p.registerErrorCodeTools(r)
	return nil
}

// Start implements plugins.Starter.
//
// One token exchange: the cheapest authenticated call there is, and the only
// one that proves the credential without reading a row of anybody's estate. It
// settles the address, TLS, whether the credential is a credential, and
// whether it covers the account this instance was told to read -- leaving only
// "does it have the right roles" to be discovered by a read, because Bandwidth
// will not say in advance.
func (p *Plugin) Start(ctx context.Context) error {
	if !p.configured {
		// Not an error the host should die on. The plugin is mounted, its
		// settings form is on the Plugins page, and Check says what is
		// missing -- which is the whole path someone follows to fix it.
		p.deps.Log.Info("bandwidth is not configured yet; add an API " +
			"credential and an account id on the Plugins page")
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()

	accounts, err := p.client.Probe(ctx)
	p.note(err, accounts)
	if err != nil {
		return err
	}

	fields := []any{"reading", p.client.Describe()}
	if len(accounts) > 0 {
		fields = append(fields, "credential_covers", strings.Join(accounts, ","))
	}
	// Nothing is said about when the credential expires, and the omission is
	// deliberate. The token carries an exp, but that is the token's -- minted
	// here, renewed automatically about a minute before it lapses, and never
	// something an operator acts on. The date that matters is the secret
	// expiry set when the credential was created, which the API does not
	// report at all. Reporting the token's exp in its place would be a
	// countdown that resets on every renewal: exactly the bug the
	// ExtremeCloud IQ integration shipped, where a sliding window was read as
	// a deadline and warned for ever about a date that never arrived.
	p.deps.Log.Info("bandwidth ready", fields...)
	return nil
}

// Check implements plugins.Checker.
//
// It reports what the last real call found rather than making one of its own.
// A health check that polls upstream on a schedule spends an account's rate
// budget to answer a question nobody asked.
func (p *Plugin) Check(_ context.Context) plugins.Health {
	if !p.configured {
		return plugins.Degraded("not configured yet — add a Bandwidth API " +
			"credential and the account id this instance should read")
	}

	p.mu.RLock()
	err, checked, accounts := p.lastErr, p.checked, p.accounts
	p.mu.RUnlock()

	switch {
	case checked.IsZero():
		return plugins.Degraded("has not reached Bandwidth yet")
	case err != nil:
		// Explained rather than passed through: the failure an operator meets
		// first is a credential whose roles or accounts are wrong, and the
		// stock wording for that is a bare 403.
		return plugins.Degraded("last call to Bandwidth failed: " +
			plugins.Explain(err).Error())
	}
	// A credential covering several accounts reads exactly one of them here,
	// and which one is the difference between an empty answer and a wrong
	// one. That belongs in the startup log and in Describe rather than here:
	// Health carries a state and a message, and there is no message on a
	// healthy plugin to hang it from.
	_ = accounts
	return plugins.Healthy()
}

// note records the outcome of a call for the health report.
func (p *Plugin) note(err error, accounts []string) {
	p.mu.Lock()
	p.lastErr, p.checked = err, p.deps.Now()
	if accounts != nil {
		p.accounts = accounts
	}
	p.mu.Unlock()
}

// ready refuses a tool call on an instance nobody has finished configuring.
//
// Checked in every tool rather than once at the transport, because the message
// is the point: a model told "not configured yet" stops and says so, and one
// handed a connection error tries three more tools first.
func (p *Plugin) ready() error {
	if !p.configured {
		return fmt.Errorf("bandwidth: not configured yet — add an API " +
			"credential and an account id on the Plugins page")
	}
	return nil
}
