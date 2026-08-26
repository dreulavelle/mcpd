// Package graylog integrates Graylog, a log management platform, over its REST
// API at /api.
//
// Read-only. Nothing here creates, edits or deletes anything in Graylog, and
// the guarantee is enforced in transport.go rather than merely implied by the
// tools that happen to exist.
//
// See docs/graylog.md for the whole of it. Five things about this API matter
// enough to repeat where the code is:
//
// # A read is a POST
//
// The endpoints this integration exists for -- /search/messages,
// /search/aggregate and /events/search -- are POSTs. A question about a
// million log lines does not fit in a query string, so Graylog takes it in a
// body. That means the shape observium uses, a transport that refuses every
// method but GET, would refuse every call worth making here.
//
// So the guard is an allow-list: a request is refused unless its method and
// path are both named in transport.go. Default-deny is stricter than
// method-deny, not weaker, and it is the only form that can say yes to
// POST /api/search/messages without also saying yes to
// POST /api/system/inputs.
//
// # Graylog refuses a POST that does not say who asked
//
// Every non-GET wants an X-Requested-By header. It is Graylog's CSRF guard:
// a browser cannot set a custom header cross-origin, so requiring one proves
// the request came from something that meant to make it. Without it the API
// answers 400 with a message about the header, which reads as a malformed
// request rather than a missing one. It is set on every request here.
//
// # A token is a username
//
// Graylog access tokens authenticate as HTTP basic auth with the token in the
// username field and the literal string "token" as the password. Not a bearer
// token, despite looking exactly like one. Getting this wrong produces a 401
// that says nothing about which half was wrong.
//
// # Results are columns, not records
//
// The scripting API answers with a schema and rows of bare values:
//
//	{"schema":[{"field":"source"},{"field":"took_ms"}],
//	 "datarows":[["a.example",50],["b.example",93]]}
//
// This integration keeps that shape rather than zipping it into a list of
// objects. Fifty log lines with every field name repeated on every row is the
// same information at several times the context, and context is the budget a
// tool result is actually spending. The tool descriptions say so, because a
// model handed columns it was not told about will read the first row as field
// names.
//
// # A query with no time range reads the whole cluster
//
// Graylog will happily scan every index a token can see. That is somebody's
// production log cluster and the search is synchronous, so an unbounded query
// is not a large answer, it is a slow one for everybody. Every tool here sends
// a time range whether the caller named one or not, defaulting to a window
// measured in minutes, and an operator can set a ceiling past which no search
// may reach.
package graylog

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// apiPrefix is the root every call is made under. Graylog serves its API under
// /api on the same port as the web interface, so the address an operator
// configures is the one they type into a browser.
const apiPrefix = "/api"

// Defaults. Each is a judgement about an installation nobody has tuned yet.
const (
	// defaultMaxMessages bounds how many log lines one search returns. Graylog
	// itself defaults to ten. Fifty is enough to see a pattern and small
	// enough that a wrong query costs a paragraph rather than a context
	// window -- a log message is not a database row, it can be kilobytes on
	// its own, and a hundred of them is most of what a conversation has.
	defaultMaxMessages = 50

	// defaultMaxItems bounds every other listing: aggregation rows, streams,
	// events, event definitions, field names. These are small, uniform rows,
	// so the ceiling can be far higher than the one on messages.
	defaultMaxItems = 200

	// defaultRangeSeconds is the window a search covers when the caller names
	// none. Fifteen minutes is the answer to "what is happening", which is the
	// question somebody asks when they do not say. Anything wider is a
	// decision, and a decision should be made rather than defaulted into.
	defaultRangeSeconds = 900

	// defaultRPS bounds outbound calls. A Graylog search is a fan-out across
	// every index in range and is the most expensive thing this integration
	// can ask for, so this is deliberately modest.
	defaultRPS = 5.0

	// defaultTimeout bounds one upstream request. Longer than most
	// integrations get: a search over a wide range on a busy cluster is slow
	// rather than broken, and a client that gives up at ten seconds turns a
	// working query into an error nobody can explain.
	defaultTimeout = 60 * time.Second

	// defaultCacheTTL is how long a read of how Graylog is *arranged* may be
	// answered from memory -- streams, event definitions, field names, inputs.
	// These change when somebody changes them, not as messages arrive.
	//
	// Searches, aggregations and events are never cached, whatever this says.
	defaultCacheTTL = 5 * time.Minute
)

// Config is the plugin's own configuration, from the `settings` block.
type Config struct {
	// BaseURL is the Graylog web root -- the address someone types to reach
	// the UI, without /api. Self-hosted, so there is no default that could be
	// right.
	BaseURL string `yaml:"base_url" json:"base_url"`

	// Token is a Graylog access token, from the user's own token page.
	// Preferred over a username and password: it carries only the permissions
	// of the account that made it, it has a TTL, and revoking it does not
	// change anybody's login.
	//
	// It is presented as the *username* of a basic-auth pair with the literal
	// password "token". That is Graylog's scheme, not a mistake here.
	Token string `yaml:"token" json:"token"`

	// Username and Password are ordinary basic auth against a Graylog account.
	// Supported because a token has a TTL and an integration whose credential
	// expires in thirty days is one somebody has to remember to feed, but a
	// token is still the better answer for anything long-lived.
	//
	// Token wins when both are set.
	Username string `yaml:"username" json:"username"`
	Password string `yaml:"password" json:"password"`

	// MaxMessages caps how many log lines one search returns. Reported in the
	// result when it bites, so a caller narrows their query rather than
	// silently seeing the first page of an incident.
	MaxMessages int `yaml:"max_messages" json:"max_messages"`

	// MaxItems caps every other listing: aggregation rows, events, streams,
	// event definitions, field names.
	MaxItems int `yaml:"max_items" json:"max_items"`

	// DefaultRangeSeconds is the window a search covers when the caller names
	// none.
	DefaultRangeSeconds int `yaml:"default_range_seconds" json:"default_range_seconds"`

	// MaxRangeSeconds is the furthest back any search may reach. Zero is no
	// ceiling, which is the default: an operator who wants one has a reason,
	// and guessing that reason for them would refuse the incident review that
	// is the second most common thing this integration is for.
	MaxRangeSeconds int `yaml:"max_range_seconds" json:"max_range_seconds"`

	// RequestsPerSecond bounds outbound calls.
	RequestsPerSecond float64 `yaml:"requests_per_second" json:"requests_per_second"`

	// Timeout bounds a single upstream request.
	Timeout time.Duration `yaml:"timeout" json:"timeout"`

	// CacheSeconds is how long a read of how Graylog is arranged may be
	// answered from memory. Zero fetches every time. It never applies to a
	// search, an aggregation or an event listing.
	CacheSeconds int `yaml:"cache_seconds" json:"cache_seconds"`
}

// withDefaults fills anything the operator left alone.
func (c *Config) withDefaults() {
	if c.MaxMessages <= 0 {
		c.MaxMessages = defaultMaxMessages
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

// Configured reports whether enough was supplied to reach Graylog.
//
// An address alone is not enough and neither is a credential alone, which is
// why this is one question rather than two booleans a caller has to combine.
func (c Config) Configured() bool {
	return c.BaseURL != "" && (c.Token != "" || (c.Username != "" && c.Password != ""))
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

	if c.Token == "" && (c.Username == "") != (c.Password == "") {
		return fmt.Errorf("graylog: a username needs a password and a password " +
			"needs a username; or use an access token instead of both")
	}
	if c.MaxMessages < 1 {
		return fmt.Errorf("graylog: max_messages must be at least 1, got %d", c.MaxMessages)
	}
	if c.MaxItems < 1 {
		return fmt.Errorf("graylog: max_items must be at least 1, got %d", c.MaxItems)
	}
	if c.DefaultRangeSeconds < 1 {
		return fmt.Errorf("graylog: default_range_seconds must be at least 1, got %d",
			c.DefaultRangeSeconds)
	}
	if c.MaxRangeSeconds < 0 {
		return fmt.Errorf("graylog: max_range_seconds cannot be negative, got %d",
			c.MaxRangeSeconds)
	}
	// A ceiling below the default would refuse every search nobody narrowed,
	// which is a configuration that looks fine and answers nothing.
	if c.MaxRangeSeconds > 0 && c.MaxRangeSeconds < c.DefaultRangeSeconds {
		return fmt.Errorf("graylog: max_range_seconds (%d) is below "+
			"default_range_seconds (%d), so every search that does not name a "+
			"window would be refused", c.MaxRangeSeconds, c.DefaultRangeSeconds)
	}
	if c.RequestsPerSecond <= 0 {
		return fmt.Errorf("graylog: requests_per_second must be positive, got %v",
			c.RequestsPerSecond)
	}
	if c.CacheSeconds < 0 {
		return fmt.Errorf("graylog: cache_seconds cannot be negative")
	}
	return nil
}

// validateAddress refuses an address that will not build a working URL.
func (c Config) validateAddress() error {
	u, err := url.Parse(strings.TrimSpace(c.BaseURL))
	if err != nil {
		return fmt.Errorf("graylog: address %q is not a URL: %w", c.BaseURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("graylog: address must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("graylog: address %q names no host", c.BaseURL)
	}
	// The API root is appended, so an address that already carries it would
	// produce /api/api/search/messages. Saying so beats a 404 from a path
	// nobody meant to build.
	//
	// Compared against the path's segments rather than with Contains: a
	// Graylog behind a reverse proxy at /graylog-api is a legitimate address
	// whose path merely contains the letters.
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "api" {
			return fmt.Errorf("graylog: address should be the web root, not the "+
				"API path -- drop the %s from %q", apiPrefix, c.BaseURL)
		}
	}
	if u.User != nil {
		return fmt.Errorf("graylog: put credentials in the token or username " +
			"and password fields, not in the address; a URL with a password in " +
			"it ends up in logs")
	}
	return nil
}

// root returns the base URL with any trailing slash removed, so paths can be
// concatenated without producing a double separator.
func (c Config) root() string {
	return strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
}
