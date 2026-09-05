package flowroute

import (
	"strings"
	"testing"
)

func TestConfigDefaults(t *testing.T) {
	t.Parallel()
	var c Config
	c.withDefaults()

	if c.MaxItems != defaultMaxItems || c.RequestsPerSecond != defaultRPS ||
		c.Timeout != defaultTimeout {
		t.Fatalf("defaults not applied: %+v", c)
	}
	if c.BaseURL != defaultBaseURL {
		t.Fatalf("base URL is %q", c.BaseURL)
	}
}

// A trailing slash on the address would produce //v2/numbers, which the guard
// would then refuse for a reason that has nothing to do with the mistake.
func TestConfigTrimsTheAddress(t *testing.T) {
	t.Parallel()
	c := Config{BaseURL: "  https://api.flowroute.com/  "}
	c.withDefaults()
	if c.BaseURL != "https://api.flowroute.com" {
		t.Fatalf("base URL is %q", c.BaseURL)
	}
}

func TestConfigured(t *testing.T) {
	t.Parallel()
	full := Customer{Name: "Acme", AccessKey: "a1b2c3d4", SecretKey: "s"}
	cases := []struct {
		name   string
		cfg    Config
		wantOK bool
	}{
		{"one complete customer", Config{Customers: []Customer{full}}, true},
		{"no customers", Config{}, false},
		// Either half of the credential alone authenticates nothing: Basic auth
		// carries them together, so a half-filled row is a 401 that reads as a
		// wrong credential rather than an incomplete one.
		{"access key only", Config{Customers: []Customer{
			{Name: "Acme", AccessKey: "a1b2c3d4"}}}, false},
		{"secret only", Config{Customers: []Customer{
			{Name: "Acme", SecretKey: "s"}}}, false},
		{"no name", Config{Customers: []Customer{
			{AccessKey: "a1b2c3d4", SecretKey: "s"}}}, false},
		// One good row does not excuse a half-filled one; it would mount and
		// then be missing from every answer without saying so.
		{"one complete and one not", Config{Customers: []Customer{
			full, {Name: "Beta", AccessKey: "b1b2c3d4"}}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.Configured(); got != tc.wantOK {
				t.Fatalf("Configured() = %v, want %v", got, tc.wantOK)
			}
		})
	}
}

func TestValidateRefusesWhatCannotWork(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cfg  Config
		says string
	}{
		{
			name: "a customer with no name",
			cfg: Config{Customers: []Customer{
				{AccessKey: "a1b2c3d4", SecretKey: "s"}}},
			says: "has no name",
		},
		{
			// Resolution would have to guess, and this integration does not.
			name: "two customers sharing a name",
			cfg: Config{Customers: []Customer{
				{Name: "Acme", AccessKey: "a1b2c3d4", SecretKey: "s"},
				{Name: "Acme", AccessKey: "b1b2c3d4", SecretKey: "s"}}},
			says: "has to point at one customer",
		},
		{
			name: "an alias that names another customer",
			cfg: Config{Customers: []Customer{
				{Name: "Acme", AccessKey: "a1b2c3d4", SecretKey: "s"},
				{Name: "Beta", Aliases: []string{"ACME"}, AccessKey: "b1b2c3d4", SecretKey: "s"}}},
			says: "has to point at one customer",
		},
		{
			// A key belongs to exactly one Flowroute account, so two customers
			// sharing one means every answer about one of them is right about
			// the wrong business.
			name: "two customers sharing an access key",
			cfg: Config{Customers: []Customer{
				{Name: "Acme", AccessKey: "a1b2c3d4", SecretKey: "s"},
				{Name: "Beta", AccessKey: "a1b2c3d4", SecretKey: "s"}}},
			says: "pointed at the wrong one",
		},
		{
			// The mistake somebody actually makes: Flowroute Manage shows the
			// two values one above the other and they go into the wrong boxes.
			name: "the keys swapped",
			cfg: Config{Customers: []Customer{
				{Name: "Acme", AccessKey: "0123456789abcdef0123456789abcdef", SecretKey: "a1b2c3d4"}}},
			says: "swapped",
		},
		{
			name: "whitespace around the access key",
			cfg: Config{Customers: []Customer{
				{Name: "Acme", AccessKey: " a1b2c3d4", SecretKey: "s"}}},
			says: "whitespace",
		},
		{
			name: "a credential in the address",
			cfg: Config{
				Customers: []Customer{{Name: "Acme", AccessKey: "a1b2c3d4", SecretKey: "s"}},
				BaseURL:   "https://a:b@api.flowroute.com"},
			says: "ends up in logs",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg
			cfg.MaxItems = defaultMaxItems
			cfg.RequestsPerSecond = defaultRPS
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("should have been refused")
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Fatalf("refusal should mention %q, said %q", tc.says, err)
			}
		})
	}
}

// An unconfigured instance is not a broken one: it mounts so its settings form
// has somewhere to live.
func TestValidateAcceptsAnEmptyConfiguration(t *testing.T) {
	t.Parallel()
	var c Config
	c.withDefaults()
	if err := c.Validate(); err != nil {
		t.Fatalf("an empty configuration should be allowed: %v", err)
	}
}

// The credentials must not survive on the config the plugin holds, so that a
// dump of it -- a log line, an error, the settings page -- cannot carry one.
func TestNewDoesNotKeepTheCredentialsOnTheConfig(t *testing.T) {
	t.Parallel()
	p, err := New(testDeps(), Config{
		Customers: []Customer{
			{Name: "Acme", AccessKey: testAccessKey, SecretKey: testSecretKey},
			{Name: "Beta", AccessKey: "b1b2c3d4", SecretKey: "another-secret"},
		},
		BaseURL: "https://api.flowroute.com",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, cu := range p.cfg.Customers {
		if cu.AccessKey != "" || cu.SecretKey != "" {
			t.Fatalf("%s still carries its credential on the plugin's config", cu.Name)
		}
	}
	// Each customer's client keeps its own, and they must not have been
	// crossed: one client holding another customer's key would answer
	// confidently about the wrong business.
	if p.accounts[0].client.secretKey != testSecretKey {
		t.Fatal("the first customer's client lost its credential")
	}
	if p.accounts[1].client.secretKey != "another-secret" {
		t.Fatal("the second customer's client has the wrong credential")
	}
}
