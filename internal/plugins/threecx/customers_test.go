package threecx

import (
	"context"
	"strings"
	"testing"
)

// twoCustomers builds a plugin over two fake phone systems that answer with
// different versions, so a test can tell which one a call reached.
func twoCustomers(t *testing.T) (*Plugin, *fakePBX, *fakePBX) {
	t.Helper()
	acme, acmeSrv := newFakePBX(t, map[string]string{
		"SystemStatus": `{"FQDN":"acme.example","Version":"20.0.1"}`, "LicenseStatus": `{}`, "Trunks": collection(0)})
	globex, globexSrv := newFakePBX(t, map[string]string{
		"SystemStatus": `{"FQDN":"globex.example","Version":"20.0.2"}`, "LicenseStatus": `{}`, "Trunks": collection(0)})
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

	_, err = p.resolve("Initech")
	if err == nil || !strings.Contains(err.Error(), `no customer here is called "Initech"`) || !strings.Contains(err.Error(), "Acme Dental Group, Globex Roofing") {
		t.Errorf("an unknown name should be refused with the list, got %v", err)
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
}
