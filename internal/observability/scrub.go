package observability

import (
	"regexp"
	"strings"
)

// Scrubbing exists because mcpd runs on somebody else's hardware.
//
// A crash report is the one thing this process sends to a server the operator
// does not control, and the text in it was written for an operator reading
// their own logs -- so it names upstream addresses, device hostnames, database
// names and the occasional query. All of that is fine in a log file on the
// customer's own machine and none of it should leave it.
//
// Go helps more than most runtimes here: a panic's stack trace carries
// function names and line numbers but not argument values, so the leak surface
// is the *strings* -- the panic value, error messages, and anything added as
// context. Those are what this file removes.
//
// The rule is redact rather than drop. "could not reach [address]" still says
// which code path failed and how, which is the whole reason to send a report;
// "could not reach observium.acme-hospital.internal" says who the customer is.

var (
	// A URL's credentials, which are the worst thing that can be in one.
	urlCredentials = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/\s@]+@`)
	// A query string, which is where tokens travel when somebody puts one there.
	urlQuery = regexp.MustCompile(`\?[^\s"']+`)
	// The host of any URL. It names the customer's infrastructure, and the
	// scheme and path are the parts that say what the code was doing.
	urlHost = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/\s"']+`)
	// Addresses, in both families. The v6 pattern is deliberately loose: a
	// false positive costs a redacted string and a false negative costs a
	// customer's network topology.
	ipv4 = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b(:\d{1,5})?`)
	ipv6 = regexp.MustCompile(`\b(?:[0-9A-Fa-f]{1,4}:){2,7}[0-9A-Fa-f]{1,4}\b`)
	// A MAC address identifies one piece of equipment.
	mac = regexp.MustCompile(`\b(?:[0-9A-Fa-f]{2}[:-]){5}[0-9A-Fa-f]{2}\b`)
	// Anything shaped like an address a person is reachable at.
	email = regexp.MustCompile(`\b[\w.+-]+@[\w-]+\.[\w.-]+\b`)
	// A long unbroken run of token characters. Every API key, session token,
	// bearer credential and password hash this project handles looks like
	// this, and nothing a human wrote does. The floor is high enough that a
	// long function name or a Go type path does not trip it.
	tokenish = regexp.MustCompile(`\b[A-Za-z0-9_\-]{28,}\b`)
	// A dotted name that is not a URL: a bare hostname in a message.
	// Restricted to names with a recognisable multi-label shape so that
	// "sentry.Init" and "config.yaml" are left alone.
	hostname = regexp.MustCompile(`\b[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?){2,}\b`)
)

// goFileOrPackage keeps the scrubber off the things a stack trace is made of.
//
// Without it, hostname matching eats "github.com/spoked/mcpd/internal/plugins"
// and "observium/mysql.go", and a report with its identifiers removed is a
// report nobody can act on. Redacting a customer's estate is the point;
// redacting our own source tree is a bug.
var goFileOrPackage = regexp.MustCompile(`\b(?:[\w.-]+/)+[\w.-]+\.go\b|\bgithub\.com/[\w.-]+/[\w.-]+|\b\w+\.(?:go|yaml|yml|json|sql|db|toml|md)\b`)

// Scrub removes what should not leave the customer's machine.
//
// Order matters. Credentials go first, because a URL's userinfo would
// otherwise be swallowed by the host rule and reported as redacted when what
// was redacted was the address rather than the password. Source paths are
// protected before hostnames, because a Go import path and a fully qualified
// domain name are the same shape.
func Scrub(s string) string {
	if s == "" {
		return s
	}

	// Park anything that looks like our own source so the later rules cannot
	// reach it, then put it back at the end.
	var parked []string
	s = goFileOrPackage.ReplaceAllStringFunc(s, func(m string) string {
		parked = append(parked, m)
		return "\x00" + itoa(len(parked)-1) + "\x00"
	})

	s = urlCredentials.ReplaceAllString(s, "$1[redacted]@")
	s = urlQuery.ReplaceAllString(s, "?[redacted]")
	s = urlHost.ReplaceAllString(s, "$1[host]")
	s = email.ReplaceAllString(s, "[email]")
	s = mac.ReplaceAllString(s, "[mac]")
	s = ipv4.ReplaceAllString(s, "[ip]")
	s = ipv6.ReplaceAllString(s, "[ip]")
	s = tokenish.ReplaceAllString(s, "[redacted]")
	s = hostname.ReplaceAllString(s, "[host]")

	for i, original := range parked {
		s = strings.ReplaceAll(s, "\x00"+itoa(i)+"\x00", original)
	}
	return s
}

// itoa avoids pulling strconv in for one call on a hot-ish path.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}
