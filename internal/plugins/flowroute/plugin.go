package flowroute

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spoked/mcpd/internal/plugins"
)

// Plugin is the Flowroute integration: one instance, many customers' accounts.
type Plugin struct {
	deps plugins.Deps
	cfg  Config

	// accounts are the customers, in the order they were configured. Each has
	// its own client, its own credential and its own health.
	accounts []*account

	// configured reports whether at least one complete customer was supplied.
	// A plugin without one still mounts, so its settings form has somewhere to
	// live.
	configured bool
}

// account is one customer and the client that reaches their Flowroute account.
type account struct {
	name    string
	aliases []string
	client  *Client

	mu      sync.RWMutex
	lastErr error
	checked time.Time
}

// note records the outcome of a call against this customer.
//
// An absent resource is not a failure. "There is no port order 41351" is a
// successful round trip that proves the address, the TLS and the credential;
// recording it as the last error would show a customer as degraded because
// somebody asked about a number that had been released.
func (a *account) note(err error, now time.Time) {
	if isNotFound(err) {
		err = nil
	}
	a.mu.Lock()
	a.lastErr, a.checked = err, now
	a.mu.Unlock()
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
	for _, cu := range cfg.Customers {
		if !cu.complete() {
			continue
		}
		name := strings.TrimSpace(cu.Name)
		client, err := NewClient(httpClient, cfg, strings.TrimSpace(cu.AccessKey),
			cu.SecretKey, deps.Log.With("customer", name), now, observe)
		if err != nil {
			return nil, err
		}
		p.accounts = append(p.accounts, &account{
			name:    name,
			aliases: cu.names()[1:],
			client:  client,
		})
	}

	// The credentials are not kept on the config the plugin holds, so a dump
	// of it -- a log line, an error, the settings page -- cannot carry one.
	// They live on each account's client and nowhere else.
	for i := range cfg.Customers {
		cfg.Customers[i].AccessKey = ""
		cfg.Customers[i].SecretKey = ""
	}
	p.cfg = cfg
	return p, nil
}

// Descriptor implements plugins.Plugin.
func (p *Plugin) Descriptor() plugins.Descriptor {
	return plugins.Descriptor{
		Name:    p.deps.Instance,
		Version: "0.1.0",
		Title:   "Flowroute",
		Description: "Your customers' Flowroute accounts: which numbers they " +
			"hold, where each is routed, its emergency address and caller-ID " +
			"name, and the port orders bringing numbers in. Name the customer " +
			"you mean; list_customers has them. Nothing here changes anything.",
	}
}

// Register implements plugins.Plugin.
//
// Eleven read tools, grouped by the question somebody is asking rather than by
// the entity Flowroute keeps the answer on.
func (p *Plugin) Register(_ context.Context, r *plugins.Registry) error {
	p.registerCustomerTools(r)
	p.registerNumberTools(r)
	p.registerRouteTools(r)
	p.registerE911Tools(r)
	p.registerCNAMTools(r)
	p.registerPortingTools(r)
	p.registerCDRTools(r)
	return nil
}

// Start implements plugins.Starter.
//
// One read of one number per customer, together rather than in turn: the
// cheapest call that proves each credential. Flowroute has no token exchange
// and no whoami, so there is no way to authenticate without touching the
// account -- which is worth knowing rather than working around, and is why
// this asks each for a single row.
//
// A customer that will not answer does not stop the others mounting. The
// instance comes up degraded and says which one, because thirty customers
// held down by one wrong key is a worse failure than a named unhealthy row.
func (p *Plugin) Start(ctx context.Context) error {
	if !p.configured {
		// Not an error the host should die on. The plugin is mounted, its
		// settings form is on the Plugins page, and Check says what is
		// missing -- which is the whole path somebody follows to fix it.
		p.deps.Log.Info("flowroute is not configured yet; add a customer with " +
			"its access key and secret key on the Plugins page")
		return nil
	}

	var wg sync.WaitGroup
	for _, a := range p.accounts {
		wg.Add(1)
		go func(a *account) {
			defer wg.Done()
			actx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
			defer cancel()
			a.note(a.client.Probe(actx), p.deps.Now())
		}(a)
	}
	wg.Wait()

	reached := 0
	for _, a := range p.accounts {
		a.mu.RLock()
		ok := a.lastErr == nil
		a.mu.RUnlock()
		if ok {
			reached++
		}
	}
	p.deps.Log.Info("flowroute ready",
		"customers", len(p.accounts), "reached", reached)
	return nil
}

// Check implements plugins.Checker.
//
// It reports what the last real call found rather than making one of its own.
// A health check that polls upstream on a schedule spends every customer's
// rate budget to answer a question nobody asked.
func (p *Plugin) Check(_ context.Context) plugins.Health {
	if !p.configured {
		return plugins.Degraded("not configured yet — add a customer with its " +
			"Flowroute access key and secret key")
	}

	var unreached, failing []string
	for _, a := range p.accounts {
		a.mu.RLock()
		err, checked := a.lastErr, a.checked
		a.mu.RUnlock()
		switch {
		case checked.IsZero():
			unreached = append(unreached, a.name)
		case err != nil:
			// Explained rather than passed through: the failure an operator
			// meets first is a rotated or mistyped key, and the stock wording
			// for that is a bare 401.
			failing = append(failing, fmt.Sprintf("%s (%s)", a.name,
				plugins.Explain(err).Error()))
		}
	}
	sort.Strings(failing)
	switch {
	case len(failing) > 0:
		return plugins.Degraded("could not read " + strings.Join(failing, "; "))
	case len(unreached) == len(p.accounts):
		return plugins.Degraded("has not reached Flowroute yet")
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
		return fmt.Errorf("flowroute: not configured yet — add a customer with its " +
			"access key and secret key on the Plugins page")
	}
	return nil
}

// resolve decides which customer a call is about.
//
// It never guesses. A name that fits two customers is refused with both named,
// because settling for one would answer confidently about the wrong business's
// numbers.
func (p *Plugin) resolve(asked string) (*account, error) {
	asked = strings.TrimSpace(asked)
	if len(p.accounts) == 0 {
		return nil, fmt.Errorf("this Flowroute plugin (%s) has no customers yet. "+
			"Somebody has to add one on the mcpd Plugins page, under %s, Customers -- "+
			"with that account's API access key and secret key -- before anything "+
			"here can be read", p.instance(), p.instance())
	}
	if asked == "" {
		if len(p.accounts) == 1 {
			return p.accounts[0], nil
		}
		return nil, fmt.Errorf("this instance serves %d customers, so say which one "+
			"with customer: %s. list_customers has each one's aliases",
			len(p.accounts), p.knownCustomers())
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
	// Nothing here can be read for a business mcpd has never been given, so
	// the answer has to be where that is fixed. It names the instance because
	// a deployment may have several, and it says not to guess: settling for
	// the nearest of the configured customers would answer confidently about
	// somebody else's numbers.
	return nil, fmt.Errorf("no customer here is called %q. This instance (%s) serves "+
		"%s. If %s should be here, somebody has to add it on the mcpd Plugins page, "+
		"under %s, Customers -- with that Flowroute account's access key and secret "+
		"key. Tell the person that rather than reading one of the others",
		asked, p.instance(), p.knownCustomers(), asked, p.instance())
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

// namesInAMessage bounds how many customers an error spells out. A deployment
// with sixty of them would otherwise put all sixty in front of a model on
// every mistyped name, which costs more context than the answer is worth and
// buries the sentence saying what to do.
const namesInAMessage = 10

// knownCustomers renders the configured customers for a message, bounded.
func (p *Plugin) knownCustomers() string {
	names := make([]string, 0, len(p.accounts))
	for _, a := range p.accounts {
		names = append(names, a.name)
	}
	if len(names) <= namesInAMessage {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more (list_customers has them all)",
		strings.Join(names[:namesInAMessage], ", "), len(names)-namesInAMessage)
}

// instance is what this plugin is configured under, which is what somebody
// looks for on the Plugins page. It is the host's name for it rather than the
// integration's, because a deployment may serve several.
func (p *Plugin) instance() string {
	if name := strings.TrimSpace(p.deps.Instance); name != "" {
		return name
	}
	return "flowroute"
}

// customerFor is the first two lines of every tool: refuse an unconfigured
// instance with a message somebody can act on, then resolve the customer
// without guessing.
func (p *Plugin) customerFor(customer string) (*account, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	return p.resolve(customer)
}
