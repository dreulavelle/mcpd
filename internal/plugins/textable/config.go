// Package textable integrates Textable, a business-SMS platform, over the v2
// REST API, as a service account.
//
// Read-only. Nothing here creates, edits or deletes anything, and the guarantee
// is enforced in transport.go by an allow-list rather than implied by the tools
// that happen to exist.
//
// See docs/textable.md for the whole of it. Five things about this API matter
// enough to repeat where the code is:
//
// # A service account, deliberately, and not a user key
//
// Textable issues two kinds of long-lived credential and they are not
// interchangeable:
//
//   - A *user token*, written accountUid:apiKey, which authenticates as one
//     account. What it may read is that account's own -- contacts, drip
//     campaigns, canned responses -- widening only to the whole instance when
//     the account happens to be an admin.
//   - A *service account token*, which authenticates as itself and carries
//     explicit scopes: read-all-tenants, read-all-users,
//     read-all-organizations, read-contacts, and their editing counterparts.
//
// This integration takes the second. The first was tried and abandoned, and the
// reason is worth recording: a user key is scoped to one account, so a host
// serving several tenants would need one instance per account, and the
// instance-wide questions -- which tenants exist, who is in them -- could not
// be asked at all without an admin key, which reaches everything on the
// instance with no way to say so.
//
// A service account inverts that. It is one credential, its powers are written
// down as scopes rather than inferred from whose account it is, and an operator
// can grant the read scopes and withhold the rest. Grant only read-all-tenants,
// read-all-users, read-all-organizations and read-contacts: the allow-list here
// would refuse a destructive endpoint anyway, but a credential an assistant can
// reach should not carry delete-tenant in the first place.
//
// # The v2 API is addressed by id, and barely lists anything
//
// Every read below is either "one thing by its id" or the tenant report.
// /api/v2 has no user listing and no contact listing at all. The v1 API has
// both, and neither accepts a service account, so they are not options here.
//
// That would leave a caller with tools they can only use if they already know
// an id, which is no use to anybody. What rescues it is the next thing.
//
// # The billing report is the directory
//
// GET /api/v2/biling/tenantReport -- the misspelling is the API's, not a typo
// here -- is a service-account-only endpoint returning every tenant, and inside
// each one every organization, and inside each of those every user with their
// id and email. It is described as a billing report and it is the only
// instance-wide directory this credential has.
//
// So it is the source for list_tenants and list_users both, which is why they
// are cheap together: one upstream call, cached, answering two questions.
// Reaching for a billing endpoint to enumerate users is odd, and it is
// deliberate rather than accidental -- there is nothing else.
//
// # Contacts can be read one at a time and never listed
//
// GET /api/v2/contacts/{id} is the only contact read a service account has, and
// there is no listing to obtain an id from. That is a real hole and it is not
// one this package can close.
//
// It is also not merely an API gap. The v1 listing, which a user key can call,
// does not complete on a large account: measured against a production tenant of
// over a million contacts it answers 408 from Textable's own thirty-second
// limit, every time, and no pagination parameter is honoured. So there is no
// contact enumeration anywhere in this API for an account of any size, by any
// credential. See docs/textable.md.
//
// # Errors carry a reference code, and arrive in two shapes
//
// The documented envelope is {_errType, message, referenceCode, reason}, where
// referenceCode is unique to that one failure and is the only string somebody
// on a support call can quote. There is a second, undocumented one --
// {"errors":["User must be admin to access this endpoint."]} -- returned as a
// 400 where the same refusal elsewhere is a 403. Both are read.
package textable

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Defaults. Each is a judgement about an installation nobody has tuned yet.
const (
	// defaultMaxItems bounds every listing.
	defaultMaxItems = 200

	// defaultRPS bounds outbound calls. Textable is somebody's live messaging
	// platform and the tenants' own staff are using it while this reads, so
	// this is deliberately modest.
	defaultRPS = 5.0

	// defaultTimeout bounds one upstream request.
	//
	// Deliberately longer than Textable's own thirty-second limit. The two used
	// to be equal, which meant a slow endpoint raced them: sometimes Textable
	// answered 408 with a reference code and a message, and sometimes this
	// client gave up first and produced "context deadline exceeded", which
	// names nothing and cannot be quoted to anybody. Losing that race on
	// purpose is worth ten seconds -- the far end's diagnosis is always better
	// than our own timeout.
	defaultTimeout = 40 * time.Second

	// defaultCacheTTL is how long a read may be answered from memory.
	//
	// It matters more here than in most integrations because of one endpoint.
	// The tenant report is the directory behind both list_tenants and
	// list_users, so a model working through an instance calls it repeatedly,
	// and it is the most expensive read this integration makes -- it walks
	// every tenant, organization and user on the instance. Five minutes is a
	// judgement that who exists changes slowly.
	defaultCacheTTL = 5 * time.Minute
)

// Config is the plugin's own configuration, from the `settings` block.
type Config struct {
	// BaseURL is the instance root -- https://<project-id>.textable.app for a
	// white-label deployment, https://api.textable.app for retail. Paths carry
	// their own /api, so this is an origin rather than an API root.
	BaseURL string `yaml:"base_url" json:"base_url"`

	// APIKey is a Textable service account token.
	//
	// Sent as an ordinary bearer credential. Not the accountUid:apiKey pair a
	// *user* token takes -- that is a different scheme for a different kind of
	// credential, and this integration does not accept one.
	APIKey string `yaml:"api_key" json:"api_key"`

	// MaxItems caps every listing. Reported in the result when it bites.
	MaxItems int `yaml:"max_items" json:"max_items"`

	// RequestsPerSecond bounds outbound calls.
	RequestsPerSecond float64 `yaml:"requests_per_second" json:"requests_per_second"`

	// Timeout bounds a single upstream request.
	Timeout time.Duration `yaml:"timeout" json:"timeout"`

	// CacheSeconds is how long a read may be answered from memory. Zero fetches
	// every time.
	CacheSeconds int `yaml:"cache_seconds" json:"cache_seconds"`
}

// withDefaults fills anything the operator left alone.
func (c *Config) withDefaults() {
	if c.MaxItems <= 0 {
		c.MaxItems = defaultMaxItems
	}
	if c.RequestsPerSecond <= 0 {
		c.RequestsPerSecond = defaultRPS
	}
	if c.Timeout <= 0 {
		c.Timeout = defaultTimeout
	}
	// Zero is a real choice here -- it means never reuse an answer -- so the
	// default only applies to a config that never mentioned it.
	if c.CacheSeconds == 0 {
		c.CacheSeconds = int(defaultCacheTTL / time.Second)
	}
}

// Configured reports whether enough was supplied to reach Textable.
func (c Config) Configured() bool {
	return c.BaseURL != "" && c.APIKey != ""
}

// CacheTTL turns the operator's seconds into a duration.
func (c Config) CacheTTL() time.Duration {
	return time.Duration(c.CacheSeconds) * time.Second
}

// Validate rejects a configuration that cannot work.
//
// An unconfigured plugin is not an error: it mounts, its settings form has
// somewhere to live, and Check says what is missing. What is refused here is a
// configuration that is present and wrong, because that fails later, further
// away, and with a worse message.
func (c Config) Validate() error {
	if c.BaseURL != "" {
		if err := c.validateAddress(); err != nil {
			return err
		}
	}
	if c.APIKey != "" {
		if err := validateKey(c.APIKey); err != nil {
			return err
		}
	}
	if c.MaxItems < 1 {
		return fmt.Errorf("textable: max_items must be at least 1, got %d", c.MaxItems)
	}
	if c.RequestsPerSecond <= 0 {
		return fmt.Errorf("textable: requests_per_second must be positive, got %v",
			c.RequestsPerSecond)
	}
	if c.CacheSeconds < 0 {
		return fmt.Errorf("textable: cache_seconds cannot be negative")
	}
	return nil
}

// validateKey refuses a credential that is plainly the wrong kind.
//
// The one mistake worth catching in advance is a *user* token pasted here.
// Those are written accountUid:apiKey, so a colon in the middle with content
// either side is a recognisable shape, and Textable's answer to sending one on
// a service-account route is a 401 saying "Invalid API Credentials" -- the same
// message a revoked token gets, with nothing distinguishing them.
//
// Nothing else is checked. A service token is an opaque string and this package
// does not know what it looks like; guessing at a format would refuse a valid
// credential the day Textable changes how it mints them.
func validateKey(key string) error {
	uid, secret, ok := strings.Cut(strings.TrimSpace(key), ":")
	if ok && strings.TrimSpace(uid) != "" && strings.TrimSpace(secret) != "" {
		return fmt.Errorf("textable: that looks like a *user* token, which is " +
			"written accountUid:apiKey. This integration authenticates as a " +
			"service account, whose token is a single opaque string with no " +
			"colon in it, and the v2 endpoints it reads do not accept a user " +
			"token. Create a service account in Textable and grant it the read " +
			"scopes")
	}
	return nil
}

// validateAddress refuses an address that will not build a working URL.
func (c Config) validateAddress() error {
	u, err := url.Parse(strings.TrimSpace(c.BaseURL))
	if err != nil {
		return fmt.Errorf("textable: address %q is not a URL: %w", c.BaseURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("textable: address must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("textable: address %q names no host", c.BaseURL)
	}
	// Paths here carry their own /api, so an address that already ends in one
	// would build /api/api/v2/users. Compared segment by segment rather than
	// with Contains, so a deployment served under a path containing the letters
	// is not refused for spelling.
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "api" {
			return fmt.Errorf("textable: address should be the instance root, "+
				"not the API path -- drop the /api from %q", c.BaseURL)
		}
	}
	if u.User != nil {
		return fmt.Errorf("textable: put the token in the API key field, not in " +
			"the address; a URL with a credential in it ends up in logs")
	}
	return nil
}

// root returns the base URL with any trailing slash removed, so paths can be
// concatenated without producing a double separator.
func (c Config) root() string {
	return strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
}
