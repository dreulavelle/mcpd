// Package bookstack reads and writes a BookStack knowledge base over its REST
// API.
//
// Unlike the carrier and phone-system integrations, this one changes things.
// That is the point of it -- a knowledge base nobody can add to is a knowledge
// base that goes stale -- and it is why the write surface is built as
// mutations rather than as tools. A mutation is planned, shown in full,
// approved, applied once, and confirmed by re-reading the target; a tool is
// none of those. See docs/bookstack.md.
//
// Two things about the API matter enough to repeat here.
//
// Authentication is a token pair sent as one header: `Authorization: Token
// <id>:<secret>`. There is no exchange and nothing expires unless somebody
// sets an expiry on the token, so the secret travels on every request -- it is
// never logged, never put in a URL, and never returned by a tool.
//
// Deleting content is not destroying it. A book, chapter, page or shelf that
// is deleted goes to the recycle bin, from where it can be restored, which is
// what lets those mutations honestly declare themselves reversible. The
// exceptions are the ones that really do destroy: emptying an item from the
// recycle bin, and deleting a user, a role, a comment, an attachment or an
// image. Those declare Reversible false, which is also what stops a standing
// rule ever approving one on its own.
package bookstack

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Defaults. Each is a judgement about an instance nobody has tuned yet.
const (
	// apiPrefix is the root every request is made under.
	apiPrefix = "/api"

	// pageSize is what one upstream page asks for. BookStack's listings take a
	// count up to 500 and answer with a total, so paging is arithmetic rather
	// than a cursor.
	pageSize = 100

	// maxPageSize is the largest count BookStack accepts on a listing.
	maxPageSize = 500

	// defaultMaxItems bounds what a single tool call accumulates. A knowledge
	// base with ten thousand pages would otherwise pull more than a context
	// window holds.
	defaultMaxItems = 200

	// defaultRPS bounds outbound calls. BookStack ships with a request throttle
	// on the API -- 180 a minute by default -- so this stays under it with room
	// for whatever else is talking to the instance.
	defaultRPS = 2.0

	// defaultTimeout bounds one upstream request. An export of a large book is
	// the slow case; anything past this is an instance that is not going to
	// answer.
	defaultTimeout = 60 * time.Second
)

// Config is the plugin's own configuration, from the `settings` block.
type Config struct {
	// Host is where BookStack is served: a URL, or a bare host with an
	// optional port. One instance, because a knowledge base is one place.
	Host string `yaml:"host" json:"host"`

	// TokenID is the public half of the API token, from a user's profile page
	// under API Tokens.
	TokenID string `yaml:"token_id" json:"token_id"`

	// TokenSecret is the private half, shown once when the token is made.
	TokenSecret string `yaml:"token_secret" json:"token_secret"`

	// MaxItems caps what one tool call accumulates.
	MaxItems int `yaml:"max_items" json:"max_items"`

	// RequestsPerSecond bounds outbound calls.
	RequestsPerSecond float64 `yaml:"requests_per_second" json:"requests_per_second"`

	// Timeout bounds a single upstream request.
	Timeout time.Duration `yaml:"timeout" json:"timeout"`
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
}

// Configured reports whether enough was supplied to reach an instance. Both
// halves of the token, because either alone authenticates nothing.
func (c Config) Configured() bool {
	return strings.TrimSpace(c.Host) != "" &&
		strings.TrimSpace(c.TokenID) != "" && c.TokenSecret != ""
}

// Validate rejects a configuration that cannot work.
//
// An unconfigured plugin is not an error: it mounts, its settings form has
// somewhere to live, and Check says what is missing. What is refused here is a
// configuration that is present and wrong, because that fails later, further
// away, and with a worse message.
func (c Config) Validate() error {
	if strings.TrimSpace(c.TokenID) != c.TokenID {
		return fmt.Errorf("bookstack: the token ID has whitespace around it")
	}
	if strings.TrimSpace(c.TokenSecret) != c.TokenSecret {
		return fmt.Errorf("bookstack: the token secret has whitespace around it")
	}
	// The pair is written `id:secret` in one header, so a colon in either half
	// would split the header somewhere nobody intended.
	if strings.Contains(c.TokenID, ":") || strings.Contains(c.TokenSecret, ":") {
		return fmt.Errorf("bookstack: a token ID or secret cannot contain a colon; " +
			"the two are sent as id:secret in one header. Paste them into their " +
			"own fields rather than together")
	}

	host := strings.TrimSpace(c.Host)
	if host == "" {
		if c.MaxItems >= 1 && c.RequestsPerSecond > 0 {
			return nil
		}
		return c.limits()
	}
	u, err := parseHost(host)
	if err != nil {
		return fmt.Errorf("bookstack: %w", err)
	}
	if u.User != nil {
		return fmt.Errorf("bookstack: put the token ID and secret in their own " +
			"fields, not in the address; a URL with a credential in it ends up in logs")
	}
	if strings.Contains(u.Path, "/api") {
		return fmt.Errorf("bookstack: the address should be where BookStack is "+
			"served, not the API path -- drop everything from /api onward in %q", c.Host)
	}
	return c.limits()
}

// limits checks the numeric settings.
func (c Config) limits() error {
	if c.MaxItems < 1 {
		return fmt.Errorf("bookstack: max_items must be at least 1, got %d", c.MaxItems)
	}
	if c.RequestsPerSecond <= 0 {
		return fmt.Errorf("bookstack: requests_per_second must be positive, got %v",
			c.RequestsPerSecond)
	}
	return nil
}

// parseHost reads an address written any of the ways somebody types one: a
// bare host, a host and port, or either with a scheme in front.
//
// A bare host is assumed to be http rather than https, which is the opposite
// of the assumption the phone-system integration makes and is deliberate:
// BookStack is usually an internal service on a LAN address, and guessing
// https at an instance that only serves http fails with a TLS error that
// reads as a certificate problem rather than as a wrong scheme.
func parseHost(in string) (*url.URL, error) {
	raw := strings.TrimSpace(in)
	if raw == "" {
		return nil, fmt.Errorf("no address")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("the address %q is not a URL: %w", in, err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("the address %q has no host in it", in)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("the address %q uses %q; BookStack is served over "+
			"http or https", in, u.Scheme)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u, nil
}

// root is the address requests are built on: scheme, host and any path
// BookStack is served under, with no trailing slash.
func (c Config) root() string {
	u, err := parseHost(c.Host)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host + u.Path
}
