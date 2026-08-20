package cnmaestro

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Config is the cnMaestro plugin's settings, decoded from the host's
// per-plugin configuration block.
type Config struct {
	// BaseURL is the controller. For Cloud this is the regional entry point,
	// https://cloud.cambiumnetworks.com; for On-Premises it is the appliance.
	BaseURL string `yaml:"base_url"`

	// ClientIDRef and ClientSecretRef are secret references, never values.
	ClientIDRef     string `yaml:"client_id_ref"`
	ClientSecretRef string `yaml:"client_secret_ref"`

	// ManagedAccount selects the tenant every request reads from.
	//
	// This is required rather than optional, because omitting it does not mean
	// one thing: the API's default depends on whether a request names a
	// network, so GET /devices spans every tenant while
	// GET /devices?network=X returns only the Main Account. Requiring it makes
	// the scope of every call explicit instead of emergent.
	//
	// Use the literal "Base Infrastructure" for the Main Account. Note that
	// Main Account objects read back as managed_account: "", and that empty
	// string is not valid to send.
	ManagedAccount string `yaml:"managed_account"`

	// RequestsPerSecond bounds outbound calls. cnMaestro documents a 429
	// response but publishes no limit, so this is a self-imposed ceiling
	// rather than a measured one.
	RequestsPerSecond float64 `yaml:"requests_per_second"`
	// Burst allows short bursts above the sustained rate.
	Burst int `yaml:"burst"`

	// PageSize is the per-request limit. No maximum is documented, so this is
	// conservative.
	PageSize int `yaml:"page_size"`
	// MaxPages bounds a paginated read. Without it a single tool call could
	// walk an entire estate and return more than a model can use.
	MaxPages int `yaml:"max_pages"`

	// Timeout bounds a single HTTP request.
	Timeout time.Duration `yaml:"timeout"`

	// InsecureSkipVerify disables TLS verification. On-Premises appliances
	// commonly ship a self-signed certificate; this exists for that case only
	// and is logged loudly at startup.
	InsecureSkipVerify bool `yaml:"insecure_skip_verify"`
}

// MainAccount is the reserved value selecting the Main Account.
//
// It is not the empty string that Main Account objects report in responses:
// sending "" is treated as if the parameter were absent, which silently falls
// back to a default that depends on the request shape.
const MainAccount = "Base Infrastructure"

func (c *Config) withDefaults() {
	if c.RequestsPerSecond <= 0 {
		c.RequestsPerSecond = 5
	}
	if c.Burst <= 0 {
		c.Burst = 10
	}
	if c.PageSize <= 0 {
		c.PageSize = 100
	}
	if c.MaxPages <= 0 {
		c.MaxPages = 20
	}
	if c.Timeout <= 0 {
		c.Timeout = 30 * time.Second
	}
	if c.ManagedAccount == "" {
		c.ManagedAccount = MainAccount
	}
}

// Validate checks the configuration before anything tries to use it.
func (c *Config) Validate() error {
	var problems []string

	if strings.TrimSpace(c.BaseURL) == "" {
		problems = append(problems, "base_url is required")
	} else {
		u, err := url.Parse(c.BaseURL)
		switch {
		case err != nil:
			problems = append(problems, fmt.Sprintf("base_url is not a valid URL: %v", err))
		case u.Scheme != "https":
			// The token and every subsequent bearer credential travel over
			// this connection.
			problems = append(problems, fmt.Sprintf("base_url must use https, got %q", u.Scheme))
		case u.Host == "":
			problems = append(problems, "base_url has no host")
		}
	}

	if strings.TrimSpace(c.ClientIDRef) == "" {
		problems = append(problems, "client_id_ref is required")
	}
	if strings.TrimSpace(c.ClientSecretRef) == "" {
		problems = append(problems, "client_secret_ref is required")
	}
	if c.ManagedAccount == "" {
		problems = append(problems, "managed_account is required; use "+
			strconvQuote(MainAccount)+" for the Main Account")
	}

	if len(problems) > 0 {
		return fmt.Errorf("cnmaestro: configuration is invalid: %s", strings.Join(problems, "; "))
	}
	return nil
}

func strconvQuote(s string) string { return `"` + s + `"` }
