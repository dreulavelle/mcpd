// Package threecx reads a 3CX v20 phone system over its configuration API.
//
// Read-only. The API this talks to can create, change and delete extensions,
// trunks, inbound rules and everything else a phone system is made of, and it
// is somebody's production PBX with their customers' calls on it. So the write
// surface is refused at the transport rather than merely left unimplemented:
// every request is checked against a list of read endpoints, and anything
// that is not a GET to one of them never reaches the network. Adding a write
// later means deliberately widening transport.go, which is the amount of
// friction that decision deserves.
//
// See docs/3cx.md for what the API does that a reader would not expect. Two
// things matter enough to repeat here:
//
// Default projections leak credentials. GET /xapi/v1/Users with no $select
// returns AuthPassword, DeskphonePassword, VMPIN and SIPID for every extension
// -- live SIP credentials and voicemail PINs for the whole business -- and
// SystemStatus returns the licence key. Every read here names its fields with
// $select, and the transport refuses one that does not, so a field that is
// never fetched cannot reach a model even by a later mistake.
//
// The extension password is exchanged for a bearer token that lasts an hour,
// and the token is what travels on every request. The password crosses the
// network once per hour, at sign-in, and never appears anywhere else.
package threecx

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"
)

// Defaults. Each is a judgement about an installation nobody has tuned yet.
const (
	// pageSize is what one upstream page asks for. 3CX refuses any $top above
	// 100 with a 400 naming the limit, so it is a ceiling rather than a
	// preference.
	pageSize = 100

	// defaultMaxItems bounds what a single tool call accumulates. A large
	// PBX has a thousand extensions, and an assistant asking for "all
	// extensions" would otherwise pull more than a context window holds.
	defaultMaxItems = 300

	// defaultRPS bounds outbound calls. A 3CX is one process on one machine,
	// often a small cloud instance, with live calls going through it. The
	// calls matter more than we do, so this is deliberately modest.
	defaultRPS = 5.0

	// defaultTimeout bounds one upstream request. A call history query over
	// a large window can take a few seconds; anything past this is a PBX
	// that is not going to answer.
	defaultTimeout = 30 * time.Second

	// The range an operator may set that to. A large installation answering a
	// wide query genuinely takes longer than thirty seconds sometimes, and the
	// alternative to raising it there was editing a constant; five minutes is
	// past the point where a model waiting on the answer has given up anyway.
	minTimeoutSeconds = 5
	maxTimeoutSeconds = 300

	// tokenMargin is how long before a token's expiry it is treated as
	// expired. A token that runs out mid-request fails in a way that reads as
	// the PBX being down, and a minute costs nothing.
	tokenMargin = time.Minute

	// fallbackTokenLife is assumed when a sign-in answers without expires_in.
	// 3CX issues an hour; half of that is safe in either direction.
	fallbackTokenLife = 30 * time.Minute
)

// apiPrefix is the OData root every configuration read is made under.
const apiPrefix = "/xapi/v1/"

// loginPath is the one POST this integration makes: the exchange of an
// extension's password for a bearer token. It is outside the OData root and is
// the only write the transport permits.
const loginPath = "/webclient/api/Login/GetAccessToken"

// Config is the plugin's own configuration, from the `settings` block.
type Config struct {
	// Systems are the phone systems this instance serves, one row each.
	//
	// The key is still `customers`, because that is what is stored, what a
	// file-provisioned host writes, and what the row still is for everybody
	// whose businesses have one phone system each. A row is a *system* rather
	// than a business because a business can have more than one, and the two
	// were the same record until they could not be.
	Systems []System `yaml:"customers" json:"customers"`

	// MaxItems caps what one tool call accumulates. Reported in the result
	// when it bites, so a caller narrows their filter instead of silently
	// seeing part of a phone system.
	MaxItems int `yaml:"max_items" json:"max_items"`

	// RequestsPerSecond bounds outbound calls to each phone system. Walking
	// pages is a loop, which is the shape most likely to lean on a small PBX.
	RequestsPerSecond float64 `yaml:"requests_per_second" json:"requests_per_second"`

	// TimeoutSeconds bounds a single upstream request. Seconds rather than a
	// duration because it is a number on a form: the settings store hands a
	// duration field back as whole seconds, and a time.Duration decoded from
	// one would be that many nanoseconds.
	TimeoutSeconds int `yaml:"timeout" json:"timeout"`
}

// System is one 3CX installation: one address, one sign-in, one row.
//
// A business owns one or more of them. Customer is what says which business,
// and it is optional precisely so that the common case does not change: a row
// that leaves it empty is its own business, which is what every row meant
// before the field existed and what an MSP with one PBX per client still
// means.
type System struct {
	// Name is what this phone system is called, and the row's identity -- the
	// collection keeps it unique, because a row nobody can name is a row
	// nobody can edit.
	//
	// For a business with one system it is the business's name, which is why
	// the table still reads as a list of customers. For a business with
	// several it names the system -- "Acme HQ", "Acme Branch" -- because the
	// business's name alone cannot say which one a call meant.
	Name string `yaml:"name" json:"name"`
	// Customer is the business this system belongs to, when that is not the
	// name above. Every row of one business spells it the same way; spellings
	// are compared the way names are matched, so case, spacing and the
	// punctuation a sentence leaves on a name do not split a business in two.
	Customer string `yaml:"customer" json:"customer"`
	// Aliases are the other things people call this system -- an
	// abbreviation, a trading name, the site, "server 1" -- so "acme" finds
	// "Acme Dental Group" and "branch" finds the second of two.
	Aliases []string `yaml:"aliases" json:"aliases"`
	// Host is the phone system's web address: the FQDN somebody types to reach
	// its console, such as pbx.example, or that address with https:// in
	// front of it.
	Host string `yaml:"host" json:"host"`
	// Extension is the number, or the email address, this integration signs
	// in as. It needs the system owner role: a normal extension can sign in
	// and see only itself, and every listing here answers 403.
	Extension string `yaml:"extension" json:"extension"`
	// Password is that extension's web client password. Exchanged for a token
	// at sign-in and never sent anywhere else.
	Password string `yaml:"password" json:"password"`
}

// complete reports whether enough was supplied to sign in to this system.
func (s System) complete() bool {
	return strings.TrimSpace(s.Name) != "" && strings.TrimSpace(s.Host) != "" &&
		strings.TrimSpace(s.Extension) != "" && s.Password != ""
}

// names is the system's name and aliases, trimmed and non-empty, for matching.
func (s System) names() []string {
	out := make([]string, 0, 1+len(s.Aliases))
	if n := strings.TrimSpace(s.Name); n != "" {
		out = append(out, n)
	}
	for _, a := range s.Aliases {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

// business is the name of the business this system belongs to: the Customer
// column, or the system's own name when that column is empty.
//
// The fallback is the whole of the backward compatibility. A table filled in
// before this column existed has a business per row, named as the row is
// named, which is exactly what it meant.
func (s System) business() string {
	if c := strings.TrimSpace(s.Customer); c != "" {
		return c
	}
	return strings.TrimSpace(s.Name)
}

// identifier is the token a caller passes to name one thing unambiguously:
// the name, folded to lower case with everything that is not a letter or a
// digit turned into a hyphen.
//
// It exists because a name is prose. "Acme Dental Group" arrives from a model
// quoted, capitalised differently or with a full stop on it; an answer that
// hands back acme-dental-group hands back something that survives being
// repeated. It is derived rather than stored because there is nowhere to store
// it -- the row's identity is its name -- so it is stable exactly as long as
// the name is, and Validate refuses two names that would shorten to one
// identifier.
func identifier(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
		default:
			dash = true
		}
	}
	return b.String()
}

// withDefaults fills anything the operator left alone.
func (c *Config) withDefaults() {
	if c.MaxItems <= 0 {
		c.MaxItems = defaultMaxItems
	}
	if c.RequestsPerSecond <= 0 {
		c.RequestsPerSecond = defaultRPS
	}
	if c.TimeoutSeconds <= 0 {
		c.TimeoutSeconds = int(defaultTimeout / time.Second)
	}
}

// Timeout is how long one upstream request may take.
func (c Config) Timeout() time.Duration {
	if c.TimeoutSeconds <= 0 {
		return defaultTimeout
	}
	return time.Duration(c.TimeoutSeconds) * time.Second
}

// Configured reports whether there is at least one phone system that can be
// signed in to. A row half filled in does not count, and is named by Validate
// rather than silently skipped.
func (c Config) Configured() bool {
	if len(c.Systems) == 0 {
		return false
	}
	for _, s := range c.Systems {
		if !s.complete() {
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
	seenHost := map[string]string{}
	for i, s := range c.Systems {
		label := strings.TrimSpace(s.Name)
		if label == "" {
			return fmt.Errorf("3cx: phone system %d has no name; every row needs one", i+1)
		}
		host := strings.TrimSpace(s.Host)
		if host == "" {
			continue
		}
		u, err := parseHost(host)
		if err != nil {
			return fmt.Errorf("3cx: %s: %w", label, err)
		}
		if u.User != nil {
			return fmt.Errorf("3cx: %s: put the extension and password in their own "+
				"fields, not in the address; a URL with a password in it ends up in logs", label)
		}
		if strings.Contains(u.Path, "/xapi") || strings.Contains(u.Path, "/webclient") {
			return fmt.Errorf("3cx: %s: the address should be the phone system's web "+
				"root, not the API path -- drop everything after the host from %q", label, s.Host)
		}
		if p := strings.Trim(u.Path, "/"); p != "" {
			return fmt.Errorf("3cx: %s: the address %q carries a path; a 3CX is reached "+
				"at the root of its host", label, s.Host)
		}
		// Two rows on one address are one phone system entered twice, whether
		// or not they claim the same business. A business with two systems has
		// two addresses; that is what makes them two.
		if other, taken := seenHost[hostKey(u)]; taken {
			return fmt.Errorf("3cx: %s and %s share the address %s; that is one phone "+
				"system entered twice, so one of them is pointed at the wrong place. A "+
				"business with more than one phone system has a different address for "+
				"each", other, label, u.Host)
		}
		seenHost[hostKey(u)] = label
		if strings.ContainsAny(s.Extension, " \t\r\n") {
			return fmt.Errorf("3cx: %s: the extension %q has whitespace in it", label, s.Extension)
		}
	}
	if err := c.checkNames(); err != nil {
		return err
	}
	if c.MaxItems < 1 {
		return fmt.Errorf("3cx: max_items must be at least 1, got %d", c.MaxItems)
	}
	if c.RequestsPerSecond <= 0 {
		return fmt.Errorf("3cx: requests_per_second must be positive, got %v", c.RequestsPerSecond)
	}
	if c.TimeoutSeconds < minTimeoutSeconds || c.TimeoutSeconds > maxTimeoutSeconds {
		return fmt.Errorf("3cx: timeout must be between %d and %d seconds, got %d",
			minTimeoutSeconds, maxTimeoutSeconds, c.TimeoutSeconds)
	}
	return nil
}

// checkNames refuses a naming that a call could not be resolved against
// without guessing.
//
// Two rules, and the second is the one that changed when a business gained a
// second phone system:
//
//   - within one business, a word names at most one of its systems. Two
//     systems of Acme both answering to "hq" is a call to Acme that cannot be
//     settled.
//   - a word that names a business names nothing else. If "acme" is the
//     business, it cannot also be one system of another business, and it
//     cannot be one of Acme's two systems either -- that second case is the
//     trap a table upgraded in place falls into, where the business is called
//     what its first phone system was called.
//
// What is deliberately *allowed* is the same word on systems of different
// businesses. "Server 1" is what everybody calls their first one, and
// refusing it would make the aliases people actually want unusable. A call
// giving it with a customer resolves inside that customer; a call giving it
// alone is refused at the time with both named, which is the one place the
// ambiguity is real.
func (c Config) checkNames() error {
	businesses := groupSystems(c.Systems)
	of := make(map[int]*business, len(c.Systems))
	for _, b := range businesses {
		for _, row := range b.rows {
			of[row] = b
		}
	}

	type claims struct {
		businesses []*business
		systems    []int
	}
	byWord := map[string]*claims{}
	claim := func(word string, b *business, row int) {
		for _, w := range []string{normaliseName(word), identifier(word)} {
			if w == "" {
				continue
			}
			got := byWord[w]
			if got == nil {
				got = &claims{}
				byWord[w] = got
			}
			switch {
			case b != nil:
				if !slices.Contains(got.businesses, b) {
					got.businesses = append(got.businesses, b)
				}
			default:
				if !slices.Contains(got.systems, row) {
					got.systems = append(got.systems, row)
				}
			}
		}
	}
	// Ordered so the message names things in the order they appear in the
	// table: a map's iteration order would make the same bad table produce a
	// different complaint each time it is saved.
	var words []string
	for _, b := range businesses {
		for _, w := range []string{normaliseName(b.name), identifier(b.name)} {
			if w != "" {
				words = append(words, w)
			}
		}
		claim(b.name, b, -1)
	}
	for i, s := range c.Systems {
		for _, n := range s.names() {
			for _, w := range []string{normaliseName(n), identifier(n)} {
				if w != "" {
					words = append(words, w)
				}
			}
			claim(n, nil, i)
		}
	}

	seen := map[string]bool{}
	for _, word := range words {
		if seen[word] {
			continue
		}
		seen[word] = true
		got := byWord[word]
		for a := 0; a < len(got.systems); a++ {
			for z := a + 1; z < len(got.systems); z++ {
				if of[got.systems[a]] == of[got.systems[z]] {
					return fmt.Errorf("3cx: %s and %s both answer to %q, and they are the "+
						"same business's phone systems, so a call naming it could not be "+
						"resolved without guessing -- give one of them a name or alias of "+
						"its own%s", describeSystem(c.Systems, got.systems[a]),
						describeSystem(c.Systems, got.systems[z]), word, matchingNote)
				}
			}
		}
		for _, b := range got.businesses {
			for _, row := range got.systems {
				if of[row] != b || len(b.rows) > 1 {
					return fmt.Errorf("3cx: the business %q and %s both answer to %q, so a "+
						"call naming it could not be resolved without guessing -- a "+
						"business's name has to be its own%s", b.name,
						describeSystem(c.Systems, row), word, matchingNote)
				}
			}
			for _, other := range got.businesses {
				if other != b {
					return fmt.Errorf("3cx: the businesses %q and %q both answer to %q, so "+
						"a call naming one could not be told from the other -- give them "+
						"names that differ by more than punctuation%s", b.name, other.name,
						word, matchingNote)
				}
			}
		}
	}
	return nil
}

// matchingNote is the sentence every naming refusal ends with. One copy,
// because an operator reading two of them should not have to work out whether
// the difference in wording means a difference in the rule.
const matchingNote = ". Names are matched ignoring case, spacing and punctuation, and " +
	"the identifier a tool answers with is the name with the spaces turned to hyphens, " +
	"so two names that differ only in those are one name here"

// describeSystem names a row the way a message should: the phone system's own
// name and where it sits in the table, because two names that collide under
// the comparison above can look identical on the page.
func describeSystem(rows []System, row int) string {
	return fmt.Sprintf("phone system %d (%q)", row+1, strings.TrimSpace(rows[row].Name))
}

// business is one customer: the businesses are what the rows add up to.
type business struct {
	// name is the business as its first row spells it. First rather than
	// longest or most common, because the table has an order an operator can
	// see, and any other rule would have the displayed name move when an
	// unrelated row was edited.
	name string
	id   string
	// rows are its phone systems, as indexes into the rows it was grouped
	// from, in table order.
	rows []int
}

// groupSystems works out the businesses a set of rows describes.
//
// Rows are grouped by their business name compared the way every other name
// here is compared, so "Acme Dental" and "acme dental." are one business
// rather than two that happen to look alike. Order of first appearance, so the
// answer is the table's order.
func groupSystems(rows []System) []*business {
	var out []*business
	byName := map[string]*business{}
	for i, s := range rows {
		name := s.business()
		if name == "" {
			continue
		}
		folded := normaliseName(name)
		b := byName[folded]
		if b == nil {
			b = &business{name: name, id: identifier(name)}
			byName[folded] = b
			out = append(out, b)
		}
		b.rows = append(b.rows, i)
	}
	return out
}

// hostKey is the address two rows are compared on. The port is part of
// it -- a different port is a different phone system, and the transport checks
// it -- but the default one is dropped, because acme.example and
// acme.example:443 are the same system spelt two ways and would otherwise be
// accepted as two customers on one PBX.
func hostKey(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" || (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		return host
	}
	return host + ":" + port
}

// parseHost reads an address written either way: a bare FQDN, or one with a
// scheme in front of it.
//
// A bare name is the form 3CX itself shows -- the console reports its FQDN,
// not a URL -- and is what an operator has to hand. It is given https because
// every 3CX v20 serves its console and API over TLS; an explicit http:// is
// accepted for an on-premise system somebody reaches without it.
func parseHost(host string) (*url.URL, error) {
	host = strings.TrimSpace(host)
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}
	u, err := url.Parse(host)
	if err != nil {
		return nil, fmt.Errorf("3cx: address %q is not usable: %w", host, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("3cx: address must be http or https, got %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("3cx: address %q names no host", host)
	}
	return u, nil
}

// rootOf returns the address requests are built on: scheme and host, no path,
// no trailing slash. An address Validate has turned down yields the empty
// string, and every request built on it fails at the guard.
func rootOf(host string) string {
	u, err := parseHost(host)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
