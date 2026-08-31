// Package extremecloudiq integrates ExtremeCloud IQ, Extreme Networks' cloud
// management platform, over its REST API at api.extremecloudiq.com.
//
// Read-only. Nothing here creates, edits or deletes anything, and the
// guarantee is enforced in transport.go rather than merely implied by the
// tools that happen to exist.
//
// See docs/extremecloudiq.md for the whole of it. Five things about this API
// matter enough to repeat where the code is:
//
// # One address, whichever region the account is in
//
// ExtremeCloud IQ shards accounts across regional data centres, and a token
// belongs to one of them. Unlike cnMaestro -- where authenticating against the
// front door and then reading from it silently returns the wrong shard's
// nothing -- the API endpoint is regionless: api.extremecloudiq.com routes to
// wherever the account lives. So there is one address, it has a default that
// is right for everybody, and the data centre is reported at startup rather
// than configured.
//
// # A read is a GET, and that is not enough on its own
//
// Every read this integration makes is a GET, so a method check would cover
// it. The guard is still an allow-list, for the reason the size of this API
// makes plain: 520 paths carrying 683 operations, of which 293 are GETs,
// and among those are
// /account/viq/default-device-password, /acct-api-token/export and
// /packetcaptures/files. Those are reads in the HTTP sense and credential
// dumps in every other. A method check would permit all three.
//
// So a request is refused unless its method and path are both named in
// transport.go. Reaching a new endpoint means naming it there, which is the
// amount of friction the decision deserves against somebody's production
// wireless estate.
//
// # Time is epoch milliseconds, and it is mandatory
//
// Alerts, audit logs, device alarms and every history series take startTime
// and endTime as required query parameters in milliseconds since the epoch.
// There is no "recent" and no default: a call that omits them is a 400. So
// every tool here that reaches one of those endpoints sends a window whether
// the caller named one or not, and says in its result which window it sent --
// a count with no window is a number with no unit.
//
// # Fields are chosen, not returned
//
// A device carries forty fields and a client carries fifty-six. Asking for all
// of them is one query parameter away and would put a single page of a hundred
// devices well past what a conversation can hold. The API's own answer is
// views: BASIC, DETAIL, STATUS, LOCATION and so on, each a named subset. This
// integration exposes that choice rather than hiding it, and defaults to the
// narrow end.
//
// # A page is a hundred, and an estate is not
//
// Every collection is paginated at a hundred rows, and a large estate is tens
// of pages. Listing walks them in a loop until the caller's limit or the
// operator's ceiling stops it, which is the shape most likely to trip an
// upstream rate limit -- hence the modest default requests-per-second and a
// truncation note that says what to narrow rather than leaving a caller to
// conclude the estate is small.
package extremecloudiq

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// defaultBaseURL is Extreme's regionless API endpoint. It resolves to whichever
// regional data centre holds the account, so it is right for every tenant and
// an operator should not normally change it.
const defaultBaseURL = "https://api.extremecloudiq.com"

// Defaults. Each is a judgement about an estate nobody has tuned yet.
const (
	// defaultMaxItems bounds how many rows one listing returns. A device row
	// in the default view is a dozen short fields, so a couple of hundred of
	// them is a readable answer rather than a context window -- and an estate
	// larger than that is one where the question needs a filter, not a bigger
	// ceiling.
	defaultMaxItems = 200

	// defaultPageSize is what one upstream request asks for. The API's own
	// maximum on most collections, because the cost of a page is the round
	// trip rather than the rows in it: asking for ten would be five times the
	// requests for the same answer, against an API that rate-limits.
	defaultPageSize = 100

	// defaultRangeSeconds is the window an alert or log listing covers when
	// the caller names none. Twenty-four hours, because the question somebody
	// asks without naming a window is "what has gone wrong today" -- and
	// unlike a log search, an alert listing over a day is tens of rows rather
	// than thousands.
	defaultRangeSeconds = 86400

	// defaultRPS bounds outbound calls. Deliberately modest: ExtremeCloud IQ
	// meters API calls per account per hour, and a walk of forty pages issued
	// as fast as the network allows is the thing most likely to spend
	// somebody's quota on one question.
	defaultRPS = 5.0

	// defaultTimeout bounds one upstream request. A listing over a large
	// estate is slow rather than broken.
	defaultTimeout = 45 * time.Second

	// defaultCacheTTL is how long a read of how the estate is *arranged* may
	// be answered from memory -- sites, buildings, floors, network policies,
	// SSIDs. These change when somebody changes them.
	//
	// Devices, clients, alerts, stats and health are never cached, whatever
	// this says.
	defaultCacheTTL = 10 * time.Minute

	// tokenExpiryWarning is how close to expiry a token gets before startup
	// says so. ExtremeCloud IQ issues API tokens with an expiry chosen at
	// creation, and an expired one is refused with the same 401 as a revoked
	// one -- so the useful moment to mention it is before it happens.
	tokenExpiryWarning = 14 * 24 * time.Hour
)

// Config is the plugin's own configuration, from the `settings` block.
type Config struct {
	// BaseURL is the API endpoint. Defaulted, and almost never changed: the
	// address is regionless, so there is no per-tenant value to get right.
	BaseURL string `yaml:"base_url" json:"base_url"`

	// APIToken is a Platform ONE API key, made under your profile's API keys
	// at extremeplatformone.com, and is the only credential this integration
	// takes. It is sent as a plain bearer token; nothing is exchanged for it.
	//
	// Deliberately not the ExtremeCloud IQ page called API Token Management.
	// That one issues for the v1 API, retired for most tenants in January
	// 2024, and following it is a dead end that reads like a permissions
	// problem. See docs/extremecloudiq.md.
	//
	// An account's own username and password would authenticate too, via
	// POST /login, and are deliberately not offered. Three reasons, and the
	// third is the one that decides it: a password carries everything that
	// account can do rather than what a token was scoped to; revoking it
	// changes somebody's login; and a tenant that signs in through an identity
	// provider has no password that /login would accept, so offering the field
	// would be offering a credential that cannot work.
	APIToken string `yaml:"api_token" json:"api_token"`

	// MaxItems caps how many rows one listing returns. Reported in the result
	// when it bites, so a caller narrows their question rather than silently
	// seeing the first slice of an estate.
	MaxItems int `yaml:"max_items" json:"max_items"`

	// DefaultRangeSeconds is the window an alert or log listing covers when
	// the caller names none.
	DefaultRangeSeconds int `yaml:"default_range_seconds" json:"default_range_seconds"`

	// MaxRangeSeconds is the furthest back any windowed read may reach. Zero
	// is no ceiling, which is the default: reviewing last month's incident is
	// a legitimate thing to ask for, and refusing it by default would be
	// guessing at a policy nobody stated.
	MaxRangeSeconds int `yaml:"max_range_seconds" json:"max_range_seconds"`

	// RequestsPerSecond bounds outbound calls.
	RequestsPerSecond float64 `yaml:"requests_per_second" json:"requests_per_second"`

	// Timeout bounds a single upstream request.
	Timeout time.Duration `yaml:"timeout" json:"timeout"`

	// CacheSeconds is how long a read of how the estate is arranged may be
	// answered from memory. Zero fetches every time. It never applies to
	// devices, clients, alerts or anything describing how the estate is *now*.
	CacheSeconds int `yaml:"cache_seconds" json:"cache_seconds"`
}

// withDefaults fills anything the operator left alone.
func (c *Config) withDefaults() {
	if strings.TrimSpace(c.BaseURL) == "" {
		c.BaseURL = defaultBaseURL
	}
	if c.MaxItems <= 0 {
		c.MaxItems = defaultMaxItems
	}
	if c.DefaultRangeSeconds <= 0 {
		c.DefaultRangeSeconds = defaultRangeSeconds
	}
	if c.RequestsPerSecond <= 0 {
		c.RequestsPerSecond = defaultRPS
	}
	if c.Timeout <= 0 {
		c.Timeout = defaultTimeout
	}
	// Zero is a real choice here -- it means never reuse an answer -- so the
	// default only applies to a config that never mentioned it. A negative
	// value is what Validate refuses.
	if c.CacheSeconds == 0 {
		c.CacheSeconds = int(defaultCacheTTL / time.Second)
	}
}

// Configured reports whether enough was supplied to reach ExtremeCloud IQ.
//
// Only the token: the address has a default that is right for every tenant, so
// unlike a self-hosted integration there is nothing else an operator has to
// supply before this can work.
func (c Config) Configured() bool {
	return strings.TrimSpace(c.APIToken) != ""
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
	if err := c.validateAddress(); err != nil {
		return err
	}
	if c.MaxItems < 1 {
		return fmt.Errorf("extremecloudiq: max_items must be at least 1, got %d", c.MaxItems)
	}
	if c.DefaultRangeSeconds < 1 {
		return fmt.Errorf("extremecloudiq: default_range_seconds must be at least 1, got %d",
			c.DefaultRangeSeconds)
	}
	if c.MaxRangeSeconds < 0 {
		return fmt.Errorf("extremecloudiq: max_range_seconds cannot be negative, got %d",
			c.MaxRangeSeconds)
	}
	// A ceiling below the default would refuse every listing nobody narrowed,
	// which is a configuration that looks fine and answers nothing.
	if c.MaxRangeSeconds > 0 && c.MaxRangeSeconds < c.DefaultRangeSeconds {
		return fmt.Errorf("extremecloudiq: max_range_seconds (%d) is below "+
			"default_range_seconds (%d), so every listing that does not name a "+
			"window would be refused", c.MaxRangeSeconds, c.DefaultRangeSeconds)
	}
	if c.RequestsPerSecond <= 0 {
		return fmt.Errorf("extremecloudiq: requests_per_second must be positive, got %v",
			c.RequestsPerSecond)
	}
	if c.CacheSeconds < 0 {
		return fmt.Errorf("extremecloudiq: cache_seconds cannot be negative")
	}
	return nil
}

// validateAddress refuses an address that will not build a working URL.
func (c Config) validateAddress() error {
	raw := strings.TrimSpace(c.BaseURL)
	if raw == "" {
		// withDefaults has not run, which is only possible for a Config built
		// in a test. Treated as the default rather than as an error, so the
		// zero value is usable.
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("extremecloudiq: address %q is not a URL: %w", c.BaseURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("extremecloudiq: address must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("extremecloudiq: address %q names no host", c.BaseURL)
	}
	if u.User != nil {
		return fmt.Errorf("extremecloudiq: put the API token in the token field, " +
			"not in the address; a URL with a credential in it ends up in logs")
	}
	return nil
}

// root returns the base URL with any trailing slash removed, so paths can be
// concatenated without producing a double separator.
func (c Config) root() string {
	raw := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if raw == "" {
		return defaultBaseURL
	}
	return raw
}

// pageSize is how many rows one upstream request asks for.
//
// The endpoint's own maximum decides the page, because the cost of a page is
// the round trip rather than the rows in it -- audit rows are short and that
// endpoint permits five hundred, so asking for a hundred there would be five
// times the requests for the same answer against an API that meters them.
//
// What the caller wants then lowers it: asking for a hundred to satisfy a
// limit of five is ninety-five rows nobody reads, decoded and discarded.
func pageSize(want, endpointMax int) int {
	size := defaultPageSize
	if endpointMax > 0 {
		size = endpointMax
	}
	if want > 0 && want < size {
		size = want
	}
	if size < 1 {
		size = 1
	}
	return size
}
