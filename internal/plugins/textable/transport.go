package textable

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// guard is the last thing every Textable request passes through, and the only
// place the read-only guarantee is actually enforced.
//
// # Why an allow-list rather than a method check
//
// observium refuses everything that is not a GET, which is one line and covers
// its whole API. It would work here today -- every tool in this package reads,
// and every read it makes is a GET -- and it is still the wrong shape, for two
// reasons that both come from the credential.
//
// A service account token carries explicit scopes, and an operator may well
// grant edit-contacts or edit-organizations alongside the read ones, because
// the same token is useful elsewhere. So this plugin cannot assume its
// credential is powerless; what stops it writing has to be here.
//
// And a method check is one edit away from permitting everything. Widening it
// to "GET, or DELETE" -- which any future deletion work would need -- permits
// DELETE /api/v2/users/{id}, DELETE /api/v2/organizations/{id} and DELETE
// /api/v2/users/{id}/token/{tokenId} in the same breath. None of those is a
// thing this plugin should reach by having been widened once.
//
// So a request is refused unless its method *and* its path are both named
// below. Default-deny is the stronger guarantee, not the weaker one: adding a
// tool that reaches a new endpoint means naming that endpoint here, in front of
// this comment, which is the amount of friction the decision deserves against
// somebody's live messaging platform.
//
// # Why patterns rather than string equality
//
// Several of these carry an identifier in the path, and anchoring both ends is
// what keeps `^/api/v2/users/[^/]+$` from also permitting
// /api/v2/users/{id}/token -- which mints a long-lived credential for that
// user, and which is a POST rather than a GET only until somebody adds a rule
// that is not anchored.
type guard struct {
	base http.RoundTripper
	// prefix is the path component of the configured address. Empty for a
	// Textable at the root of its host, which is what every deployment of it
	// looks like today; carried anyway so that one behind a reverse proxy at a
	// sub-path does not have every request refused by its own guard.
	prefix string
}

// rule is one request this integration may make.
type rule struct {
	method string
	path   *regexp.Regexp
	// why is quoted back when a *different* method is tried on a path that is
	// otherwise known, so a refused DELETE can say what the path is for rather
	// than only that it is not allowed.
	why string
}

// allowed is the complete set of requests this integration may make.
// Everything else is refused before it reaches the network.
//
// Every entry is a GET, and every one is a read a service account is documented
// to be permitted. Grouped by the question a tool asks.
var allowed = []rule{
	// Whether the instance is up at all. Unauthenticated, and the first half of
	// the startup probe: it separates "cannot reach it" from "it did not like
	// the token".
	{http.MethodGet, regexp.MustCompile(`^/health$`), "reading the instance's health"},

	// The tenant listing. Undocumented as a GET -- the specification describes
	// only POST /api/v2/tenants -- and it is the endpoint that makes the rest
	// work, because it is the only source of Textable's internal tenant id.
	{http.MethodGet, regexp.MustCompile(`^/api/v2/tenants$`), "listing tenants"},

	// The billing report, which doubles as the user directory. Spelled the way
	// the API serves it: the specification writes "biling", and that path
	// answers 404 on a live instance.
	{http.MethodGet, regexp.MustCompile(`^/api/v2/billing/tenantReport$`),
		"listing every tenant's users"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/billing/tenantReport/[^/]+$`),
		"reading one tenant's licensing and users"},

	// Organizations, which need a tenant id to list and an id of their own to
	// read.
	{http.MethodGet, regexp.MustCompile(`^/api/v2/organizations$`),
		"listing a tenant's organizations"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/organizations/[^/]+$`),
		"reading one organization"},

	// One contact, by id. There is no user read here: GET /api/v2/users/{id}
	// answers 401 to a service account token whatever the specification says,
	// so naming it would permit a call that cannot work.
	//
	// Anchoring still matters on what remains: /api/v2/users/{id}/token mints a
	// long-lived credential, and an unanchored pattern for any /api/v2/users
	// path would reach it.
	{http.MethodGet, regexp.MustCompile(`^/api/v2/contacts/[^/]+$`), "reading one contact"},
}

func (g guard) RoundTrip(req *http.Request) (*http.Response, error) {
	// URL.Path, not RawPath: percent-escapes are decoded here, so a path
	// reaching this check by a different spelling is compared in the form
	// Textable will actually route on. A contact id carrying an encoded slash
	// is one segment to an anchored pattern written against RawPath and two to
	// the server, which is the whole of how an allow-list gets walked past.
	full := normalisePath(req.URL.Path)

	// A request that is not under this instance's own root is refused outright
	// rather than trimmed to something that might match. The only way to
	// produce one is a redirect chased somewhere else or a bug in how a URL was
	// built, and neither is a thing to let through on the strength of its tail
	// happening to look familiar.
	path, ok := underPrefix(full, g.prefix)
	if !ok {
		return nil, fmt.Errorf(
			"textable: refusing %s %s; it is not under this instance's root (%s)",
			req.Method, full, g.prefix)
	}

	var known []string
	for _, r := range allowed {
		if !r.path.MatchString(path) {
			continue
		}
		if r.method == req.Method {
			return g.roundTrip(req)
		}
		known = append(known, r.method+" ("+r.why+")")
	}

	// A path this integration does know, reached with a method it does not.
	// Worth saying separately: it is the shape a bug in this package takes, and
	// naming what the path is *for* is what makes it findable.
	if len(known) > 0 {
		return nil, fmt.Errorf(
			"textable: refusing %s %s; this integration only reads, and %s is "+
				"only ever called with %s",
			req.Method, path, path, strings.Join(known, " or "))
	}
	return nil, fmt.Errorf(
		"textable: refusing %s %s; it is not one of the endpoints this "+
			"integration is permitted to call. Every request is checked "+
			"against an allow-list, so a read this plugin needs has to be "+
			"added to it deliberately",
		req.Method, path)
}

func (g guard) roundTrip(req *http.Request) (*http.Response, error) {
	base := g.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// underPrefix strips the configured path prefix, requiring a whole-segment
// match.
//
// CutPrefix alone would accept "/txbfoo/api/users" against a prefix of "/txb"
// and hand the allow-list "foo/api/users", so what remains has to begin a new
// segment.
func underPrefix(path, prefix string) (string, bool) {
	if prefix == "" || prefix == "/" {
		return path, true
	}
	rest, ok := strings.CutPrefix(path, prefix)
	if !ok {
		return "", false
	}
	if rest == "" {
		// The root itself. Nothing calls it and no pattern matches it;
		// returning "/" keeps the refusal in the ordinary path below rather
		// than making this function decide.
		return "/", true
	}
	if !strings.HasPrefix(rest, "/") {
		return "", false
	}
	return rest, true
}

// normalisePath puts a path into the single form the allow-list is written
// for, so a request cannot arrive past an anchored pattern by spelling.
func normalisePath(path string) string {
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

// readOnly wraps a client so every request it makes goes through the guard.
//
// A copy: the host's HTTP client is shared, and a transport that refuses
// everything but a named list of reads belongs to this plugin rather than to
// everything using that client.
func readOnly(c *http.Client, basePath string) *http.Client {
	g := guard{prefix: strings.TrimSuffix(normalisePath(basePath), "/")}
	if g.prefix == "/" {
		g.prefix = ""
	}
	if c == nil {
		return &http.Client{Transport: g, CheckRedirect: dontFollow}
	}
	clone := *c
	g.base = c.Transport
	clone.Transport = g
	clone.CheckRedirect = dontFollow
	return &clone
}

// dontFollow stops the client chasing redirects, so a redirect arrives as a
// redirect rather than as whatever it eventually lands on.
//
// Two reasons, and the second is the one that matters. A white-label instance
// behind a gateway answers an unauthenticated call with a redirect to a sign-in
// page, and following it turns a diagnosable "your credential was not accepted"
// into an HTML page parsed as JSON. And a redirect is the one thing that could
// carry a request past the guard: the guard runs per request and would check
// the new location too, but a redirect to a different *host* would carry the
// Authorization header -- which here is a live account credential -- somewhere
// the operator never named.
func dontFollow(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}
