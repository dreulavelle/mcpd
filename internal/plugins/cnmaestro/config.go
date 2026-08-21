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

	// ClientID and ClientSecret are the resolved credential, not a reference
	// to one. Where it came from -- a dashboard field, an env: reference in a
	// file, a systemd credential -- is the host's problem, and asking every
	// plugin to understand each form would be asking each of them to get it
	// right.
	ClientID     string `yaml:"client_id" json:"client_id"`
	ClientSecret string `yaml:"client_secret" json:"client_secret"`

	// ManagedAccount selects the account every request reads from.
	//
	// Either an MSP managed account (tenant) name, or the reserved value
	// "Base Infrastructure" meaning the Main Account. Matching upstream is
	// exact and case-sensitive.
	//
	// Set it. Omitting it is not the same as naming the Main Account: the
	// default depends on whether a request names a network, so GET /devices
	// spans every account while GET /devices?network=X returns Main Account
	// devices alone. Two tool calls that differ only by a filter would
	// otherwise read from different accounts.
	//
	// Objects in the Main Account report managed_account: "" when read back,
	// and that empty string is never valid to send -- sending it is treated as
	// omitting the parameter. The value that selects the Main Account is not
	// the value read off objects in it, which is the trap this comment exists
	// for.
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

	// MainAccount is the reserved value naming the Main Account, as opposed to
	// an MSP tenant. Spelled exactly this way: matching upstream is
	// case-sensitive and "base infrastructure" is rejected.
	MainAccount = "Base Infrastructure"
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

// Validate reports configuration that cannot work.
//
// Credentials are deliberately not checked here. They are entered in the
// dashboard, so a host that refused to start without them could never be
// opened to enter them -- the plugin mounts unconfigured instead, shows its
// form, and says it is not ready. See Configured.
func (c *Config) Validate() error {
	var problems []string

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

	// The single most likely typo, and it fails at request time with a 404
	// that reads as "no such account" rather than "wrong case".
	if acct := strings.TrimSpace(c.ManagedAccount); acct != "" &&
		acct != MainAccount && strings.EqualFold(acct, MainAccount) {
		problems = append(problems, fmt.Sprintf(
			"managed_account is matched exactly and is case-sensitive: write %q, not %q",
			MainAccount, acct))
	}

	if len(problems) > 0 {
		return fmt.Errorf("cnmaestro: %s", strings.Join(problems, "; "))
	}
	return nil
}

// Configured reports whether the credentials needed to reach cnMaestro are
// present. Absent is an ordinary state for a plugin nobody has filled in yet.
func (c *Config) Configured() bool {
	return strings.TrimSpace(c.ClientID) != "" && strings.TrimSpace(c.ClientSecret) != ""
}
