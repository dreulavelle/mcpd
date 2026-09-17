package threecx

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// A business with more than one 3CX.
//
// The shape these defend: a row is a phone system, the Business column says
// which business owns it, and nothing may answer for a customer's two systems
// as though it had one. The failure they exist to prevent is silent -- an
// answer about the branch office read as the whole business, with nothing in
// the result to say otherwise -- so most of them check what came back rather
// than only that it came back.

// estate builds a plugin over a table with the shapes that matter at once: a
// business with three phone systems, a business with two, and two businesses
// with one each whose names are nearly the same.
//
// Every fake answers anything, so a test can be about which one a call
// reached. Each reports its own FQDN from SystemStatus, which is how a test
// says which phone system actually answered.
func estate(t *testing.T) (*Plugin, map[string]*fakePBX) {
	t.Helper()
	rows := []struct{ name, business, host string }{
		{"Acme HQ", "Acme Dental Group", "acme-hq.example"},
		{"Acme Branch", "Acme Dental Group", "acme-branch.example"},
		{"Acme Annexe", "Acme Dental Group", "acme-annexe.example"},
		{"Globex Main", "Globex Roofing", "globex-main.example"},
		{"Globex Yard", "Globex Roofing", "globex-yard.example"},
		{"Initech", "", "initech.example"},
		{"Initech Holdings", "", "initech-holdings.example"},
	}
	fakes := map[string]*fakePBX{}
	clients := map[string]*http.Client{}
	systems := make([]System, 0, len(rows))
	for _, r := range rows {
		f, srv := newFakePBX(t, map[string]string{
			"SystemStatus": fmt.Sprintf(`{"FQDN":%q,"Version":"20.0.1"}`, r.host),
		})
		f.anything = true
		fakes[r.name], clients[r.name] = f, srv.Client()
		systems = append(systems, System{
			Name: r.name, Customer: r.business, Host: srv.URL,
			Extension: "100", Password: "right-password",
		})
	}
	// Aliases that say something about the shape rather than decorating it:
	// "hq" and "branch" pick between Acme's systems, and "main" is used by
	// Globex as well, which is the collision a call has to be refused over.
	systems[0].Aliases = []string{"hq", "main"}
	systems[1].Aliases = []string{"branch"}
	systems[3].Aliases = []string{"main"}

	p, err := New(testDeps(), Config{Systems: systems, RequestsPerSecond: 1000})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range p.accounts {
		a.client.http = readOnly(clients[a.name], a.host)
	}
	return p, fakes
}

// A customer owns its systems, and the systems know their customer.
func TestSystems_TheModelIsCustomerToSystems(t *testing.T) {
	p, _ := estate(t)

	if len(p.customers) != 4 {
		names := []string{}
		for _, c := range p.customers {
			names = append(names, c.name)
		}
		t.Fatalf("four businesses, got %v", names)
	}
	if len(p.accounts) != 7 {
		t.Fatalf("seven phone systems, got %d", len(p.accounts))
	}
	want := map[string]int{
		"Acme Dental Group": 3, "Globex Roofing": 2,
		"Initech": 1, "Initech Holdings": 1,
	}
	for _, c := range p.customers {
		if got := len(c.systems); got != want[c.name] {
			t.Errorf("%s has %d phone systems, want %d", c.name, got, want[c.name])
		}
		for _, a := range c.systems {
			if a.owner != c {
				t.Errorf("%s does not point back at %s", a.name, c.name)
			}
		}
	}
	// The identifier is the name, not the business's name, so two systems of
	// one business are told apart by it.
	ids := map[string]bool{}
	for _, a := range p.accounts {
		if a.id == "" {
			t.Errorf("%s has no identifier", a.name)
		}
		if ids[a.id] {
			t.Errorf("two phone systems share the identifier %q", a.id)
		}
		ids[a.id] = true
	}
	if !ids["acme-hq"] || !ids["acme-branch"] || !ids["acme-annexe"] {
		t.Errorf("identifiers are derived from the system's name, got %v", ids)
	}
}

// A customer with one phone system resolves with no system named, which is
// every deployment that existed before this and most of them after.
func TestSystems_OneSystemNeedsNoNaming(t *testing.T) {
	p, _ := estate(t)
	a, err := p.resolve("Initech", "")
	if err != nil {
		t.Fatalf("a business with one phone system settles on its own: %v", err)
	}
	if a.name != "Initech" {
		t.Errorf("resolved to %q", a.name)
	}
	// And the nearly-identical neighbour is a different business, not a
	// second system of this one.
	b, err := p.resolve("Initech Holdings", "")
	if err != nil || b.name != "Initech Holdings" {
		t.Errorf("two businesses with similar names are two businesses, got %v %v", b, err)
	}
	if a.owner == b.owner {
		t.Error("Initech and Initech Holdings are not one business")
	}
}

// A customer with several and no system named is refused -- with the systems
// listed, so the caller can ask the person rather than guess.
func TestSystems_AmbiguousCustomerIsRefusedWithTheChoices(t *testing.T) {
	p, fakes := estate(t)

	_, err := p.resolve("Acme Dental Group", "")
	if err == nil {
		t.Fatal("a business with three phone systems and no system named must be refused")
	}
	for _, want := range []string{
		"Acme Dental Group has 3 phone systems",
		"say which one with system",
		"acme-hq", "acme-branch", "acme-annexe",
		"Do not pick one -- ask the person",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should say %q, got %v", want, err)
		}
	}
	// Refused before anything is read: the point is that no phone system is
	// picked, not that the wrong answer is labelled afterwards.
	for name, f := range fakes {
		if f.reads.Load() != 0 {
			t.Errorf("%s was read while the target was still ambiguous", name)
		}
	}

	// A tool call, not just the resolver: the same refusal has to reach a
	// caller through the tool it used.
	if _, err := p.getSystemStatus(context.Background(), statusArgs{Customer: "acme dental group"}); err == nil {
		t.Error("the tool must refuse it too")
	}
}

// Naming the system settles it, and the call reaches that system and no other.
func TestSystems_ExplicitSelectionReachesThatSystem(t *testing.T) {
	p, fakes := estate(t)
	ctx := context.Background()

	for _, sys := range []string{"Acme Branch", "acme-branch", "branch", "ACME BRANCH"} {
		for _, f := range fakes {
			f.reads.Store(0)
		}
		got, err := p.getSystemStatus(ctx, statusArgs{Customer: "Acme Dental Group", System: sys})
		if err != nil {
			t.Errorf("system %q: %v", sys, err)
			continue
		}
		if got.FQDN != "acme-branch.example" {
			t.Errorf("system %q reached %s", sys, got.FQDN)
		}
		for name, f := range fakes {
			if name != "Acme Branch" && f.reads.Load() != 0 {
				t.Errorf("system %q also reached %s", sys, name)
			}
		}
	}

	// And without the customer at all: an identifier is unique across the
	// instance, so it is enough on its own.
	got, err := p.getSystemStatus(ctx, statusArgs{System: "acme-annexe"})
	if err != nil {
		t.Fatalf("an identifier alone should settle it: %v", err)
	}
	if got.FQDN != "acme-annexe.example" {
		t.Errorf("reached %s", got.FQDN)
	}
}

// Every answer says which phone system it came from, so a result can never be
// read as another of the same customer's.
func TestSystems_EveryAnswerNamesItsSource(t *testing.T) {
	p, _ := estate(t)
	ctx := context.Background()

	st, err := p.getSystemStatus(ctx, statusArgs{System: "acme-branch"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Customer != "Acme Dental Group" || st.System != "Acme Branch" || st.SystemID != "acme-branch" {
		t.Errorf("the answer should name both, got customer=%q system=%q id=%q",
			st.Customer, st.System, st.SystemID)
	}
	// In the JSON a model actually reads, not only on the Go value.
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"customer":"Acme Dental Group"`, `"system":"Acme Branch"`, `"system_id":"acme-branch"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the encoded answer should carry %s, got %s", want, raw)
		}
	}

	ext, err := p.listExtensions(ctx, extensionsArgs{Customer: "Globex Roofing", System: "globex-yard"})
	if err != nil {
		t.Fatal(err)
	}
	if ext.Customer != "Globex Roofing" || ext.SystemID != "globex-yard" {
		t.Errorf("a listing names its source too, got %+v", ext.Source)
	}
}

// An alias two businesses both use is refused when it is given alone, and
// resolves when the customer says which business is meant.
func TestSystems_CollidingAliasIsRefusedRatherThanPicked(t *testing.T) {
	p, _ := estate(t)

	_, err := p.resolve("", "main")
	if err == nil {
		t.Fatal(`"main" names one system of Acme and one of Globex; it must be refused`)
	}
	for _, want := range []string{"ambiguous", "acme-hq", "globex-main", "ask the person"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should say %q, got %v", want, err)
		}
	}
	// The error says which business each candidate belongs to, since that is
	// the thing the caller has to ask about.
	for _, want := range []string{"Acme Dental Group", "Globex Roofing"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should name %q, got %v", want, err)
		}
	}

	// With the customer, the same word is not ambiguous at all.
	a, err := p.resolve("Globex Roofing", "main")
	if err != nil || a.name != "Globex Main" {
		t.Errorf(`"main" within Globex is Globex Main, got %v %v`, a, err)
	}
	b, err := p.resolve("Acme Dental Group", "main")
	if err != nil || b.name != "Acme HQ" {
		t.Errorf(`"main" within Acme is Acme HQ, got %v %v`, b, err)
	}
}

// Naming a system that belongs to somebody else is refused, and says whose it
// is rather than reading it.
func TestSystems_ASystemOfAnotherCustomerIsRefused(t *testing.T) {
	p, fakes := estate(t)

	_, err := p.resolve("Acme Dental Group", "globex-yard")
	if err == nil {
		t.Fatal("a system of another business must not be read for this one")
	}
	for _, want := range []string{"Globex Yard belongs to Globex Roofing", "not to Acme Dental Group", "acme-hq"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should say %q, got %v", want, err)
		}
	}
	if fakes["Globex Yard"].reads.Load() != 0 {
		t.Error("nothing should have been read")
	}

	// And a system nobody has, within a customer that does exist.
	_, err = p.resolve("Acme Dental Group", "acme-warehouse")
	if err == nil {
		t.Fatal("an unknown system must be refused")
	}
	for _, want := range []string{
		`Acme Dental Group has no phone system called "acme-warehouse"`,
		"acme-hq", "ask the person",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should say %q, got %v", want, err)
		}
	}
}

// Discovery reports the relationship, not a flat list whose names happen to
// look alike.
func TestSystems_DiscoveryShowsTheRelationship(t *testing.T) {
	p, _ := estate(t)
	out, err := p.listCustomers(context.Background(), customersArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Count != 4 || out.SystemCount != 7 {
		t.Fatalf("four businesses over seven phone systems, got %d and %d", out.Count, out.SystemCount)
	}
	byName := map[string]CustomerRow{}
	for _, c := range out.Customers {
		byName[c.Name] = c
	}

	acme := byName["Acme Dental Group"]
	if acme.SystemCount != 3 || len(acme.Systems) != 3 {
		t.Fatalf("Acme should carry its three systems, got %+v", acme)
	}
	if acme.ID != "acme-dental-group" {
		t.Errorf("the business has an identifier of its own, got %q", acme.ID)
	}
	// A business with several has no single address, and does not invent one.
	if acme.Host != "" || acme.Reachable != nil {
		t.Errorf("a multi-system business must not report one address or one health: %+v", acme)
	}
	for i, want := range []string{"acme-hq", "acme-branch", "acme-annexe"} {
		if acme.Systems[i].ID != want {
			t.Errorf("system %d is %q, want %q", i, acme.Systems[i].ID, want)
		}
		if acme.Systems[i].Host == "" {
			t.Errorf("system %q reports no address", want)
		}
	}
	if strings.Join(acme.Systems[0].Aliases, ",") != "hq,main" {
		t.Errorf("aliases belong to the system that has them, got %v", acme.Systems[0].Aliases)
	}

	// A business with one still reports the flat fields it always reported,
	// so a caller written before any of this reads it unchanged.
	initech := byName["Initech"]
	if initech.SystemCount != 1 || len(initech.Systems) != 1 {
		t.Fatalf("Initech: %+v", initech)
	}
	if initech.Host != initech.Systems[0].Host || initech.Host == "" {
		t.Errorf("a single-system business carries its system's address: %+v", initech)
	}
}

// Discovery and the refusals carry no credential. The passwords are on the
// clients and nowhere a caller can reach.
func TestSystems_NothingLeaksACredential(t *testing.T) {
	p, _ := estate(t)
	ctx := context.Background()

	out, err := p.listCustomers(ctx, customersArgs{})
	if err != nil {
		t.Fatal(err)
	}
	mustNotContain(t, out, "right-password", "password", "extension", "secret", "token")

	// The errors are read by a model and repeated to a person, so they are
	// checked the same way.
	for _, bad := range [][2]string{{"Acme Dental Group", ""}, {"", "main"}, {"Acme Dental Group", "globex-yard"}, {"nobody", ""}} {
		_, err := p.resolve(bad[0], bad[1])
		if err == nil {
			t.Fatalf("%v should have been refused", bad)
		}
		for _, word := range []string{"right-password", "password=", "Bearer"} {
			if strings.Contains(err.Error(), word) {
				t.Errorf("the refusal carries %q: %v", word, err)
			}
		}
	}
}

// Adding a second phone system to a business that had one changes that
// business from settled to ambiguous, and changes nothing about the others.
//
// This is what an operator does on the Plugins page: they add a row and name
// the business it belongs to. The plugin is rebuilt from the rows, so the
// question is whether the rebuilt one has the relationship rather than seven
// unrelated rows.
func TestSystems_AddingASecondSystemToAnExistingCustomer(t *testing.T) {
	before := []System{
		{Name: "Acme Dental Group", Host: "acme.example", Extension: "100", Password: "p"},
		{Name: "Globex Roofing", Host: "globex.example", Extension: "100", Password: "p"},
	}
	p, err := New(testDeps(), Config{Systems: before})
	if err != nil {
		t.Fatal(err)
	}
	if a, err := p.resolve("Acme Dental Group", ""); err != nil || a.name != "Acme Dental Group" {
		t.Fatalf("one system each: %v %v", a, err)
	}

	// The operator adds the second site. The business name goes on both rows,
	// and the row that was there keeps its name -- which is what the dashboard
	// does when somebody edits one row and adds another.
	after := []System{
		{Name: "Acme HQ", Customer: "Acme Dental Group", Host: "acme.example", Extension: "100", Password: "p"},
		{Name: "Globex Roofing", Host: "globex.example", Extension: "100", Password: "p"},
		{Name: "Acme Branch", Customer: "Acme Dental Group", Host: "acme-branch.example", Extension: "100", Password: "p"},
	}
	p2, err := New(testDeps(), Config{Systems: after})
	if err != nil {
		t.Fatal(err)
	}
	if len(p2.customers) != 2 {
		t.Fatalf("still two businesses, got %d", len(p2.customers))
	}
	if _, err := p2.resolve("Acme Dental Group", ""); err == nil {
		t.Error("Acme now has two phone systems and must be asked about by system")
	}
	// The business that was not touched answers exactly as it did.
	if a, err := p2.resolve("Globex Roofing", ""); err != nil || a.name != "Globex Roofing" {
		t.Errorf("the untouched business should be unaffected: %v %v", a, err)
	}
	// And the rows are grouped, not merely adjacent: the second Acme row is
	// listed with the first even though another business sits between them.
	out, err := p2.listCustomers(context.Background(), customersArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Customers[0].Name != "Acme Dental Group" || out.Customers[0].SystemCount != 2 {
		t.Errorf("Acme should carry both of its systems, got %+v", out.Customers[0])
	}
}

// Whatever a caller writes the customer as, tools resolve it the same way --
// including the names list_customers gave for a business's individual systems,
// which is what a model will have in front of it.
func TestSystems_TheCustomerArgumentStillTakesASystemName(t *testing.T) {
	p, _ := estate(t)
	ctx := context.Background()

	// Every one of these is a phone system's own name or alias passed as
	// `customer`, which is how a table that used to be one row per business
	// was asked about, and how a model that read a system's name will ask.
	for _, asked := range []string{"Acme Branch", "acme-branch", "branch", "Globex Yard"} {
		got, err := p.getSystemStatus(ctx, statusArgs{Customer: asked})
		if err != nil {
			t.Errorf("customer %q: %v", asked, err)
			continue
		}
		want := "acme-branch.example"
		if strings.Contains(strings.ToLower(asked), "globex") {
			want = "globex-yard.example"
		}
		if got.FQDN != want {
			t.Errorf("customer %q reached %s, want %s", asked, got.FQDN, want)
		}
	}

	// Naming two different phone systems in the two arguments is refused
	// rather than one of them being preferred.
	_, err := p.resolve("Acme HQ", "acme-branch")
	if err == nil {
		t.Fatal("customer and system naming different phone systems must be refused")
	}
	if !strings.Contains(err.Error(), "two different phone systems") {
		t.Errorf("the refusal should say so, got %v", err)
	}
}

// One business with several phone systems and nothing named at all: the
// refusal asks for the system rather than for the customer, because naming
// the only business would not settle anything.
func TestSystems_OneBusinessWithSeveralAsksForTheSystem(t *testing.T) {
	p, err := New(testDeps(), Config{Systems: []System{
		{Name: "Site A", Customer: "Acme", Host: "a.example", Extension: "100", Password: "p"},
		{Name: "Site B", Customer: "Acme", Host: "b.example", Extension: "100", Password: "p"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.resolve("", "")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "Acme has 2 phone systems") || !strings.Contains(err.Error(), "site-a") {
		t.Errorf("it should ask for the system, got %v", err)
	}
}

// An argument that carries no name is not a name.
//
// A model writing back a quoted or punctuated value is the case normaliseName
// exists for, and it applies to `system` as well: a system of "." must mean
// "not given" rather than "a fragment that matches everything", which is what
// a contains-match on the empty string does.
func TestSystems_AnEmptyishSystemIsNotAWildcard(t *testing.T) {
	p, _ := estate(t)
	for _, given := range []string{"", " ", ".", "  -  "} {
		_, err := p.resolve("Acme Dental Group", given)
		if err == nil {
			t.Errorf("system %q should not settle a business with three phone systems", given)
			continue
		}
		if !strings.Contains(err.Error(), "has 3 phone systems") {
			t.Errorf("system %q should be read as unset, got %v", given, err)
		}
	}
	// And a quoted identifier still resolves, since the same normalisation is
	// what lets that through.
	for _, given := range []string{`"acme-branch"`, "acme-branch.", " ACME-BRANCH "} {
		a, err := p.resolve("Acme Dental Group", given)
		if err != nil || a.name != "Acme Branch" {
			t.Errorf("system %q should resolve to Acme Branch, got %v %v", given, a, err)
		}
	}
}
