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
	"strings"
	"time"
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
	// Host is the phone system's web address: the FQDN somebody types to reach
	// its console, such as acme.ny.3cx.us, or that address with https:// in
	// front of it. Every customer has their own, so there is no default.
	Host string `yaml:"host" json:"host"`

	// Extension is the number, or the email address, that this integration
	// signs in as. It needs the system owner role: a normal extension can
	// sign in and see only itself, and every listing here answers 403.
	Extension string `yaml:"extension" json:"extension"`

	// Password is that extension's web client password. Exchanged for a token
	// at sign-in and never sent anywhere else.
	Password string `yaml:"password" json:"password"`

	// MaxItems caps what one tool call accumulates. Reported in the result
	// when it bites, so a caller narrows their filter instead of silently
	// seeing part of a phone system.
	MaxItems int `yaml:"max_items" json:"max_items"`

	// RequestsPerSecond bounds outbound calls. Walking pages is a loop, which
	// is the shape most likely to lean on a small PBX.
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

// Configured reports whether enough was supplied to sign in.
//
// All three, because none is useful without the others, which is why this is
// one question rather than three booleans a caller has to combine.
func (c Config) Configured() bool {
	return strings.TrimSpace(c.Host) != "" &&
		strings.TrimSpace(c.Extension) != "" &&
		c.Password != ""
}

// Validate rejects a configuration that cannot work.
//
// An unconfigured plugin is not an error: it mounts, its settings form has
// somewhere to live, and Check says what is missing. What is refused here is a
// configuration that is present and wrong, because that fails later, further
// away, and with a worse message.
func (c Config) Validate() error {
	host := strings.TrimSpace(c.Host)
	if host == "" {
		return nil
	}

	u, err := parseHost(host)
	if err != nil {
		return err
	}
	if u.User != nil {
		return fmt.Errorf("3cx: put the extension and password in their own " +
			"fields, not in the address; a URL with a password in it ends up in logs")
	}
	if strings.Contains(u.Path, "/xapi") || strings.Contains(u.Path, "/webclient") {
		return fmt.Errorf("3cx: the address should be the phone system's web "+
			"root, not the API path -- drop everything after the host from %q", c.Host)
	}
	if p := strings.Trim(u.Path, "/"); p != "" {
		return fmt.Errorf("3cx: the address %q carries a path; a 3CX is reached "+
			"at the root of its host", c.Host)
	}
	if strings.ContainsAny(c.Extension, " \t\r\n") {
		return fmt.Errorf("3cx: the extension %q has whitespace in it", c.Extension)
	}
	if c.MaxItems < 1 {
		return fmt.Errorf("3cx: max_items must be at least 1, got %d", c.MaxItems)
	}
	if c.RequestsPerSecond <= 0 {
		return fmt.Errorf("3cx: requests_per_second must be positive, got %v", c.RequestsPerSecond)
	}
	return nil
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

// root returns the address requests are built on: scheme and host, no path,
// no trailing slash. An address Validate has turned down yields the empty
// string, and every request built on it fails at the guard.
func (c Config) root() string {
	u, err := parseHost(c.Host)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
