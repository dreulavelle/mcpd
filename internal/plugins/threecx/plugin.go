package threecx

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/spoked/mcpd/internal/plugins"
)

// Plugin is the 3CX integration: one instance, many customers' phone systems.
type Plugin struct {
	deps plugins.Deps
	cfg  Config

	// accounts are the phone systems, in the order they were configured. Each
	// has its own client, its own token and its own health.
	accounts []*account

	// customers are the businesses those systems belong to, in the order they
	// first appear. One business owns one or more accounts; a business with
	// one is the case every row was before the Customer column existed, and it
	// resolves exactly as it did.
	customers []*customer

	// bundleCeiling is the largest support bundle that will be spooled. A
	// field rather than the constant at the call site so a test can lower it
	// without writing to package state a running collection reads.
	bundleCeiling int64

	// configured reports whether at least one complete customer was supplied.
	// A plugin without one still mounts, so its settings form has somewhere to
	// live.
	configured bool
}

// customer is one business and the phone systems it owns.
type customer struct {
	// id is the name with its spaces turned to hyphens: what an answer hands
	// back and what a caller can repeat without having to spell prose.
	id   string
	name string
	// systems are its phone systems in table order. Never empty: a business
	// exists because a row named it.
	systems []*account
}

// account is one phone system and the client that reaches it.
type account struct {
	// id is the identifier a caller passes as `system`. Derived from the name
	// rather than stored, because the row's identity *is* its name; see
	// identifier.
	id string
	// name is this phone system's name, which for a business with one system
	// is the business's name.
	name    string
	aliases []string
	// owner is the business this system belongs to. Always set.
	owner  *customer
	host   string
	client *Client

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
	if httpClient != nil {
		// A copy, because the host's client is shared and this plugin's
		// timeout is its own business.
		clone := *httpClient
		clone.Timeout = cfg.Timeout()
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

	p := &Plugin{deps: deps, configured: configured, bundleCeiling: maxBundle}

	// Only the rows that can be signed in to, and grouped after that filter
	// rather than before it: a business whose every row is half filled in has
	// no phone system to reach, and listing it as a customer with no systems
	// would have a model ask about something nothing here can answer.
	usable := make([]System, 0, len(cfg.Systems))
	for _, s := range cfg.Systems {
		if s.complete() {
			usable = append(usable, s)
		}
	}
	for _, b := range groupSystems(usable) {
		cust := &customer{id: b.id, name: b.name}
		for _, row := range b.rows {
			s := usable[row]
			name := strings.TrimSpace(s.Name)
			acct := &account{
				id:      identifier(name),
				name:    name,
				aliases: s.names()[1:],
				owner:   cust,
				host:    rootOf(s.Host),
				// Both names on the logger: with several systems under one
				// business, "customer=Acme" alone does not say which of them a
				// line is about.
				client: NewClient(httpClient, cfg, s.Host, s.Extension, s.Password,
					deps.Log.With("customer", cust.name, "system", name), now, observe),
			}
			cust.systems = append(cust.systems, acct)
			p.accounts = append(p.accounts, acct)
		}
		p.customers = append(p.customers, cust)
	}

	// The credentials are not kept on the config the plugin holds, so a dump
	// of it -- a log line, an error, the settings page -- cannot carry one.
	// They live on each account's client and nowhere else.
	for i := range cfg.Systems {
		cfg.Systems[i].Password = ""
	}
	p.cfg = cfg
	return p, nil
}

// Descriptor implements plugins.Plugin.
func (p *Plugin) Descriptor() plugins.Descriptor {
	return plugins.Descriptor{
		Name:    "threecx",
		Version: "0.3.0",
		Title:   "3CX",
		Description: "Answers questions about your customers' phone systems: whether " +
			"the phones are working, who is registered, where a number rings, and " +
			"what happened to a call. Name the customer you mean; list_customers " +
			"has them, and says which of them run more than one phone system -- " +
			"those need system set as well. Nothing here changes anything.",
	}
}

// Register implements plugins.Plugin.
//
// Twenty-four read tools in ten groups, split by the question a technician is
// asking rather than by the entity 3CX keeps the answer on.
func (p *Plugin) Register(_ context.Context, r *plugins.Registry) error {
	p.registerCustomerTools(r)
	p.registerSystemTools(r)
	p.registerExtensionTools(r)
	p.registerRoutingTools(r)
	p.registerGroupTools(r)
	p.registerAudioTools(r)
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
	p.deps.Log.InfoContext(ctx, "3cx ready", "customers", len(p.customers),
		"systems", len(p.accounts),
		"reading", "each phone system's configuration API on first use, restricted to a "+
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
			// The business as well as the system when they differ: "Branch is
			// down" does not say whose branch, and the person reading the
			// dashboard is looking for a customer to ring.
			where := a.name
			if a.owner.name != a.name {
				where = a.owner.name + " / " + a.name
			}
			failing = append(failing, where+": "+plugins.Explain(err).Error())
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

// normaliseName is the form two names are compared in: folded to lower case,
// trimmed, with runs of whitespace collapsed to one space and the punctuation
// a sentence leaves on a name taken off the ends. Any punctuation rather than
// a list of the quote marks somebody thought of: the German and CJK forms are
// what a name comes back wrapped in when it was not written in English.
//
// An assistant repeating a name back does not always repeat it exactly: it
// arrives quoted, with a trailing full stop, or with the double space somebody
// typed into the settings form. None of those is a different customer, and
// refusing them sends the model round again with the long form -- which is the
// behaviour #157 reported. Nothing here loosens what counts as a match beyond
// that: two different names still do not meet, the trim is ends-only so
// "A.C.M.E" is not "ACME", and a configuration where two customers normalise
// alike is refused by Validate rather than resolved by guessing.
func normaliseName(s string) string {
	s = strings.TrimFunc(strings.ToLower(s), func(r rune) bool {
		return unicode.IsPunct(r) || unicode.IsSpace(r)
	})
	return strings.Join(strings.Fields(s), " ")
}

// scope is what a caller's `customer` argument settled: a business, or one
// phone system named directly.
//
// Both, because the argument has always accepted either. A table where every
// business has one phone system cannot tell them apart, and the names people
// already have in their prompts -- the ones list_customers reported before
// this field existed -- are the systems' names.
type scope struct {
	cust *customer
	acct *account
}

// resolve finds the phone system a call is about.
//
// The rule is that this never guesses, and it now has two arguments to not
// guess with. `customer` names the business, or one system outright; `system`
// picks between a business's systems. What has not changed is the answer for
// the table that has one phone system per business: the customer argument
// alone settles it, exactly as it did, and `system` is never needed.
//
// Matching, at both levels: an exact match on a name, an alias or an
// identifier wins, folding case, collapsing whitespace and ignoring the
// punctuation a sentence leaves on a name. Failing that, a word contained in
// exactly one candidate is taken -- "acme" for "Acme Dental Group" -- and one
// contained in two is refused with both, because picking the first of two is
// how a technician reads one phone system while believing they are reading
// another.
func (p *Plugin) resolve(asked, system string) (*account, error) {
	if len(p.accounts) == 0 {
		return nil, fmt.Errorf("this 3CX plugin (%s) has no customers yet. Somebody has to "+
			"add one on the mcpd Plugins page, under %s, Customers -- with the phone "+
			"system's address, a system owner extension and its password -- before "+
			"anything here can be read", p.instance(), p.instance())
	}
	sc, err := p.scopeOf(asked)
	if err != nil {
		return nil, err
	}
	// Normalised rather than trimmed, for the same reason the customer
	// argument is: a system of "." names no more of a phone system than an
	// empty one does, and a fragment match on nothing matches everything.
	if normaliseName(system) != "" {
		return p.systemIn(sc, system)
	}
	switch {
	case sc.acct != nil:
		return sc.acct, nil
	case sc.cust != nil:
		if len(sc.cust.systems) == 1 {
			return sc.cust.systems[0], nil
		}
		return nil, needSystem(sc.cust)
	case len(p.accounts) == 1:
		return p.accounts[0], nil
	case len(p.customers) == 1:
		// One business with several phone systems. Naming it would add
		// nothing the caller does not already know, so the answer is the same
		// one they would get for naming it.
		return nil, needSystem(p.customers[0])
	}
	return nil, fmt.Errorf("this instance serves %d customers, so say which one with "+
		"customer: %s. list_customers has each one's aliases", len(p.customers), p.knownCustomers())
}

// scopeOf reads the `customer` argument. An empty one is not an error here;
// what to do about it depends on what else was given.
func (p *Plugin) scopeOf(asked string) (scope, error) {
	// The normalised form decides whether a name was given at all: an argument
	// of "." carries no more of a name than an empty one does, and the two
	// should not end in different answers.
	folded := normaliseName(strings.TrimSpace(asked))
	if folded == "" {
		return scope{}, nil
	}
	for _, exact := range []bool{true, false} {
		var custs []*customer
		var accts []*account
		for _, c := range p.customers {
			if nameMatches(folded, exact, c.name, c.id) {
				custs = append(custs, c)
			}
		}
		for _, a := range p.accounts {
			if nameMatches(folded, exact, append([]string{a.name, a.id}, a.aliases...)...) {
				accts = append(accts, a)
			}
		}
		switch {
		case len(custs) == 1 && len(accts) == 0:
			return scope{cust: custs[0]}, nil
		case len(custs) == 0 && len(accts) == 1:
			return scope{acct: accts[0]}, nil
		case len(custs) == 0 && len(accts) == 0:
			continue
		}
		// More than one candidate. They are not necessarily more than one
		// answer: a business with a single phone system and that system are
		// the same thing under two headings, which is what every row of an
		// ordinary table is.
		if only := onlyAccount(custs, accts); only != nil {
			return scope{acct: only}, nil
		}
		return scope{}, ambiguous(asked, custs, accts)
	}
	// Nothing here can be read for a business mcpd has never been given, so
	// the answer has to be where that is fixed. It names the instance because
	// a deployment may have several, and it says not to guess: settling for
	// the nearest of the configured customers would answer confidently about
	// somebody else's phone system.
	return scope{}, fmt.Errorf("no customer here is called %q. This instance (%s) serves %s. "+
		"Any of those names works. If %s should be "+
		"here, somebody has to add it on the mcpd Plugins page, under %s, Customers "+
		"-- with the phone system's address and a system owner extension. Tell the "+
		"person that rather than reading one of the others",
		asked, p.instance(), p.knownCustomers(), asked, p.instance())
}

// systemIn settles the `system` argument within whatever `customer` left.
func (p *Plugin) systemIn(sc scope, asked string) (*account, error) {
	folded := normaliseName(strings.TrimSpace(asked))
	within := p.accounts
	switch {
	case sc.acct != nil:
		within = sc.acct.owner.systems
	case sc.cust != nil:
		within = sc.cust.systems
	}
	var found []*account
	for _, exact := range []bool{true, false} {
		for _, a := range within {
			if nameMatches(folded, exact, append([]string{a.name, a.id}, a.aliases...)...) {
				found = append(found, a)
			}
		}
		if len(found) > 0 {
			break
		}
	}
	switch {
	case len(found) > 1:
		return nil, ambiguous(asked, nil, found)
	case len(found) == 0:
		// Named a real phone system, but not one of this customer's. Saying so
		// is the difference between an operator fixing a mistyped customer and
		// one concluding the system is missing.
		if sc.cust != nil || sc.acct != nil {
			for _, a := range p.accounts {
				if nameMatches(folded, true, append([]string{a.name, a.id}, a.aliases...)...) {
					return nil, fmt.Errorf("%s belongs to %s, not to %s. Ask for it by that "+
						"customer, or name one of %s's own: %s", a.name, a.owner.name,
						ownerOf(sc).name, ownerOf(sc).name, systemsOf(ownerOf(sc)))
				}
			}
			return nil, fmt.Errorf("%s has no phone system called %q. It has %s. Do not pick "+
				"one -- ask the person which they mean, then call again with that identifier",
				ownerOf(sc).name, asked, systemsOf(ownerOf(sc)))
		}
		return nil, fmt.Errorf("no phone system here is called %q. This instance (%s) has %s. "+
			"If it should be here, somebody has to add it on the mcpd Plugins page, under "+
			"%s, Customers. Tell the person that rather than reading one of the others",
			asked, p.instance(), p.knownSystems(), p.instance())
	}
	if sc.acct != nil && found[0] != sc.acct {
		return nil, fmt.Errorf("customer %q and system %q name two different phone systems, "+
			"%s and %s. Say which one you mean", sc.acct.name, asked, sc.acct.name, found[0].name)
	}
	return found[0], nil
}

// ownerOf is the business a scope is within, for a message. A scope reaching
// here has one.
func ownerOf(sc scope) *customer {
	if sc.cust != nil {
		return sc.cust
	}
	return sc.acct.owner
}

// onlyAccount reports the single phone system a set of candidates all denote,
// or nil if they denote more than one.
func onlyAccount(custs []*customer, accts []*account) *account {
	var only *account
	consider := func(a *account) bool {
		if only == nil {
			only = a
			return true
		}
		return only == a
	}
	for _, c := range custs {
		for _, a := range c.systems {
			if !consider(a) {
				return nil
			}
		}
	}
	for _, a := range accts {
		if !consider(a) {
			return nil
		}
	}
	return only
}

// matches reports whether a name a caller gave fits any of these names, either
// exactly or as a fragment of one.
//
// Each name is compared twice, as it normalises and as its identifier: the
// identifier is what answers hand back, so a model repeating "acme-dental"
// must reach what "Acme Dental" reaches.
func nameMatches(folded string, exact bool, names ...string) bool {
	for _, n := range names {
		for _, form := range [2]string{normaliseName(n), identifier(n)} {
			if form == "" {
				continue
			}
			if exact && form == folded {
				return true
			}
			if !exact && strings.Contains(form, folded) {
				return true
			}
		}
	}
	return false
}

// ambiguous is the refusal for a name that fits more than one thing. It names
// them all and says what to do, because the model reading it is about to act
// on it.
func ambiguous(asked string, custs []*customer, accts []*account) error {
	named := make([]string, 0, len(custs)+len(accts))
	for _, c := range custs {
		named = append(named, c.name)
	}
	sort.Strings(named)
	// Phone systems are named with their identifier and their business, since
	// the whole reason two of them collided is that their names do not tell
	// them apart on their own.
	systems := make([]string, 0, len(accts))
	for _, a := range accts {
		systems = append(systems, fmt.Sprintf("%s (%s, on %s)", a.id, a.owner.name, displayHost(a.host)))
	}
	sort.Strings(systems)
	switch {
	case len(named) == 0:
		return fmt.Errorf("%q is ambiguous: it matches the phone systems %s. Do not pick "+
			"one -- ask the person which they mean, then call again with system set to "+
			"that identifier", asked, strings.Join(systems, " and "))
	case len(systems) > 0:
		return fmt.Errorf("%q is ambiguous: it matches %s and the phone systems %s. Do not "+
			"pick one -- ask the person which they mean, then call again with that exact "+
			"name", asked, strings.Join(named, " and "), strings.Join(systems, " and "))
	}
	return fmt.Errorf("%q is ambiguous: it matches %s. Do not pick one -- ask the "+
		"person which customer they mean, then call again with that exact name",
		asked, strings.Join(named, " and "))
}

// needSystem is the refusal for a business with more than one phone system and
// a call that did not say which.
//
// It is not a failure of the request so much as a question the request left
// open, and the answer says so: the systems are listed with the identifier to
// pass back, and the model is told to ask rather than to try one. Reading the
// wrong site's extensions and reporting them as the customer's is the failure
// this exists to prevent, and it is invisible when it happens.
func needSystem(c *customer) error {
	return fmt.Errorf("%s has %d phone systems, so say which one with system: %s. "+
		"Do not pick one -- ask the person which they mean, or read each in turn and "+
		"say which is which. list_customers has them all",
		c.name, len(c.systems), systemsOf(c))
}

// systemsOf renders a business's phone systems for a message: the identifier
// to pass back, the name a person uses, and the address, which is often the
// only thing that tells two sites apart to somebody who knows the estate.
func systemsOf(c *customer) string {
	shown := make([]string, 0, len(c.systems))
	for i, a := range c.systems {
		if i == namesInAMessage {
			return fmt.Sprintf("%s; and %d more (list_customers has them all)",
				strings.Join(shown, "; "), len(c.systems)-namesInAMessage)
		}
		shown = append(shown, fmt.Sprintf("%s (%s, on %s)", a.id, a.name, displayHost(a.host)))
	}
	return strings.Join(shown, "; ")
}

// namesInAMessage bounds how many customers an error spells out. A deployment
// with sixty of them would otherwise put all sixty in front of a model on
// every mistyped name, which costs more context than the answer is worth and
// buries the sentence saying what to do.
const namesInAMessage = 10

// aliasesInAMessage bounds how many of one customer's other names are spelt
// out beside it, for the same reason.
const aliasesInAMessage = 3

// knownCustomers renders the configured businesses for a message, bounded,
// each with the aliases it also answers to or the phone systems it owns.
//
// The aliases are named because leaving them out is what sends a model back
// with the long form of a name it had a short one for: a message listing only
// "Acme Dental Group" reads as though "ADG" was never going to work. A
// business with several systems is spelt out the other way, with their
// identifiers, because those are what the next call has to carry.
func (p *Plugin) knownCustomers() string {
	shown := make([]string, 0, len(p.customers))
	for i, c := range p.customers {
		if i == namesInAMessage {
			return fmt.Sprintf("%s; and %d more (list_customers has them all)",
				strings.Join(shown, "; "), len(p.customers)-namesInAMessage)
		}
		if len(c.systems) > 1 {
			ids := make([]string, 0, len(c.systems))
			for _, a := range c.systems {
				ids = append(ids, a.id)
			}
			// The systems rather than the business, because naming the
			// business is not enough to ask it anything: "Any of these
			// works" has to be true of every name in the list.
			shown = append(shown, fmt.Sprintf("%s, which runs %d phone systems -- ask about %s",
				c.name, len(c.systems), strings.Join(ids, " or ")))
			continue
		}
		also := c.systems[0].aliases
		if len(also) == 0 {
			shown = append(shown, c.name)
			continue
		}
		// Aliases are bounded too. Ten customers with ten aliases each is a
		// hundred names, which is the cost the customer bound exists to avoid.
		if len(also) > aliasesInAMessage {
			also = also[:aliasesInAMessage]
		}
		// Semicolons between customers, commas between one customer's names:
		// with one separator doing both jobs a model cannot split the list
		// back into customers.
		shown = append(shown, fmt.Sprintf("%s, also called %s", c.name, strings.Join(also, " or ")))
	}
	return strings.Join(shown, "; ")
}

// knownSystems renders every phone system on the instance, for the refusal of
// a `system` that named none of them and no customer to narrow it to.
func (p *Plugin) knownSystems() string {
	shown := make([]string, 0, len(p.accounts))
	for i, a := range p.accounts {
		if i == namesInAMessage {
			return fmt.Sprintf("%s; and %d more (list_customers has them all)",
				strings.Join(shown, "; "), len(p.accounts)-namesInAMessage)
		}
		shown = append(shown, fmt.Sprintf("%s (%s)", a.id, a.owner.name))
	}
	return strings.Join(shown, "; ")
}

// instance is what this plugin is configured under, which is what somebody
// looks for on the Plugins page. It is the host's name for it rather than the
// integration's, because a deployment may serve several.
func (p *Plugin) instance() string {
	if name := strings.TrimSpace(p.deps.Instance); name != "" {
		return name
	}
	return "3CX"
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

// source is what every answer carries to say which phone system it came from.
func (a *account) source() Source {
	return Source{Customer: a.owner.name, System: a.name, SystemID: a.id}
}
