// Package flowroute reads Flowroute accounts over their v2 API.
//
// One instance serves many customers. A Flowroute API key belongs to exactly
// one account -- the key-creation screen scopes it to a single account, and a
// parent account's key reads the parent's own inventory rather than its
// children's -- so an MSP with thirty customers on Flowroute has thirty key
// pairs. They are rows of one instance here, so the MSP runs one endpoint, one
// tunnel and one connector, and every tool says which customer it is about.
// The cost is that access is per instance: anyone who reaches it reaches every
// customer on it, so customers that must be kept apart go on separate
// instances.
//
// Read-only. The same API buys and releases numbers, rewrites the inbound
// route a DID rings on, and changes the address emergency services are given
// for it -- so the write surface is refused at the transport rather than
// merely left unimplemented: every request is checked against a list of read
// endpoints, and anything that is not a GET to one of them never reaches the
// network. Adding a write later means deliberately widening transport.go,
// which is the amount of friction that decision deserves against a live
// carrier account.
//
// See docs/flowroute.md for what the API does that a reader would not expect.
// Three things matter enough to repeat here.
//
// Authentication is HTTP Basic, with the access key as the username and the
// secret key as the password. There is no token exchange and nothing expires,
// which means the credential itself travels on every request rather than once
// an hour -- so it is never logged, never put in a URL, and never returned by
// a tool.
//
// Every response is JSON:API: the entity is under `data`, its fields are under
// `data.attributes`, and a listing carries `links.next` when there is another
// page. Related entities arrive in a sibling `included` array rather than
// nested, which is why reading a number's route means looking there rather
// than under the number.
//
// A 404 means two different things and they must not be collapsed. Flowroute
// answers "no such port order" with a JSON:API error whose `status` is the
// number 404 and which carries a `title` and a `detail`; it answers a URL it
// does not serve with an error whose `status` is the *string* "404 Not
// Found: The requested URL was not found on the server…" and nothing else. The
// first is an answer -- the thing is not there -- and the second is a bug in
// how this package built a path. errors.go tells them apart, because reporting
// a mistyped URL as "not found" would send somebody looking for a port order
// that was never missing.
package flowroute

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Defaults. Each is a judgement about an account nobody has tuned yet.
const (
	// defaultBaseURL is Flowroute's API root. Not a setting: there is one
	// production estate and no sandbox, so an address field would be a way to
	// point a credential somewhere it does not belong.
	defaultBaseURL = "https://api.flowroute.com"

	// maxPageSize is the largest limit Flowroute accepts on a listing. A page
	// is asked for at this size or at what is left of the caller's limit,
	// whichever is smaller, and links.next says whether more remain.
	maxPageSize = 200

	// defaultMaxItems bounds what a single tool call accumulates. An account
	// carrying several thousand numbers would otherwise pull more than a
	// context window holds.
	defaultMaxItems = 300

	// defaultRPS bounds outbound calls, per customer. Flowroute publishes no
	// documented rate limit, which is a reason for restraint rather than
	// licence: the account is shared with whatever else the business runs
	// against it.
	defaultRPS = 5.0

	// defaultTimeout bounds one upstream request.
	defaultTimeout = 30 * time.Second

	// accessKeyMaxLen is the longest an access key plausibly is. Flowroute
	// issues eight characters against a thirty-two character secret, so a long
	// value in the access field is almost always the two swapped.
	accessKeyMaxLen = 16
)

// Config is the plugin's own configuration, from the `settings` block.
type Config struct {
	// Customers are the accounts this instance serves, one per business. Every
	// tool takes a customer argument resolved against these by name or alias;
	// an instance with one customer resolves it without being told.
	Customers []Customer `yaml:"customers" json:"customers"`

	// MaxItems caps what one tool call accumulates. Reported in the result
	// when it bites, so a caller narrows their filter instead of silently
	// seeing part of an account.
	MaxItems int `yaml:"max_items" json:"max_items"`

	// RequestsPerSecond bounds outbound calls to each customer's account.
	RequestsPerSecond float64 `yaml:"requests_per_second" json:"requests_per_second"`

	// Timeout bounds a single upstream request.
	Timeout time.Duration `yaml:"timeout" json:"timeout"`

	// BaseURL is the API root. Not a setting -- it is here so a test can point
	// the client at a fixture, and it defaults to Flowroute's own address.
	BaseURL string `yaml:"-" json:"-"`
}

// Customer is one business and the Flowroute account it is billed under.
type Customer struct {
	// Name is what the business is called: what a person types and what the
	// answer names. Unique within the instance.
	Name string `yaml:"name" json:"name"`
	// Aliases are the other things people call it -- an abbreviation, a
	// trading name, the site -- so "acme" finds "Acme Dental Group".
	Aliases []string `yaml:"aliases" json:"aliases"`
	// AccessKey is the account's API access key: the username half.
	AccessKey string `yaml:"access_key" json:"access_key"`
	// SecretKey is the password half. Sent on every request rather than
	// exchanged for a token.
	SecretKey string `yaml:"secret_key" json:"secret_key"`
}

// complete reports whether enough was supplied to read this customer.
func (c Customer) complete() bool {
	return strings.TrimSpace(c.Name) != "" &&
		strings.TrimSpace(c.AccessKey) != "" && c.SecretKey != ""
}

// names is the customer's name and aliases, trimmed and non-empty, for
// matching.
func (c Customer) names() []string {
	out := make([]string, 0, 1+len(c.Aliases))
	if n := strings.TrimSpace(c.Name); n != "" {
		out = append(out, n)
	}
	for _, a := range c.Aliases {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
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
	if strings.TrimSpace(c.BaseURL) == "" {
		c.BaseURL = defaultBaseURL
	}
	c.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
}

// Configured reports whether there is at least one customer that can be read.
// A customer half filled in does not count, and is named by Validate rather
// than silently skipped.
func (c Config) Configured() bool {
	if len(c.Customers) == 0 {
		return false
	}
	for _, cu := range c.Customers {
		if !cu.complete() {
			return false
		}
	}
	return true
}

// Validate rejects a configuration that cannot work.
//
// An unconfigured plugin is not an error: it mounts, its settings form has
// somewhere to live, and Check says what is missing. What is refused here is a
// configuration that is present and wrong, because that fails later, further
// away, and with a worse message.
func (c Config) Validate() error {
	// Keyed by row rather than by label: two customers both called "Acme" have
	// the same label, so comparing labels lets the duplicate through. The row
	// index is what distinguishes "this row's alias repeats its own name",
	// which is harmless, from "two rows answer to one name", which is a call
	// that cannot be resolved without guessing.
	type owner struct {
		row   int
		label string
	}
	seenName := map[string]owner{}
	seenKey := map[string]string{}
	for i, cu := range c.Customers {
		label := strings.TrimSpace(cu.Name)
		if label == "" {
			label = fmt.Sprintf("customer %d", i+1)
		}
		if strings.TrimSpace(cu.Name) == "" {
			return fmt.Errorf("flowroute: %s has no name; every customer needs one", label)
		}
		// A name or alias shared by two customers is a call that cannot be
		// resolved without guessing, and this integration does not guess.
		for _, n := range cu.names() {
			folded := strings.ToLower(n)
			if other, taken := seenName[folded]; taken && other.row != i {
				return fmt.Errorf("flowroute: %q names both %s and %s; a name or alias "+
					"has to point at one customer", n, other.label, label)
			}
			seenName[folded] = owner{row: i, label: label}
		}

		access := strings.TrimSpace(cu.AccessKey)
		if access != cu.AccessKey {
			return fmt.Errorf("flowroute: %s: the access key has whitespace around it", label)
		}
		if strings.TrimSpace(cu.SecretKey) != cu.SecretKey {
			return fmt.Errorf("flowroute: %s: the secret key has whitespace around it", label)
		}
		// The mistake somebody actually makes: Flowroute Manage shows the two
		// values one above the other and they go into the wrong boxes.
		if len(access) > accessKeyMaxLen && strings.TrimSpace(cu.SecretKey) != "" {
			return fmt.Errorf("flowroute: %s: the access key is %d characters, which is "+
				"the length of a secret key -- check the two have not been swapped",
				label, len(access))
		}
		if access == "" {
			continue
		}
		// A key belongs to exactly one Flowroute account, so two customers
		// sharing one means one of them is pointed at the other's account --
		// and every answer about it would be right about the wrong business.
		if other, taken := seenKey[access]; taken {
			return fmt.Errorf("flowroute: %s and %s use the same access key; a key "+
				"belongs to one Flowroute account, so one of them is pointed at the "+
				"wrong one", other, label)
		}
		seenKey[access] = label
	}

	if c.MaxItems < 1 {
		return fmt.Errorf("flowroute: max_items must be at least 1, got %d", c.MaxItems)
	}
	if c.RequestsPerSecond <= 0 {
		return fmt.Errorf("flowroute: requests_per_second must be positive, got %v",
			c.RequestsPerSecond)
	}
	if base := strings.TrimSpace(c.BaseURL); base != "" {
		u, err := url.Parse(base)
		if err != nil {
			return fmt.Errorf("flowroute: the API address %q is not a URL: %w", base, err)
		}
		if u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("flowroute: the API address %q needs a scheme and a host", base)
		}
		if u.User != nil {
			return fmt.Errorf("flowroute: put the access key and secret in their own " +
				"fields, not in the address; a URL with a credential in it ends up in logs")
		}
	}
	return nil
}
