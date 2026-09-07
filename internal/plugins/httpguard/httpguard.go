// Package httpguard is the read-only guarantee, enforced once.
//
// Three integrations had grown their own copy of this file, identical but for
// the plugin's name in the error sentences and the prose in the comments. A
// guarantee kept in three places is one that can be widened in one of them
// quietly, which is exactly the wrong property for the last thing standing
// between a tool call and somebody's production estate.
//
// # Why an allow-list rather than a method check
//
// Refusing everything that is not a GET is one line and covers some APIs
// entirely. It cannot cover all of them: search, aggregation and event
// endpoints are POSTs -- a question about a million log lines does not fit in
// a query string -- and those are often most of the reason an integration
// exists. A method check would refuse them, and widening it to "GET, or POST"
// would permit every write in the API in the same breath.
//
// So a request is refused unless its method *and* its path are both named in
// the caller's rule table. Default-deny is the stronger guarantee, not the
// weaker one: adding a tool that reaches a new endpoint means naming that
// endpoint in the table, which is the amount of friction the decision deserves.
//
// # Why patterns rather than string equality
//
// Some endpoints carry an identifier in the path. Anchoring both ends is what
// keeps `^/system/inputs$` from also permitting `/system/inputs/{id}/start`,
// which is a write that looks like a read right up until it starts an input.
package httpguard

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// Rule is one request an integration may make.
type Rule struct {
	Method string
	Path   *regexp.Regexp
	// Why is quoted back when a *different* method is tried on a path that is
	// otherwise known, so "POST /streams is refused" can say what /streams is
	// for rather than only that it is not allowed.
	Why string
}

// Get is a permitted read. The pattern is anchored by the caller and compiled
// at start-up, so a malformed one is a panic on the first line of a test run
// rather than a rule that silently never matches.
func Get(pattern, why string) Rule {
	return Rule{Method: http.MethodGet, Path: regexp.MustCompile(pattern), Why: why}
}

// Post is a permitted read that has to be a POST, because the question does not
// fit in a query string. It is still a read: what makes that true is that the
// path is named here and nowhere else.
func Post(pattern, why string) Rule {
	return Rule{Method: http.MethodPost, Path: regexp.MustCompile(pattern), Why: why}
}

// guard is the last thing every request passes through, and the only place the
// read-only guarantee is actually enforced.
type guard struct {
	base http.RoundTripper
	// name prefixes every refusal, so a message in a log or a tool result says
	// which integration refused it.
	name string
	// prefix is everything before the endpoint's own path. It is not a
	// constant: an installation behind a reverse proxy at /graylog is an
	// ordinary deployment and its requests arrive as /graylog/api/... .
	// Trimming a fixed root would leave those unmatched by every pattern, so a
	// proxied installation would have every request refused by its own guard.
	prefix  string
	allowed []Rule
}

// Client wraps c so every request it makes goes through the guard.
//
// A copy: the host's HTTP client is shared, and a transport that refuses
// everything but a named list of reads belongs to one plugin rather than to
// everything using that client.
//
// prefix is the caller's already-normalised base path, because how it is
// derived is the integration's own business -- one appends "/api" to it and
// the others do not.
func Client(c *http.Client, name, prefix string, allowed []Rule) *http.Client {
	g := guard{name: name, prefix: prefix, allowed: allowed}
	if c == nil {
		return &http.Client{Transport: g, CheckRedirect: dontFollow}
	}
	clone := *c
	g.base = c.Transport
	clone.Transport = g
	clone.CheckRedirect = dontFollow
	return &clone
}

func (g guard) RoundTrip(req *http.Request) (*http.Response, error) {
	// URL.Path, not RawPath: percent-escapes are decoded here, so a path
	// reaching this check by a different spelling is compared in the form the
	// API will actually route on. "/streams/x%2Fstart" is one segment to an
	// anchored pattern written against RawPath and two segments to the server,
	// which is the whole of how an allow-list gets walked past.
	full := Normalise(req.URL.Path)

	// A request that is not under this instance's own base path is refused
	// outright rather than trimmed to something that might match. The only way
	// to produce one is a redirect chased to somewhere else or a bug in how a
	// URL was built, and neither is a thing to let through on the strength of
	// its tail happening to look familiar.
	path, ok := underPrefix(full, g.prefix)
	if !ok {
		return nil, fmt.Errorf(
			"%s: refusing %s %s; it is not under this instance's base path (%s)",
			g.name, req.Method, full, g.prefix)
	}

	var known []string
	for _, r := range g.allowed {
		if !r.Path.MatchString(path) {
			continue
		}
		if r.Method == req.Method {
			return g.roundTrip(req)
		}
		known = append(known, r.Method+" ("+r.Why+")")
	}

	// A path this integration does know, reached with a method it does not.
	// Worth saying separately: it is the shape a bug in a plugin takes, and
	// naming what the path *is* for is what makes it findable.
	if len(known) > 0 {
		return nil, fmt.Errorf(
			"%s: refusing %s %s; this integration only reads, and %s is "+
				"only ever called with %s",
			g.name, req.Method, path, path, strings.Join(known, " or "))
	}
	return nil, fmt.Errorf(
		"%s: refusing %s %s; it is not one of the endpoints this "+
			"integration is permitted to call. Every request is checked "+
			"against an allow-list, so a read this plugin needs has to be "+
			"added to it deliberately",
		g.name, req.Method, path)
}

func (g guard) roundTrip(req *http.Request) (*http.Response, error) {
	base := g.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// underPrefix strips the base path, requiring a whole-segment match.
//
// An empty prefix -- an API living at the root of its host -- accepts every
// path unchanged. A non-empty one has to be followed by a separator: CutPrefix
// alone would accept "/gatewayfoo/devices" against a prefix of "/gateway" and
// hand the allow-list "foo/devices".
func underPrefix(path, prefix string) (string, bool) {
	if prefix == "" || prefix == "/" {
		return path, true
	}
	rest, ok := strings.CutPrefix(path, prefix)
	if !ok {
		return "", false
	}
	if rest == "" {
		// The base path itself. Nothing calls it and no pattern matches it;
		// returning "/" keeps the refusal in the ordinary path above rather
		// than making this function decide.
		return "/", true
	}
	if !strings.HasPrefix(rest, "/") {
		return "", false
	}
	return rest, true
}

// Normalise puts a path into the single form an allow-list is written for, so
// a request cannot arrive past an anchored pattern by spelling. Exported
// because a caller builds its prefix with it before handing it over.
func Normalise(path string) string {
	p := strings.TrimSpace(path)
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
	}
	return p
}

// dontFollow stops the client chasing redirects, so a redirect arrives as a
// redirect rather than as whatever it eventually lands on.
//
// Two reasons, and the second is the one that matters. An installation behind
// an authenticating proxy answers an unauthenticated call with a 302 to a
// sign-in page, and following it turns a diagnosable "your credential was not
// accepted" into an HTML page parsed as JSON. And a redirect is the one thing
// that could carry a request past the guard: the guard runs per request and
// would check the new location too, but a redirect to a different *host* would
// carry the Authorization header somewhere the operator never named. Not
// following at all is simpler than reasoning about which redirects are safe.
func dontFollow(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}
