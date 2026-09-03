package threecx

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spoked/mcpd/internal/plugins"
)

// Plugin is the 3CX integration: one instance, many customers' phone systems.
type Plugin struct {
	deps plugins.Deps
	cfg  Config

	// accounts are the customers, in the order they were configured. Each has
	// its own client, its own token and its own health.
	accounts []*account

	// configured reports whether at least one complete customer was supplied.
	// A plugin without one still mounts, so its settings form has somewhere to
	// live.
	configured bool
}

// account is one customer and the client that reaches their phone system.
type account struct {
	name    string
	aliases []string
	host    string
	client  *Client

	mu      sync.RWMutex
	lastErr error
	checked time.Time

	// bundle is the most recent support bundle capture for this customer,
	// running or finished. One at a time: the PBX builds the zip on request,
	// and two at once would be two walks of the same logs.
	bundleMu sync.Mutex
	bundle   *bundleJob
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
	instance := deps.Instance
	observe := func(outcome string, d time.Duration) {}
	if deps.Upstream != nil {
		observe = func(outcome string, d time.Duration) {
			deps.Upstream.UpstreamRequest(instance, outcome, d)
		}
	}

	p := &Plugin{deps: deps, configured: configured}
	for _, cu := range cfg.Customers {
		if !cu.complete() {
			continue
		}
		p.accounts = append(p.accounts, &account{
			name:    strings.TrimSpace(cu.Name),
			aliases: cu.names()[1:],
			host:    rootOf(cu.Host),
			client: NewClient(httpClient, cfg, cu.Host, cu.Extension, cu.Password,
				deps.Log.With("customer", strings.TrimSpace(cu.Name)), now, observe),
		})
	}

	// The credentials are not kept on the config the plugin holds, so a dump
	// of it -- a log line, an error, the settings page -- cannot carry one.
	// They live on each account's client and nowhere else.
	for i := range cfg.Customers {
		cfg.Customers[i].Password = ""
	}
	p.cfg = cfg
	return p, nil
}

// Descriptor implements plugins.Plugin.
func (p *Plugin) Descriptor() plugins.Descriptor {
	return plugins.Descriptor{
		Name:    "threecx",
		Version: "0.2.0",
		Title:   "3CX",
		Description: "Answers questions about your customers' phone systems: whether " +
			"the phones are working, who is registered, where a number rings, and " +
			"what happened to a call. Name the customer you mean; list_customers " +
			"has them. Nothing here changes anything.",
	}
}

// Register implements plugins.Plugin.
//
// Twenty-one read tools in nine groups, split by the question a technician is
// asking rather than by the entity 3CX keeps the answer on.
func (p *Plugin) Register(_ context.Context, r *plugins.Registry) error {
	p.registerCustomerTools(r)
	p.registerSystemTools(r)
	p.registerExtensionTools(r)
	p.registerRoutingTools(r)
	p.registerGroupTools(r)
	p.registerScheduleTools(r)
	p.registerHistoryTools(r)
	p.registerAccessTools(r)
	p.registerBundleTools(r)
	return nil
}

// Start implements plugins.Starter.
//
// It reaches no phone system. A customer's PBX is signed in to when somebody
// asks about that customer, and not before: a start that probed thirty
// customers cost thirty sign-ins per restart, each counted by 3CX's
// anti-hacking protection, and marked the plugin degraded over a customer
// nobody had asked about. list_customers with check set does the probing when
// somebody actually wants it.
func (p *Plugin) Start(ctx context.Context) error {
	if !p.configured {
		// Not an error the host should die on. The plugin is mounted, its
		// settings form is on the Plugins page, and Check says what is missing
		// -- which is the whole path someone follows to fix it.
		p.deps.Log.InfoContext(ctx, "3cx is not configured yet; add a customer with its "+
			"address, a system owner extension and its password on the Plugins page")
		return nil
	}
	p.deps.Log.InfoContext(ctx, "3cx ready", "customers", len(p.accounts),
		"reading", "each customer's configuration API on first use, restricted to a "+
			"named list of read endpoints by its transport")
	return nil
}

// Check implements plugins.Checker.
//
// It reports what the last real call to each customer found rather than making
// one of its own. A customer nobody has asked about yet is not a problem, so
// it is not reported as one; a customer whose last call failed is named, so
// the dashboard says which of thirty phone systems is the one to look at.
func (p *Plugin) Check(_ context.Context) plugins.Health {
	if !p.configured {
		return plugins.Degraded("not configured yet — add a customer below with its " +
			"address, a system owner extension and its password")
	}
	var failing []string
	for _, a := range p.accounts {
		a.mu.RLock()
		err := a.lastErr
		a.mu.RUnlock()
		if err != nil {
			failing = append(failing, a.name+": "+plugins.Explain(err).Error())
		}
	}
	if len(failing) > 0 {
		return plugins.Degraded("last call failed for " + strings.Join(failing, "; "))
	}
	return plugins.Healthy()
}

// note records the outcome of a call for the health report.
func (a *account) note(err error) {
	a.mu.Lock()
	a.lastErr, a.checked = err, time.Now()
	a.mu.Unlock()
}

// call wraps a tool body so every outcome reaches the health report.
func (a *account) call(err error) error {
	a.note(err)
	return err
}

// resolve finds the customer a call is about.
//
// The rule is that this never guesses. An exact match on the name or an alias
// wins, folding case. Failing that, a name that is contained in exactly one
// customer's name or alias is taken -- "acme" for "Acme Dental Group" -- but a
// name contained in two is refused with both, because picking the first of two
// customers is how a technician reads one business's phone system while
// believing they are reading another's. With one customer configured no name
// is needed; with several, none given is refused with the list.
func (p *Plugin) resolve(asked string) (*account, error) {
	asked = strings.TrimSpace(asked)
	if len(p.accounts) == 0 {
		return nil, fmt.Errorf("3cx: not configured yet — add a customer with its address, " +
			"a system owner extension and its password on the Plugins page")
	}
	if asked == "" {
		if len(p.accounts) == 1 {
			return p.accounts[0], nil
		}
		return nil, fmt.Errorf("this instance serves %d customers, so say which one with "+
			"customer: %s. list_customers has each one's aliases", len(p.accounts),
			strings.Join(p.customerNames(), ", "))
	}

	folded := strings.ToLower(asked)
	var exact, partial []*account
	for _, a := range p.accounts {
		matched, contained := false, false
		for _, n := range append([]string{a.name}, a.aliases...) {
			fn := strings.ToLower(n)
			if fn == folded {
				matched = true
			} else if strings.Contains(fn, folded) {
				contained = true
			}
		}
		switch {
		case matched:
			exact = append(exact, a)
		case contained:
			partial = append(partial, a)
		}
	}
	switch {
	case len(exact) == 1:
		return exact[0], nil
	case len(exact) > 1:
		return nil, ambiguous(asked, exact)
	case len(partial) == 1:
		return partial[0], nil
	case len(partial) > 1:
		return nil, ambiguous(asked, partial)
	}
	return nil, fmt.Errorf("no customer here is called %q. This instance serves: %s. "+
		"Ask the person which they mean if none of those is obviously it",
		asked, strings.Join(p.customerNames(), ", "))
}

// ambiguous is the refusal for a name that fits more than one customer. It
// names them all and says what to do, because the model reading it is about to
// act on it.
func ambiguous(asked string, matches []*account) error {
	names := make([]string, 0, len(matches))
	for _, a := range matches {
		names = append(names, a.name)
	}
	sort.Strings(names)
	return fmt.Errorf("%q is ambiguous: it matches %s. Do not pick one -- ask the "+
		"person which customer they mean, then call again with that exact name",
		asked, strings.Join(names, " and "))
}

func (p *Plugin) customerNames() []string {
	out := make([]string, 0, len(p.accounts))
	for _, a := range p.accounts {
		out = append(out, a.name)
	}
	return out
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
