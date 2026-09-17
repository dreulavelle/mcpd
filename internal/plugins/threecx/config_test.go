package threecx

import (
	"strings"
	"testing"
	"time"
)

func oneCustomer(host string) Config {
	cfg := Config{Systems: []System{{Name: "Acme", Host: host, Extension: "100", Password: "p"}}}
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
		// A port on a bare name. url.Parse reads "example.com:5001" as a
		// scheme and an opaque body, so the scheme is prepended before it is
		// parsed rather than after -- which is what keeps a customer whose
		// console is on a non-standard port working.
		{host: "acme.ny.3cx.us:5001", root: "https://acme.ny.3cx.us:5001"},
		{host: "https://acme.ny.3cx.us:5001", root: "https://acme.ny.3cx.us:5001"},
		{host: "https://acme.ny.3cx.us:5001/", root: "https://acme.ny.3cx.us:5001"},
		{host: "  acme.ny.3cx.us:5001/  ", root: "https://acme.ny.3cx.us:5001"},
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
	if cfg.MaxItems != defaultMaxItems || cfg.RequestsPerSecond != defaultRPS || cfg.Timeout() != defaultTimeout {
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
	for name, partial := range map[string]System{
		"no host":      {Name: "Acme", Extension: "100", Password: "p"},
		"no extension": {Name: "Acme", Host: "acme.ny.3cx.us", Password: "p"},
		"no password":  {Name: "Acme", Host: "acme.ny.3cx.us", Extension: "100"},
	} {
		cfg := Config{Systems: []System{full.Systems[0], partial}}
		if cfg.Configured() {
			t.Errorf("%s should leave the instance unconfigured", name)
		}
	}
}

// A naming a call could not be resolved against without guessing is refused at
// the configuration rather than at every call.
//
// Two rows of the *same* business may not share a name or alias, no two rows
// may share an address, and a business's own name may not also name something
// else. What is deliberately allowed is below: two different businesses using
// the same alias.
func TestConfig_RefusesCollidingCustomers(t *testing.T) {
	cases := map[string]Config{
		"same name, folded": {Systems: []System{
			{Name: "Acme", Host: "a.example", Extension: "100", Password: "p"},
			{Name: "acme", Host: "b.example", Extension: "100", Password: "p"},
		}},
		// Two rows spelt identically. This slipped through until the guard
		// stopped comparing labels: both labels are the string "Acme", so the
		// check compared a value with itself and found no collision. Every
		// call to either was then refused as ambiguous at run time, which is a
		// worse place to learn about it than the settings page.
		"same name, exactly": {Systems: []System{
			{Name: "Acme", Host: "a.example", Extension: "100", Password: "p"},
			{Name: "Acme", Host: "b.example", Extension: "101", Password: "p"},
		}},
		// One business, two phone systems, one alias between them: a call to
		// that business naming "hq" could not be settled, and asking the
		// person is no help because they did say which.
		"same alias on one business's two systems": {Systems: []System{
			{Name: "Acme HQ", Customer: "Acme", Aliases: []string{"hq"}, Host: "a.example", Extension: "100", Password: "p"},
			{Name: "Acme Branch", Customer: "Acme", Aliases: []string{"hq"}, Host: "b.example", Extension: "101", Password: "p"},
		}},
		// A business with two systems cannot also be the name of one of them:
		// "Acme" would mean the pair and one of the pair at once. This is the
		// trap a table upgraded in place falls into, where the business is
		// named after the phone system it used to be.
		"business named after one of its own systems": {Systems: []System{
			{Name: "Acme", Customer: "Acme", Host: "a.example", Extension: "100", Password: "p"},
			{Name: "Acme Branch", Customer: "Acme", Host: "b.example", Extension: "101", Password: "p"},
		}},
		// Two businesses whose names differ only by punctuation are one
		// business to the resolver and to this check.
		"business name another business cannot be told from": {Systems: []System{
			{Name: "First", Customer: "Acme Inc", Host: "a.example", Extension: "100", Password: "p"},
			{Name: "Second", Customer: "Acme-Inc", Host: "b.example", Extension: "101", Password: "p"},
			{Name: "Third", Customer: "Acme Inc", Host: "c.example", Extension: "102", Password: "p"},
		}},
		"alias is another's name": {Systems: []System{
			{Name: "Acme Dental", Aliases: []string{"globex"}, Host: "a.example", Extension: "100", Password: "p"},
			{Name: "Globex", Host: "b.example", Extension: "100", Password: "p"},
		}},
		"same host": {Systems: []System{
			{Name: "Acme", Host: "pbx.example", Extension: "100", Password: "p"},
			{Name: "Globex", Host: "https://PBX.example/", Extension: "101", Password: "p"},
		}},
		"no name": {Systems: []System{
			{Host: "pbx.example", Extension: "100", Password: "p"},
		}},
	}
	for name, cfg := range cases {
		cfg.withDefaults()
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s should be refused", name)
		}
	}
	allowed := map[string]Config{
		// A customer's own alias repeating its name is not a collision.
		"an alias repeating the row's own name": {Systems: []System{
			{Name: "Acme", Aliases: []string{"acme", "ACME"}, Host: "a.example", Extension: "100", Password: "p"},
		}},
		// One business, one phone system, named the same: this is every row of
		// a table filled in before the Business column existed, and it has to
		// stay legal or every existing deployment is refused.
		"a business whose one system carries its name": {Systems: []System{
			{Name: "Acme", Customer: "Acme", Host: "a.example", Extension: "100", Password: "p"},
		}},
		// Two businesses, one alias. Refused before a business could have two
		// systems, and allowed now on purpose: "server 1" is what everybody
		// calls their first one, and a global ban would make the aliases
		// people actually want unusable. A call giving it with a customer
		// resolves inside that customer; a call giving it alone is refused at
		// the time, with both named.
		"the same alias under two different businesses": {Systems: []System{
			{Name: "Acme Dental", Aliases: []string{"server 1"}, Host: "a.example", Extension: "100", Password: "p"},
			{Name: "Globex Roofing", Aliases: []string{"server 1"}, Host: "b.example", Extension: "101", Password: "p"},
		}},
	}
	for name, cfg := range allowed {
		cfg.withDefaults()
		if err := cfg.Validate(); err != nil {
			t.Errorf("%s should be accepted: %v", name, err)
		}
	}
}

// The plugin built from settings keeps no password on its config, so a dump of
// the config -- a log line, the settings page -- cannot carry it.
func TestNew_DoesNotRetainThePassword(t *testing.T) {
	p, err := New(testDeps(), Config{Systems: []System{
		{Name: "Acme", Host: "acme.ny.3cx.us", Extension: "100", Password: "secret-pass"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if p.cfg.Systems[0].Password != "" {
		t.Error("the plugin's config still holds the password")
	}
	if p.accounts[0].client.password != "secret-pass" {
		t.Error("the client should hold the password, and only the client")
	}
}

// A port is part of the host everywhere it matters: the address requests are
// built on, the transport's check that a request is going to the configured
// phone system, and what the customer list reports.
func TestConfig_PortsSurviveEverywhere(t *testing.T) {
	cfg := oneCustomer("acme.ny.3cx.us:5001")
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	root := rootOf(cfg.Systems[0].Host)
	if root != "https://acme.ny.3cx.us:5001" {
		t.Fatalf("root = %q", root)
	}
	if got := displayHost(root); got != "acme.ny.3cx.us:5001" {
		t.Errorf("the customer list should keep the port, got %q", got)
	}

	// The guard is built from that root, so a request to the same host and
	// port is permitted and one to the default port is not: they are two
	// different phone systems.
	c := readOnly(nil, root)
	if err := try(t, c, "GET", "https://acme.ny.3cx.us:5001/xapi/v1/Users?$select=Id"); err == nil {
		t.Error("the configured host and port should be permitted (no transport, so a dial error is expected, not a refusal)")
	} else if strings.Contains(err.Error(), "not the configured phone system") {
		t.Errorf("the configured host and port was refused: %v", err)
	}
	err := try(t, c, "GET", "https://acme.ny.3cx.us/xapi/v1/Users?$select=Id")
	if err == nil || !strings.Contains(err.Error(), "not the configured phone system") {
		t.Errorf("a different port is a different system and should be refused, got %v", err)
	}
}

// The timeout is an operator's to set, within bounds: a large phone system
// genuinely takes longer than thirty seconds on a wide question, and the
// alternative was editing a constant.
func TestConfig_TimeoutIsSettableWithinBounds(t *testing.T) {
	base := func(seconds int) Config {
		return Config{
			Systems:        []System{{Name: "Acme", Host: "acme.example", Extension: "100", Password: "p"}},
			TimeoutSeconds: seconds, MaxItems: defaultMaxItems, RequestsPerSecond: defaultRPS,
		}
	}
	cfg := base(90)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("90 seconds should be allowed: %v", err)
	}
	if cfg.Timeout() != 90*time.Second {
		t.Errorf("timeout %v, want 90s", cfg.Timeout())
	}
	for _, seconds := range []int{1, 600} {
		if err := base(seconds).Validate(); err == nil {
			t.Errorf("%d seconds is outside the range and should be refused", seconds)
		}
	}
	// Left alone it is the default, not zero -- a zero timeout on an
	// http.Client means no timeout at all.
	var none Config
	if none.Timeout() != defaultTimeout {
		t.Errorf("an unset timeout is the default, got %v", none.Timeout())
	}
}

// The timeout arrives from the settings store as whole seconds, and the field
// it lands in has to be an int for that reason: decoded into a time.Duration,
// a 45 would be forty-five nanoseconds, every request would fail instantly,
// and nothing would refuse to compile.
func TestConfig_TimeoutDecodesAsSeconds(t *testing.T) {
	var c Config
	if err := decode(map[string]any{"timeout": 45}, &c); err != nil {
		t.Fatal(err)
	}
	c.withDefaults()
	if c.TimeoutSeconds != 45 || c.Timeout() != 45*time.Second {
		t.Errorf("a stored timeout of 45 is forty-five seconds, got %d / %v", c.TimeoutSeconds, c.Timeout())
	}
	// And the field the dashboard writes is the field the plugin reads: the
	// settings declaration has to name the same key.
	found := false
	for _, f := range Type().Settings {
		if f.Key == "timeout" {
			found = true
			if f.Default != int(defaultTimeout/time.Second) {
				t.Errorf("the form's default should be the plugin's, got %v", f.Default)
			}
			if f.Min == nil || *f.Min != minTimeoutSeconds || f.Max == nil || *f.Max != maxTimeoutSeconds {
				t.Errorf("the form's range should be what Validate enforces, got %v-%v", f.Min, f.Max)
			}
		}
	}
	if !found {
		t.Error("the timeout is not on the settings form, so nobody can raise it")
	}
}

// Two customers a caller could not tell apart are refused at the configuration
// rather than at every call. The resolver ignores case, repeated whitespace
// and the punctuation a name is written back with, so names differing only in
// those are one name as far as anybody asking is concerned.
func TestConfig_NamesTheResolverCannotTellApartAreRefused(t *testing.T) {
	// Each pair collides on the name or the alias quoted beside it. They are
	// rows of one business, which is what two names that cannot be told apart
	// amount to: an unset Business column means the row is its own business,
	// so "Acme Inc" and "Acme Inc." are one business with two phone systems
	// and one name between them.
	pairs := [][2]System{
		{{Name: "Acme Inc", Host: "a.example", Extension: "100", Password: "p"},
			{Name: "Acme Inc.", Host: "b.example", Extension: "100", Password: "p"}},
		{{Name: "Acme Dental", Host: "a.example", Extension: "100", Password: "p"},
			{Name: "Acme  Dental", Host: "b.example", Extension: "100", Password: "p"}},
		{{Name: "Acme HQ", Customer: "Acme", Aliases: []string{"ADG."}, Host: "a.example", Extension: "100", Password: "p"},
			{Name: "Acme Branch", Customer: "Acme", Aliases: []string{"adg"}, Host: "b.example", Extension: "100", Password: "p"}},
	}
	for _, pair := range pairs {
		cfg := Config{Systems: pair[:], MaxItems: defaultMaxItems, RequestsPerSecond: defaultRPS}
		err := cfg.Validate()
		if err == nil {
			t.Errorf("%q and %q cannot be told apart and should be refused",
				pair[0].Name, pair[1].Name)
			continue
		}
		// The rows are named, and both spellings are quoted: "Acme Dental"
		// and "Acme  Dental" look identical once anything renders them, so a
		// message naming only the names tells an operator that two things
		// they cannot tell apart are the same thing.
		for _, want := range []string{"phone system 1", "phone system 2", "a name or alias of its own"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal should say %q, got %v", want, err)
			}
		}
	}
}
