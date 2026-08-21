package cnmaestro

import (
	"strings"
	"testing"
)

func validConfig() Config {
	return Config{
		BaseURL:         "https://cloud.cambiumnetworks.com",
		ClientIDRef:     "env:MCPD_CNMAESTRO_CLIENT_ID",
		ClientSecretRef: "env:MCPD_CNMAESTRO_CLIENT_SECRET",
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
		{"no client id", func(c *Config) { c.ClientIDRef = "" }, "client_id_ref"},
		{"no client secret", func(c *Config) { c.ClientSecretRef = "" }, "client_secret_ref"},
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
