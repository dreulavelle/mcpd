package threecx

import (
	"strings"
	"testing"
)

func oneCustomer(host string) Config {
	cfg := Config{Customers: []Customer{{Name: "Acme", Host: host, Extension: "100", Password: "p"}}}
	cfg.withDefaults()
	return cfg
}

// The address is accepted the way 3CX itself reports it -- a bare FQDN -- and
// as a URL, and refused when it carries anything that would make requests land
// somewhere other than the phone system's root.
func TestConfig_AddressForms(t *testing.T) {
	cases := []struct {
		host    string
		root    string
		refused string
	}{
		{host: "acme.ny.3cx.us", root: "https://acme.ny.3cx.us"},
		{host: "https://acme.ny.3cx.us", root: "https://acme.ny.3cx.us"},
		{host: "https://acme.ny.3cx.us/", root: "https://acme.ny.3cx.us"},
		{host: "http://pbx.internal:5000", root: "http://pbx.internal:5000"},
		{host: "  acme.ny.3cx.us  ", root: "https://acme.ny.3cx.us"},
		{host: "https://acme.ny.3cx.us/xapi/v1", refused: "web root"},
		{host: "https://acme.ny.3cx.us/webclient", refused: "web root"},
		{host: "https://acme.ny.3cx.us/something", refused: "carries a path"},
		{host: "https://100:secret@acme.ny.3cx.us", refused: "not in the address"},
		{host: "ftp://acme.ny.3cx.us", refused: "http or https"},
	}
	for _, c := range cases {
		cfg := oneCustomer(c.host)
		err := cfg.Validate()
		if c.refused != "" {
			if err == nil || !strings.Contains(err.Error(), c.refused) {
				t.Errorf("%q: want a refusal mentioning %q, got %v", c.host, c.refused, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected refusal: %v", c.host, err)
			continue
		}
		if got := rootOf(c.host); got != c.root {
			t.Errorf("%q: root = %q, want %q", c.host, got, c.root)
		}
	}
}

// An instance nobody has configured is valid and reports itself unconfigured,
// so it mounts and shows its form rather than refusing to start the host.
func TestConfig_EmptyIsValidAndUnconfigured(t *testing.T) {
	var cfg Config
	cfg.withDefaults()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("an empty configuration should validate: %v", err)
	}
	if cfg.Configured() {
		t.Error("an empty configuration is not configured")
	}
	if cfg.MaxItems != defaultMaxItems || cfg.RequestsPerSecond != defaultRPS || cfg.Timeout != defaultTimeout {
		t.Errorf("defaults not applied: %+v", cfg)
	}
}

// A customer needs all four of name, address, extension and password; one
// half filled in leaves the instance unconfigured rather than silently served
// without that customer.
func TestConfig_ConfiguredNeedsEveryCustomerComplete(t *testing.T) {
	full := oneCustomer("acme.ny.3cx.us")
	if !full.Configured() {
		t.Error("a complete customer should be configured")
	}
	for name, partial := range map[string]Customer{
		"no host":      {Name: "Acme", Extension: "100", Password: "p"},
		"no extension": {Name: "Acme", Host: "acme.ny.3cx.us", Password: "p"},
		"no password":  {Name: "Acme", Host: "acme.ny.3cx.us", Extension: "100"},
	} {
		cfg := Config{Customers: []Customer{full.Customers[0], partial}}
		if cfg.Configured() {
			t.Errorf("%s should leave the instance unconfigured", name)
		}
	}
}

// Two customers may not share a name or alias, and may not share a phone
// system: either is a call that could only be resolved by guessing.
func TestConfig_RefusesCollidingCustomers(t *testing.T) {
	cases := map[string]Config{
		"same name": {Customers: []Customer{
			{Name: "Acme", Host: "a.example", Extension: "100", Password: "p"},
			{Name: "acme", Host: "b.example", Extension: "100", Password: "p"},
		}},
		"alias is another's name": {Customers: []Customer{
			{Name: "Acme Dental", Aliases: []string{"globex"}, Host: "a.example", Extension: "100", Password: "p"},
			{Name: "Globex", Host: "b.example", Extension: "100", Password: "p"},
		}},
		"same host": {Customers: []Customer{
			{Name: "Acme", Host: "pbx.example", Extension: "100", Password: "p"},
			{Name: "Globex", Host: "https://PBX.example/", Extension: "101", Password: "p"},
		}},
		"no name": {Customers: []Customer{
			{Host: "pbx.example", Extension: "100", Password: "p"},
		}},
	}
	for name, cfg := range cases {
		cfg.withDefaults()
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s should be refused", name)
		}
	}
	// A customer's own alias repeating its name is not a collision.
	ok := Config{Customers: []Customer{{Name: "Acme", Aliases: []string{"acme", "ACME"}, Host: "a.example", Extension: "100", Password: "p"}}}
	ok.withDefaults()
	if err := ok.Validate(); err != nil {
		t.Errorf("an alias repeating the customer's own name is fine: %v", err)
	}
}

// The plugin built from settings keeps no password on its config, so a dump of
// the config -- a log line, the settings page -- cannot carry it.
func TestNew_DoesNotRetainThePassword(t *testing.T) {
	p, err := New(testDeps(), Config{Customers: []Customer{
		{Name: "Acme", Host: "acme.ny.3cx.us", Extension: "100", Password: "secret-pass"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if p.cfg.Customers[0].Password != "" {
		t.Error("the plugin's config still holds the password")
	}
	if p.accounts[0].client.password != "secret-pass" {
		t.Error("the client should hold the password, and only the client")
	}
}
