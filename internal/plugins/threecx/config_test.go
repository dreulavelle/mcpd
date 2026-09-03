package threecx

import (
	"strings"
	"testing"
)

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
		cfg := Config{Host: c.host, Extension: "100", Password: "p"}
		cfg.withDefaults()
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
		if got := cfg.root(); got != c.root {
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

// All three of address, extension and password are needed; any two is not a
// configuration that can sign in.
func TestConfig_ConfiguredNeedsAllThree(t *testing.T) {
	full := Config{Host: "acme.ny.3cx.us", Extension: "100", Password: "p"}
	if !full.Configured() {
		t.Error("all three supplied should be configured")
	}
	for name, partial := range map[string]Config{
		"no host":      {Extension: "100", Password: "p"},
		"no extension": {Host: "acme.ny.3cx.us", Password: "p"},
		"no password":  {Host: "acme.ny.3cx.us", Extension: "100"},
	} {
		if partial.Configured() {
			t.Errorf("%s should not be configured", name)
		}
	}
}

// The plugin built from settings keeps no password on its config, so a dump
// of the config -- a log line, the settings page -- cannot carry it.
func TestNew_DoesNotRetainThePassword(t *testing.T) {
	p, err := New(testDeps(), Config{Host: "acme.ny.3cx.us", Extension: "100", Password: "secret-pass"})
	if err != nil {
		t.Fatal(err)
	}
	if p.cfg.Password != "" {
		t.Error("the plugin's config still holds the password")
	}
	if p.client.password != "secret-pass" {
		t.Error("the client should hold the password, and only the client")
	}
}
