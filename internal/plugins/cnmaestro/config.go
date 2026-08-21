// Package cnmaestro integrates Cambium's cnMaestro, the controller for their
// Wi-Fi and fixed-wireless estates.
//
// Read-only for now. The API's write surface includes endpoints that run
// arbitrary commands on network hardware, reachable with the same account-wide
// token as every read, so the deny-list in denylist.go is enforced from the
// start rather than added alongside the first mutation.
//
// See docs/cnmaestro.md for what the API does that a reader would not expect:
// regional sharding announced in the token response, two pagination schemes
// depending on the endpoint, and managed_account's meaning by absence.
package cnmaestro

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Config is the plugin's own configuration, from the `settings` block.
type Config struct {
	// BaseURL is where tokens are obtained. Cloud accounts are regionally
	// sharded and the token response names the host that data calls must
	// target, so this is the front door rather than the final address.
	BaseURL string `yaml:"base_url" json:"base_url"`

	// ClientIDRef and ClientSecretRef name secrets, never values. Both come
	// from Download Credentials in cnMaestro's API Clients page.
	ClientIDRef     string `yaml:"client_id_ref" json:"client_id_ref"`
	ClientSecretRef string `yaml:"client_secret_ref" json:"client_secret_ref"`

	// ManagedAccount scopes every request to one managed account.
	//
	// Sent on every request when set. Omitting it means different things
	// depending on whether a request names a network, which makes "leave it
	// off unless needed" a rule with an exception nobody remembers.
	ManagedAccount string `yaml:"managed_account" json:"managed_account"`

	// PageSize is how many items a page asks for. It bounds one response, not
	// the total: a listing walks pages until the collection is exhausted.
	PageSize int `yaml:"page_size" json:"page_size"`

	// MaxItems caps how many items a single tool call will accumulate.
	//
	// A model asking for "all devices" on a large estate would otherwise pull
	// an unbounded amount into a context window that cannot hold it, slowly.
	// The cap is reported in the result so the caller can narrow instead of
	// silently seeing part of an estate.
	MaxItems int `yaml:"max_items" json:"max_items"`

	// RequestsPerSecond bounds outbound calls. Listing walks pages in a loop,
	// which is the shape most likely to trip an upstream rate limit.
	RequestsPerSecond float64 `yaml:"requests_per_second" json:"requests_per_second"`

	// Timeout bounds a single upstream request.
	Timeout time.Duration `yaml:"timeout" json:"timeout"`
}

const (
	defaultBaseURL   = "https://cloud.cambiumnetworks.com"
	defaultPageSize  = 100
	defaultMaxItems  = 1000
	defaultRPS       = 5
	defaultTimeout   = 30 * time.Second
	maxAllowedPage   = 1000
	tokenPath        = "/api/v2/access/token"
	apiPrefix        = "/api/v2"
	managedAccountKV = "managed_account"
)

// withDefaults fills anything the operator left unset.
func (c *Config) withDefaults() {
	if strings.TrimSpace(c.BaseURL) == "" {
		c.BaseURL = defaultBaseURL
	}
	c.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if c.PageSize <= 0 {
		c.PageSize = defaultPageSize
	}
	if c.PageSize > maxAllowedPage {
		c.PageSize = maxAllowedPage
	}
	if c.MaxItems <= 0 {
		c.MaxItems = defaultMaxItems
	}
	if c.RequestsPerSecond <= 0 {
		c.RequestsPerSecond = defaultRPS
	}
	if c.Timeout <= 0 {
		c.Timeout = defaultTimeout
	}
}

// Validate reports configuration that cannot work, before anything starts.
func (c *Config) Validate() error {
	var problems []string

	if strings.TrimSpace(c.ClientIDRef) == "" {
		problems = append(problems, "client_id_ref is required")
	}
	if strings.TrimSpace(c.ClientSecretRef) == "" {
		problems = append(problems, "client_secret_ref is required")
	}
	// A URL rather than a host, because the token request is built from it and
	// a bare hostname produces a request to a relative path that fails much
	// later and much less clearly.
	if u, err := url.Parse(c.BaseURL); err != nil || u.Scheme == "" || u.Host == "" {
		problems = append(problems,
			fmt.Sprintf("base_url must be a full URL like %s (got %q)", defaultBaseURL, c.BaseURL))
	} else if u.Scheme != "https" {
		// The credential is a bearer token for an account that manages network
		// infrastructure. There is no deployment where sending it in the clear
		// is the right trade.
		problems = append(problems, "base_url must use https")
	}

	if len(problems) > 0 {
		return fmt.Errorf("cnmaestro: %s", strings.Join(problems, "; "))
	}
	return nil
}
