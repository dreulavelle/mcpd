package threecx

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/spoked/mcpd/internal/plugins"
)

// Plugin is the 3CX integration.
type Plugin struct {
	deps   plugins.Deps
	cfg    Config
	client *Client

	// configured reports whether an address, an extension and a password were
	// supplied. A plugin without them still mounts, so its settings form has
	// somewhere to live.
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
	extension, password := cfg.Extension, cfg.Password
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
	cfg.Password = ""

	now := deps.Now
	if now == nil {
		now = time.Now
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
		client:     NewClient(httpClient, cfg, extension, password, deps.Log, now, observe),
	}, nil
}

// Descriptor implements plugins.Plugin.
func (p *Plugin) Descriptor() plugins.Descriptor {
	return plugins.Descriptor{
		Name:    "threecx",
		Version: "0.1.0",
		Title:   "3CX",
		Description: "Reads one 3CX v20 phone system: system health and licence, " +
			"extensions and whether each is registered, one extension in full " +
			"with its forwarding and desk-phone keys, provisioned handsets, " +
			"trunks and the DID numbers on them, where each number rings, " +
			"outbound dialling rules, ring groups, queues, digital " +
			"receptionists, a department's office hours and holidays, call " +
			"records, calls in progress and the event log.\n\n" +
			"Read-only. It will not create, change or delete anything, and " +
			"every request is checked against a list of read endpoints rather " +
			"than merely being a method nothing writes with. Credentials the " +
			"API would hand out -- SIP passwords, voicemail PINs, provisioning " +
			"links, the licence key -- are never requested, so they cannot " +
			"appear in an answer. Start with get_system_status for any " +
			"\"the phones are down\" question; most of them are a trunk or a " +
			"handset that is not registered.",
	}
}

// Register implements plugins.Plugin.
//
// Sixteen read tools in six groups, split by the question a technician is
// asking rather than by the entity 3CX keeps the answer on.
func (p *Plugin) Register(_ context.Context, r *plugins.Registry) error {
	p.registerSystemTools(r)
	p.registerExtensionTools(r)
	p.registerRoutingTools(r)
	p.registerGroupTools(r)
	p.registerScheduleTools(r)
	p.registerHistoryTools(r)
	return nil
}

// Start implements plugins.Starter.
//
// The probe signs in, reads the system status and lists one extension. The
// three fail differently -- a wrong address, a wrong password and an extension
// without the system owner role -- and told apart at startup they are three
// different sentences on the dashboard rather than one confusing failure inside
// the first tool call an assistant makes.
func (p *Plugin) Start(ctx context.Context) error {
	if !p.configured {
		// Not an error the host should die on. The plugin is mounted, its
		// settings form is on the Plugins page, and Check says what is missing
		// -- which is the whole path someone follows to fix it.
		p.deps.Log.InfoContext(ctx, "3cx is not configured yet; add its address, "+
			"a system owner extension and its password on the Plugins page")
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
	p.deps.Log.InfoContext(ctx, "3cx ready",
		"reading", p.client.Describe(),
		"fqdn", info.FQDN, "version", info.Version,
		"extensions", info.ExtensionsTotal, "trunks", info.TrunksTotal)
	return nil
}

// Check implements plugins.Checker.
//
// It reports what the last real call found rather than making one of its own.
// A health check that polls the PBX on a schedule spends a phone system's
// capacity to answer a question the next tool call answers for free, and
// readiness that depends on someone else's uptime reports mcpd as broken when
// it is not.
func (p *Plugin) Check(_ context.Context) plugins.Health {
	p.mu.RLock()
	err, checked := p.lastErr, p.checked
	p.mu.RUnlock()

	switch {
	case !p.configured:
		return plugins.Degraded("not configured yet — set the address, a system " +
			"owner extension and its password below")
	case checked.IsZero():
		return plugins.Degraded("has not reached the phone system yet")
	case err != nil:
		return plugins.Degraded("last call to the phone system failed: " +
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
		return fmt.Errorf("3cx: not configured yet — set the address, a system " +
			"owner extension and its password on the Plugins page")
	}
	return nil
}

// note records the outcome of a call for the health report.
func (p *Plugin) note(err error) {
	p.mu.Lock()
	p.lastErr, p.checked = err, p.deps.Now()
	p.mu.Unlock()
}

// call wraps a tool body so every outcome reaches the health report.
func (p *Plugin) call(err error) error {
	p.note(err)
	return err
}

// limitOf resolves a caller's requested ceiling against the instance's.
//
// A caller asking for more than the operator allows gets the operator's number
// rather than an error: the request is reasonable, the answer is simply
// bounded, and the result says so.
func (p *Plugin) limitOf(requested int) int {
	if requested <= 0 || requested > p.cfg.MaxItems {
		return p.cfg.MaxItems
	}
	return requested
}
