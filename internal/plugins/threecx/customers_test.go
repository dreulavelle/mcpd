package threecx

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/spoked/mcpd/internal/plugins"
)

// twoCustomers builds a plugin over two fake phone systems that answer with
// different versions, so a test can tell which one a call reached.
func twoCustomers(t *testing.T) (*Plugin, *fakePBX, *fakePBX) {
	t.Helper()
	acme, acmeSrv := newFakePBX(t, map[string]string{
		"SystemStatus": `{"FQDN":"acme.example","Version":"20.0.1"}`, "LicenseStatus": `{}`, "Trunks": collection(0), "Users": collection(0)})
	globex, globexSrv := newFakePBX(t, map[string]string{
		"SystemStatus": `{"FQDN":"globex.example","Version":"20.0.2"}`, "LicenseStatus": `{}`, "Trunks": collection(0), "Users": collection(0)})
	// Both fakes are httptest servers on loopback; the guard checks the
	// configured host, so each client is built over its own server's client.
	p, err := New(testDeps(), Config{Customers: []Customer{
		{Name: "Acme Dental Group", Aliases: []string{"acme", "ADG", "Acme Roof Care"}, Host: acmeSrv.URL, Extension: "100", Password: "right-password"},
		{Name: "Globex Roofing", Aliases: []string{"globex"}, Host: globexSrv.URL, Extension: "100", Password: "right-password"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	p.accounts[0].client.http = readOnly(acmeSrv.Client(), p.accounts[0].host)
	p.accounts[1].client.http = readOnly(globexSrv.Client(), p.accounts[1].host)
	return p, acme, globex
}

// A customer is found by its name or an alias, folding case; a fragment that
// fits exactly one customer is taken; and every refusal names the customers so
// the caller can ask rather than guess.
func TestResolve_NamesAliasesAndFragments(t *testing.T) {
	p, _, _ := twoCustomers(t)
	cases := map[string]string{
		"Acme Dental Group": "Acme Dental Group",
		"acme dental group": "Acme Dental Group",
		"ADG":               "Acme Dental Group",
		"adg":               "Acme Dental Group",
		"globex":            "Globex Roofing",
		"Roofing":           "Globex Roofing",
		"dental":            "Acme Dental Group",
		"  Globex Roofing ": "Globex Roofing",
	}
	for asked, want := range cases {
		a, err := p.resolve(asked)
		if err != nil {
			t.Errorf("%q: %v", asked, err)
			continue
		}
		if a.name != want {
			t.Errorf("%q resolved to %q, want %q", asked, a.name, want)
		}
	}
}

// "acme" is an alias of one customer and a fragment of that customer's other
// alias; the exact match wins. "roof" is a fragment of one customer's alias and
// of the other's name, and is refused with both rather than resolved to
// whichever came first.
func TestResolve_NeverGuesses(t *testing.T) {
	p, _, _ := twoCustomers(t)

	a, err := p.resolve("acme")
	if err != nil || a.name != "Acme Dental Group" {
		t.Errorf("an exact alias match should win over a fragment of another customer, got %v %v", a, err)
	}

	_, err = p.resolve("roof")
	if err == nil {
		t.Fatal("a fragment matching two customers must be refused")
	}
	for _, want := range []string{"ambiguous", "Acme Dental Group", "Globex Roofing", "ask the person"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should say %q, got %v", want, err)
		}
	}

	_, err = p.resolve("")
	if err == nil || !strings.Contains(err.Error(), "serves 2 customers") || !strings.Contains(err.Error(), "Globex Roofing") {
		t.Errorf("no name with two customers should be refused with the list, got %v", err)
	}

	// An unknown customer says where it would be added, names the instance
	// somebody has to open, and tells the model not to settle for a nearby
	// one -- reading the wrong business's phone system confidently is the
	// failure this wording exists to prevent.
	_, err = p.resolve("Initech")
	if err == nil {
		t.Fatal("an unknown customer must be refused")
	}
	// Each customer is named with the aliases it also answers to: a message
	// listing only the long form is what sends a model back with the long form
	// of a name it had a short one for.
	for _, want := range []string{
		`no customer here is called "Initech"`,
		"Acme Dental Group (acme, ADG, Acme Roof Care), Globex Roofing (globex)",
		"add it on the mcpd Plugins page", "Customers", "rather than reading one of the others",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should say %q, got %v", want, err)
		}
	}
	if !strings.Contains(err.Error(), "(threecx)") {
		t.Errorf("the refusal should name the instance to open, got %v", err)
	}
}

// With one customer no name is needed and any name that fits it works; a name
// that does not is still refused rather than falling back to the only one.
func TestResolve_SingleCustomer(t *testing.T) {
	p, _ := toolPlugin(t, map[string]string{})
	if a, err := p.resolve(""); err != nil || a.name != "Acme" {
		t.Errorf("one customer needs no name, got %v %v", a, err)
	}
	if a, err := p.resolve("ACME"); err != nil || a.name != "Acme" {
		t.Errorf("the one customer by name, got %v %v", a, err)
	}
	if _, err := p.resolve("Globex"); err == nil {
		t.Error("a name that fits nobody must not fall back to the only customer")
	}
}

// A call names a customer and reaches that customer's phone system and no
// other; the health of each is kept apart.
func TestTools_ReachTheNamedCustomer(t *testing.T) {
	p, acme, globex := twoCustomers(t)
	ctx := context.Background()

	s, err := p.getSystemStatus(ctx, statusArgs{Customer: "globex"})
	if err != nil {
		t.Fatal(err)
	}
	if s.FQDN != "globex.example" || s.Version != "20.0.2" {
		t.Errorf("asked for globex, read %+v", s)
	}
	if s.Customer != "Globex Roofing" {
		t.Errorf("every answer names the customer it is about, got %q", s.Customer)
	}
	if acme.reads.Load() != 0 {
		t.Errorf("acme's phone system should not have been touched, saw %v", acme.seen)
	}
	if globex.reads.Load() == 0 {
		t.Error("globex's phone system should have been read")
	}

	if _, err := p.getSystemStatus(ctx, statusArgs{}); err == nil {
		t.Error("two customers and no name must be refused before anything is read")
	}

	list, err := p.listCustomers(ctx, customersArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if list.Count != 2 || list.Customers[0].Name != "Acme Dental Group" || list.Customers[1].Name != "Globex Roofing" {
		t.Errorf("customers: %+v", list.Customers)
	}
	if list.Customers[0].Reachable != nil {
		t.Errorf("a customer never called reports no reachability yet, got %+v", list.Customers[0])
	}
	if list.Customers[1].Reachable == nil || !*list.Customers[1].Reachable {
		t.Errorf("a customer just read reports reachable, got %+v", list.Customers[1])
	}
	if strings.Join(list.Customers[1].Aliases, ",") != "globex" {
		t.Errorf("aliases: %v", list.Customers[1].Aliases)
	}
	// An https address is reported without its scheme; these fakes are http,
	// so they keep theirs.
	if displayHost("https://acme.ny.3cx.us/") != "acme.ny.3cx.us" ||
		displayHost("http://pbx.internal:5000") != "http://pbx.internal:5000" {
		t.Error("an https address should read as the bare host and http should not")
	}
}

// Starting reaches no phone system; a check on demand reaches every one, and
// names the customer whose sign-in fails while the others stay reachable.
func TestStart_ReachesNothingUntilAsked(t *testing.T) {
	p, acme, globex := twoCustomers(t)
	ctx := context.Background()
	if err := p.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if acme.logins.Load()+globex.logins.Load() != 0 {
		t.Error("start must not sign in to any customer")
	}
	if h := p.Check(ctx); h.State != "healthy" {
		t.Errorf("customers nobody has asked about are not a problem: %+v", h)
	}

	globex.loginOK = false
	list, err := p.listCustomers(ctx, customersArgs{Check: true})
	if err != nil {
		t.Fatal(err)
	}
	if acme.logins.Load() != 1 || globex.logins.Load() != 1 {
		t.Errorf("check should sign in to each customer once: %d %d", acme.logins.Load(), globex.logins.Load())
	}
	if r := list.Customers[0].Reachable; r == nil || !*r {
		t.Errorf("acme should be reachable: %+v", list.Customers[0])
	}
	if r := list.Customers[1].Reachable; r == nil || *r || !strings.Contains(list.Customers[1].LastError, "extension and password") {
		t.Errorf("globex should report its refused sign-in: %+v", list.Customers[1])
	}
	if h := p.Check(ctx); h.State != "degraded" || !strings.Contains(h.Message, "Globex Roofing") {
		t.Errorf("health should name the failing customer: %+v", h)
	}
}

// A long customer list is not spelled out in full on every mistyped name: the
// sentence saying what to do is the part that matters, and sixty names would
// bury it.
func TestResolve_BoundsTheNamesItLists(t *testing.T) {
	customers := make([]Customer, 0, 14)
	for i := range 14 {
		customers = append(customers, Customer{
			Name:      fmt.Sprintf("Customer %02d", i),
			Host:      fmt.Sprintf("pbx%02d.example", i),
			Extension: "100", Password: "p",
		})
	}
	p, err := New(testDeps(), Config{Customers: customers})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.resolve("nobody")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "and 4 more (list_customers has them all)") {
		t.Errorf("the list should be bounded, got %v", err)
	}
	if strings.Contains(err.Error(), "Customer 12") {
		t.Errorf("only the first ten should be spelled out, got %v", err)
	}
}

// A plugin with no customers at all says so, and says where to add one, rather
// than reporting the tool as broken.
func TestResolve_NoCustomersPointsAtThePluginsPage(t *testing.T) {
	p, err := New(testDeps(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.resolve("Acme")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	for _, want := range []string{"has no customers yet", "mcpd Plugins page", "system owner extension"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("should say %q, got %v", want, err)
		}
	}
}

// Every tool that takes a customer resolves an alias, and resolves it to the
// same phone system the canonical name reaches.
//
// This is the guarantee #157 asked for, mechanised: an alias `list_customers`
// reports has to work everywhere a customer is accepted, and one tool
// resolving customers its own way is the shape of bug that produces "it
// worked, then it did not". The table is checked against the registry below,
// so a tool added without a line here fails rather than going untested.
func TestResolve_EveryToolAcceptsAnAlias(t *testing.T) {
	calls := map[string]func(*Plugin, string) error{
		"list_extensions": func(p *Plugin, c string) error {
			_, err := p.listExtensions(context.Background(), extensionsArgs{Customer: c})
			return err
		},
		"get_extension": func(p *Plugin, c string) error {
			_, err := p.getExtension(context.Background(), extensionArgs{Customer: c, Extension: "100"})
			return err
		},
		"list_devices": func(p *Plugin, c string) error {
			_, err := p.listDevices(context.Background(), devicesArgs{Customer: c})
			return err
		},
		"list_trunks": func(p *Plugin, c string) error {
			_, err := p.listTrunks(context.Background(), trunksArgs{Customer: c})
			return err
		},
		"list_inbound_rules": func(p *Plugin, c string) error {
			_, err := p.listInboundRules(context.Background(), inboundRulesArgs{Customer: c})
			return err
		},
		"list_outbound_rules": func(p *Plugin, c string) error {
			_, err := p.listOutboundRules(context.Background(), outboundRulesArgs{Customer: c})
			return err
		},
		"search_directory": func(p *Plugin, c string) error {
			_, err := p.searchDirectory(context.Background(), directoryArgs{Customer: c})
			return err
		},
		"list_ring_groups": func(p *Plugin, c string) error {
			_, err := p.listRingGroups(context.Background(), ringGroupsArgs{Customer: c})
			return err
		},
		"list_queues": func(p *Plugin, c string) error {
			_, err := p.listQueues(context.Background(), queuesArgs{Customer: c})
			return err
		},
		"list_receptionists": func(p *Plugin, c string) error {
			_, err := p.listReceptionists(context.Background(), receptionistsArgs{Customer: c})
			return err
		},
		"get_schedule": func(p *Plugin, c string) error {
			_, err := p.getSchedule(context.Background(), scheduleArgs{Customer: c})
			return err
		},
		"get_system_status": func(p *Plugin, c string) error {
			_, err := p.getSystemStatus(context.Background(), statusArgs{Customer: c})
			return err
		},
		"list_services": func(p *Plugin, c string) error {
			_, err := p.listServices(context.Background(), servicesArgs{Customer: c})
			return err
		},
		"list_active_calls": func(p *Plugin, c string) error {
			_, err := p.listActiveCalls(context.Background(), activeCallsArgs{Customer: c})
			return err
		},
		"search_events": func(p *Plugin, c string) error {
			_, err := p.searchEvents(context.Background(), eventsArgs{Customer: c})
			return err
		},
		"list_blocked": func(p *Plugin, c string) error {
			_, err := p.listBlocked(context.Background(), blockedArgs{Customer: c})
			return err
		},
		"list_sbcs": func(p *Plugin, c string) error {
			_, err := p.listSBCs(context.Background(), sbcArgs{Customer: c})
			return err
		},
		"search_call_history": func(p *Plugin, c string) error {
			_, err := p.searchCallHistory(context.Background(), callHistoryArgs{Customer: c})
			return err
		},
		"get_support_bundle_report": func(p *Plugin, c string) error {
			_, err := p.bundleReport(context.Background(), bundleReportArgs{Customer: c})
			return err
		},
	}

	// Registered but not called here: list_customers takes no customer, and
	// aggregate_support_bundle asks the phone system to build its support
	// bundle, which is not a thing to start twenty times in a unit test. Its
	// resolution is covered in bundle_test.go.
	exempt := map[string]bool{"list_customers": true, "aggregate_support_bundle": true}

	m := plugins.NewManager(slog.New(slog.NewTextHandler(io.Discard, nil)), "test", nil, nil, nil, nil)
	p, acme, globex := twoOpenCustomers(t)
	if err := m.Register(context.Background(), p, "threecx", false); err != nil {
		t.Fatalf("registering the plugin: %v", err)
	}
	mounted := m.Lookup("threecx")
	if mounted == nil {
		t.Fatal("the plugin did not mount")
	}
	for _, name := range mounted.Registry.ToolNames() {
		bare := strings.TrimPrefix(name, "threecx_")
		if _, covered := calls[bare]; !covered && !exempt[bare] {
			t.Errorf("%s takes a customer and is not in this table; every tool that "+
				"accepts one has to resolve an alias the same way", bare)
		}
	}

	// Both spellings of the same customer, and one of another, so a tool that
	// resolved differently would reach the wrong phone system rather than
	// merely failing.
	for _, asked := range []string{"Acme Dental Group", "ADG", "adg", " acme ", "dental"} {
		for name, call := range calls {
			acme.reads.Store(0)
			globex.reads.Store(0)
			err := call(p, asked)
			if err != nil && isResolution(err) {
				t.Errorf("%s with customer %q: %v", name, asked, err)
				continue
			}
			if globex.reads.Load() != 0 {
				t.Errorf("%s with customer %q reached the wrong customer's phone system", name, asked)
			}
			if acme.reads.Load() == 0 && err != nil {
				t.Errorf("%s with customer %q reached no phone system: %v", name, asked, err)
			}
		}
	}
}

// isResolution reports whether an error is the resolver refusing the customer,
// rather than the phone system answering badly.
func isResolution(err error) bool {
	text := err.Error()
	return strings.Contains(text, "no customer here is called") ||
		strings.Contains(text, "is ambiguous") ||
		strings.Contains(text, "say which one with customer")
}

// twoOpenCustomers is twoCustomers with both phone systems answering anything
// asked of them, so a test can be about which one a call reached. The rate
// limit is lifted: this one makes a hundred calls, and five a second would
// make it a twenty-second test about nothing it is testing.
func twoOpenCustomers(t *testing.T) (*Plugin, *fakePBX, *fakePBX) {
	t.Helper()
	acme, acmeSrv := newFakePBX(t, map[string]string{})
	globex, globexSrv := newFakePBX(t, map[string]string{})
	acme.anything, globex.anything = true, true
	p, err := New(testDeps(), Config{
		RequestsPerSecond: 1000,
		Customers: []Customer{
			{Name: "Acme Dental Group", Aliases: []string{"acme", "ADG"}, Host: acmeSrv.URL, Extension: "100", Password: "right-password"},
			{Name: "Globex Roofing", Aliases: []string{"globex"}, Host: globexSrv.URL, Extension: "100", Password: "right-password"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	p.accounts[0].client.http = readOnly(acmeSrv.Client(), p.accounts[0].host)
	p.accounts[1].client.http = readOnly(globexSrv.Client(), p.accounts[1].host)
	return p, acme, globex
}

// A name an assistant repeats back is not always spelt the way it was stored:
// it arrives quoted, with a full stop on the end, or with the double space
// somebody typed into the form. None of those is a different customer.
func TestResolve_TolerantOfHowANameIsWrittenBack(t *testing.T) {
	p, _, _ := twoCustomers(t)
	for _, asked := range []string{
		`"Acme Dental Group"`, "Acme Dental Group.", "Acme  Dental   Group",
		"ADG.", "'adg'", "\u00a0ADG\u00a0", "(globex)",
	} {
		a, err := p.resolve(asked)
		if err != nil {
			t.Errorf("%q should resolve: %v", asked, err)
			continue
		}
		want := "Acme Dental Group"
		if strings.Contains(strings.ToLower(asked), "globex") {
			want = "Globex Roofing"
		}
		if a.name != want {
			t.Errorf("%q resolved to %q, want %q", asked, a.name, want)
		}
	}

	// And it still refuses what it should. Loosening how a name is written
	// must not loosen which names match.
	for _, asked := range []string{"Initech", "Acme Dental Group Ltd", "roof"} {
		if _, err := p.resolve(asked); err == nil {
			t.Errorf("%q should not resolve to anything", asked)
		}
	}
}
