package flowroute

import (
	"context"
	"strings"
	"testing"
)

// One instance, many customers, one Flowroute key each. These tests defend the
// resolution -- which is the part where a mistake answers correctly about the
// wrong business.

func twoCustomers(t *testing.T, f *fakeFlowroute) *Plugin {
	t.Helper()
	f.credentials = map[string]string{
		"acmekey1": "acme-secret",
		"betakey1": "beta-secret",
	}
	p, _ := newPluginFor(t, f,
		Customer{Name: "Acme Dental Group", Aliases: []string{"acme", "ADG"},
			AccessKey: "acmekey1", SecretKey: "acme-secret"},
		Customer{Name: "Beta Logistics", Aliases: []string{"beta"},
			AccessKey: "betakey1", SecretKey: "beta-secret"},
	)
	return p
}

func TestResolvesByNameAliasAndPartial(t *testing.T) {
	t.Parallel()
	p := twoCustomers(t, newFake(t))

	cases := map[string]string{
		"Acme Dental Group": "Acme Dental Group",
		"acme dental group": "Acme Dental Group", // folded
		"ADG":               "Acme Dental Group", // alias
		"dental":            "Acme Dental Group", // partial, and only one match
		"Beta Logistics":    "Beta Logistics",
		"beta":              "Beta Logistics",
	}
	for asked, want := range cases {
		got, err := p.resolve(asked)
		if err != nil {
			t.Fatalf("resolve(%q): %v", asked, err)
		}
		if got.name != want {
			t.Fatalf("resolve(%q) = %q, want %q", asked, got.name, want)
		}
	}
}

// The whole point of refusing rather than guessing: picking the nearest match
// would answer confidently about somebody else's numbers.
func TestAmbiguityRefusesAndNamesBoth(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.credentials = map[string]string{"k1": "s1", "k2": "s2"}
	p, _ := newPluginFor(t, f,
		Customer{Name: "Acme Dental", AccessKey: "k1", SecretKey: "s1"},
		Customer{Name: "Acme Logistics", AccessKey: "k2", SecretKey: "s2"},
	)

	_, err := p.resolve("acme")
	if err == nil {
		t.Fatal("an ambiguous name should be refused")
	}
	for _, want := range []string{"Acme Dental", "Acme Logistics", "Do not pick one"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should say %q, said %q", want, err)
		}
	}
}

// An instance serving several customers cannot answer a question that names
// none of them.
func TestSeveralCustomersNeedOneNamed(t *testing.T) {
	t.Parallel()
	p := twoCustomers(t, newFake(t))

	_, err := p.listNumbers(context.Background(), numbersArgs{})
	if err == nil {
		t.Fatal("a call naming no customer should be refused")
	}
	if !strings.Contains(err.Error(), "say which one") ||
		!strings.Contains(err.Error(), "Acme Dental Group") {
		t.Fatalf("the refusal should name the choices, said %q", err)
	}
}

// A business mcpd has never been given cannot be read, so the answer has to be
// where that is fixed -- not the nearest configured customer.
func TestAnUnknownCustomerSaysWhereToAddIt(t *testing.T) {
	t.Parallel()
	p := twoCustomers(t, newFake(t))

	_, err := p.listNumbers(context.Background(), numbersArgs{Customer: "Globex"})
	if err == nil {
		t.Fatal("an unknown customer should be refused")
	}
	for _, want := range []string{"Globex", "Plugins page", "Customers", "access key"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should mention %q, said %q", want, err)
		}
	}
}

// The failure this shape exists to prevent: one customer's question asked with
// another customer's credential. Flowroute would answer it, correctly, about
// the wrong business.
func TestEachCustomerIsReadWithItsOwnCredential(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.bodies["/v2/numbers"] = numbersPage
	p := twoCustomers(t, f)

	if _, err := p.listNumbers(context.Background(), numbersArgs{Customer: "acme"}); err != nil {
		t.Fatalf("acme: %v", err)
	}
	if _, err := p.listNumbers(context.Background(), numbersArgs{Customer: "beta"}); err != nil {
		t.Fatalf("beta: %v", err)
	}
	if len(f.keys) != 2 {
		t.Fatalf("want two reads, saw %d", len(f.keys))
	}
	if f.keys[0] != "acmekey1" {
		t.Errorf("acme was read with %q", f.keys[0])
	}
	if f.keys[1] != "betakey1" {
		t.Errorf("beta was read with %q", f.keys[1])
	}
}

// Every answer names the business it is about, so it can never be read as
// another customer's.
func TestEveryAnswerNamesItsCustomer(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.bodies["/v2/numbers"] = numbersPage
	f.bodies["/v2/routes"] = `{"data":[],"links":{"self":"x"}}`
	f.bodies["/v2/e911s"] = `{"data":[],"links":{"self":"x"}}`
	f.bodies["/v2/cnams"] = `{"data":[],"links":{"self":"x"}}`
	p := twoCustomers(t, f)

	numbers, err := p.listNumbers(context.Background(), numbersArgs{Customer: "beta"})
	if err != nil {
		t.Fatalf("listNumbers: %v", err)
	}
	if numbers.Customer != "Beta Logistics" {
		t.Errorf("numbers answered for %q", numbers.Customer)
	}
	routes, err := p.listRoutes(context.Background(), routesArgs{Customer: "beta"})
	if err != nil {
		t.Fatalf("listRoutes: %v", err)
	}
	if routes.Customer != "Beta Logistics" {
		t.Errorf("routes answered for %q", routes.Customer)
	}
	e911s, err := p.listE911Addresses(context.Background(), e911ListArgs{Customer: "acme"})
	if err != nil {
		t.Fatalf("listE911Addresses: %v", err)
	}
	if e911s.Customer != "Acme Dental Group" {
		t.Errorf("addresses answered for %q", e911s.Customer)
	}
}

// One customer whose key has been rotated must not hide the others, and the
// health report has to say which one it is.
func TestOneBadCustomerIsNamedAndDoesNotStopTheRest(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.bodies["/v2/numbers"] = numbersPage
	p := twoCustomers(t, f)

	// Beta's key stops working: exactly what a rotation looks like.
	f.credentials["betakey1"] = "a-different-secret"

	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("one bad customer should not stop the instance mounting: %v", err)
	}

	got := p.Check(context.Background())
	if got.State == "healthy" {
		t.Fatal("an instance with a customer it cannot read is not healthy")
	}
	if !strings.Contains(got.Message, "Beta Logistics") {
		t.Errorf("the health report should name the failing customer, said %q", got.Message)
	}
	if strings.Contains(got.Message, "Acme Dental Group") {
		t.Errorf("the working customer should not appear as failing: %q", got.Message)
	}

	// And the good one still answers.
	if _, err := p.listNumbers(context.Background(), numbersArgs{Customer: "acme"}); err != nil {
		t.Fatalf("acme should still be readable: %v", err)
	}
}

func TestListCustomersReportsWhatTheLastCallFound(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.bodies["/v2/numbers"] = numbersPage
	p := twoCustomers(t, f)

	// Before anything has been asked, reachability is unknown rather than
	// false -- the two are different answers.
	got, err := p.listCustomers(context.Background(), customersArgs{})
	if err != nil {
		t.Fatalf("listCustomers: %v", err)
	}
	if got.Count != 2 {
		t.Fatalf("want two customers, got %d", got.Count)
	}
	for _, row := range got.Customers {
		if row.Reachable != nil {
			t.Errorf("%s should have no verdict before any call", row.Name)
		}
	}
	if len(got.Customers[0].Aliases) != 2 {
		t.Errorf("aliases not reported: %v", got.Customers[0].Aliases)
	}

	f.credentials["betakey1"] = "rotated"
	got, err = p.listCustomers(context.Background(), customersArgs{Check: true})
	if err != nil {
		t.Fatalf("listCustomers(check): %v", err)
	}
	byName := map[string]CustomerRow{}
	for _, row := range got.Customers {
		byName[row.Name] = row
	}
	if r := byName["Acme Dental Group"]; r.Reachable == nil || !*r.Reachable {
		t.Errorf("acme should be reachable: %+v", r)
	}
	if r := byName["Beta Logistics"]; r.Reachable == nil || *r.Reachable {
		t.Errorf("beta should be unreachable: %+v", r)
	} else if r.LastError == "" {
		t.Error("an unreachable customer should say why")
	}
}
