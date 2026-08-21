package cnmaestro

import (
	"strings"
	"testing"
)

func validConfig() Config {
	return Config{
		BaseURL:      "https://cloud.cambiumnetworks.com",
		ClientID:     "a-client-id",
		ClientSecret: "a-client-secret",
	}
}

func TestConfig_Defaults(t *testing.T) {
	var c Config
	c.withDefaults()

	if c.BaseURL != defaultBaseURL {
		t.Errorf("BaseURL = %q, want the cloud front door", c.BaseURL)
	}
	if c.PageSize != defaultPageSize || c.MaxItems != defaultMaxItems {
		t.Errorf("paging defaults = %d/%d", c.PageSize, c.MaxItems)
	}
	if c.RequestsPerSecond != defaultRPS || c.Timeout != defaultTimeout {
		t.Errorf("rate/timeout defaults = %v/%v", c.RequestsPerSecond, c.Timeout)
	}

	// A trailing slash would produce "//api/v2/..." on every request.
	c2 := Config{BaseURL: "https://example.test/"}
	c2.withDefaults()
	if strings.HasSuffix(c2.BaseURL, "/") {
		t.Errorf("BaseURL = %q, want no trailing slash", c2.BaseURL)
	}
}

// A page size the API will not honour is worse than a smaller one: it fails
// per request rather than at startup.
func TestConfig_PageSizeIsCapped(t *testing.T) {
	c := Config{PageSize: 100000}
	c.withDefaults()
	if c.PageSize != maxAllowedPage {
		t.Errorf("PageSize = %d, want it capped at %d", c.PageSize, maxAllowedPage)
	}
}

func TestConfig_Validate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*Config)
		wantSub string
	}{
		{"base url is a bare host", func(c *Config) { c.BaseURL = "cloud.cambiumnetworks.com" }, "full URL"},
		// The credential manages network infrastructure. There is no
		// deployment where sending it in the clear is the right trade.
		{"plaintext base url", func(c *Config) { c.BaseURL = "http://cloud.cambiumnetworks.com" }, "https"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := validConfig()
			tc.mutate(&c)
			c.withDefaults()

			err := c.Validate()
			if err == nil {
				t.Fatalf("expected a failure")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error = %q, want it to mention %q", err, tc.wantSub)
			}
		})
	}
}

func TestConfig_ValidAccepted(t *testing.T) {
	c := validConfig()
	c.withDefaults()
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// Matching upstream is exact and case-sensitive, and this is the likely typo.
// Left alone it fails at request time with a 404 that reads as "no such
// account" rather than "wrong case".
func TestConfig_CatchesMainAccountMiscased(t *testing.T) {
	c := validConfig()
	c.ManagedAccount = "base infrastructure"
	c.withDefaults()

	err := c.Validate()
	if err == nil {
		t.Fatal("a miscased Main Account name must be refused")
	}
	if !strings.Contains(err.Error(), MainAccount) {
		t.Fatalf("error = %q, want it to show the correct spelling", err)
	}
}

// A real tenant name that merely differs in case from nothing in particular is
// none of our business: only the reserved value is checked.
func TestConfig_AcceptsTenantNames(t *testing.T) {
	for _, name := range []string{MainAccount, "Acme Networks", "acme", ""} {
		c := validConfig()
		c.ManagedAccount = name
		c.withDefaults()
		if err := c.Validate(); err != nil {
			t.Errorf("managed_account %q was refused: %v", name, err)
		}
	}
}

// Credentials are entered in the dashboard, so a plugin without them has to
// mount anyway -- a host that refused to start without them could never be
// opened to enter them. Validate stays silent; Configured is what reports it.
func TestConfig_MissingCredentialsIsNotAValidationFailure(t *testing.T) {
	c := validConfig()
	c.ClientID, c.ClientSecret = "", ""
	c.withDefaults()

	if err := c.Validate(); err != nil {
		t.Fatalf("missing credentials must not fail validation: %v", err)
	}
	if c.Configured() {
		t.Error("Configured must report that credentials are absent")
	}

	c.ClientID, c.ClientSecret = "id", "secret"
	if !c.Configured() {
		t.Error("Configured must report that credentials are present")
	}
	// Half a credential is not a credential.
	c.ClientSecret = ""
	if c.Configured() {
		t.Error("one half of a credential must not count as configured")
	}
}
