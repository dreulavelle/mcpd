package graylog

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// guard is the last thing every Graylog request passes through, and the only
// place the read-only guarantee is actually enforced.
//
// # Why an allow-list rather than a method check
//
// observium refuses everything that is not a GET, which is one line and
// covers its whole API. It cannot work here. Graylog's search, aggregation and
// event endpoints are POSTs -- a question about a million log lines does not
// fit in a query string -- and those three are most of the reason this
// integration exists. A method check would refuse them, and widening it to
// "GET, or POST" would permit every write in the API in the same breath.
//
// So a request is refused unless its method *and* its path are both named
// below. Default-deny is the stronger guarantee, not the weaker one: adding a
// tool that reaches a new endpoint means naming that endpoint here, in front
// of this comment, which is the amount of friction the decision deserves
// against somebody's production log cluster.
//
// It also removes the need for the separate deny-list observium carries. That
// list exists to survive its guard being widened from "GET only" to something
// broader; there is nothing equivalent to survive here, because widening this
// guard *is* naming a path. A second list would be a second thing to keep in
// step, and the one that drifted would be the one nobody was reading.
//
// # Why patterns rather than string equality
//
// Two of these carry an identifier in the path. Anchoring both ends is what
// keeps `^/system/inputs$` from also permitting `/system/inputs/{id}/start`,
// which is a write that looks like a read right up until it starts an input.
type guard struct {
	base http.RoundTripper
	// prefix is everything before the endpoint's own path: the path component
	// of the configured address, plus /api. It is not a constant, because
	// Graylog behind a reverse proxy at /graylog is an ordinary deployment and
	// its requests arrive as /graylog/api/search/messages. Trimming a fixed
	// "/api" would leave that unmatched by every pattern below, so a proxied
	// installation would have every single request refused by its own guard.
	prefix string
}

// rule is one request this integration may make.
type rule struct {
	method string
	path   *regexp.Regexp
	// why is quoted back when a *different* method is tried on a path that is
	// otherwise known, so "POST /streams is refused" can say what /streams is
	// for rather than only that it is not allowed.
	why string
}

// allowed is the complete set of requests this integration may make, under
// /api. Everything else is refused before it reaches the network.
//
// Grouped the way the tools are: what a question is asked with, then what the
// answer needs to be understood.
var allowed = []rule{
	// The scripting API. These are the reads that have to be POSTs.
	{http.MethodPost, regexp.MustCompile(`^/search/messages$`), "running a search"},
	{http.MethodPost, regexp.MustCompile(`^/search/aggregate$`), "running an aggregation"},
	{http.MethodPost, regexp.MustCompile(`^/events/search$`), "searching events and alerts"},

	// Field types. GET is every field in the system; POST is the same
	// question narrowed to a set of streams, which on a large installation is
	// the difference between a usable answer and ten thousand names.
	//
	// Anchored, deliberately: /views/fields/poll is a POST that triggers a
	// cluster-wide refresh of the field type cache, and it must not be
	// reachable by a tool that means to ask what fields exist.
	{http.MethodGet, regexp.MustCompile(`^/views/fields$`), "listing field names"},
	{http.MethodPost, regexp.MustCompile(`^/views/fields$`), "listing field names for streams"},

	// What this Graylog is: version, node id, lifecycle. The cheapest
	// authenticated call there is, which is what makes it the startup probe.
	{http.MethodGet, regexp.MustCompile(`^/system$`), "reading the server's version and state"},

	// How the installation is arranged.
	{http.MethodGet, regexp.MustCompile(`^/streams/paginated$`), "listing streams"},
	{http.MethodGet, regexp.MustCompile(`^/streams/[^/]+$`), "reading one stream"},
	{http.MethodGet, regexp.MustCompile(`^/events/definitions$`), "listing event definitions"},
	{http.MethodGet, regexp.MustCompile(`^/events/definitions/[^/]+$`), "reading one event definition"},

	// How the installation is. /cluster is the node overview; the indexer
	// health is the one that says whether messages are being written at all.
	{http.MethodGet, regexp.MustCompile(`^/cluster$`), "reading the node overview"},
	{http.MethodGet, regexp.MustCompile(`^/system/cluster/nodes$`), "listing nodes"},
	{http.MethodGet, regexp.MustCompile(`^/system/indexer/cluster/health$`), "reading indexer health"},
	{http.MethodGet, regexp.MustCompile(`^/system/inputs$`), "listing inputs"},
	{http.MethodGet, regexp.MustCompile(`^/system/notifications$`), "listing system notifications"},
	{http.MethodGet, regexp.MustCompile(`^/system/indices/index_sets$`), "listing index sets"},
}

func (g guard) RoundTrip(req *http.Request) (*http.Response, error) {
	// URL.Path, not RawPath: percent-escapes are decoded here, so a path
	// reaching this check by a different spelling is compared in the form
	// Graylog will actually route on. "/streams/x%2Fstart" is one segment to
	// an anchored pattern written against RawPath and two segments to the
	// server, which is the whole of how an allow-list gets walked past.
	full := normalisePath(req.URL.Path)

	// A request that is not under this instance's own API root is refused
	// outright rather than trimmed to something that might match. The only way
	// to produce one is a redirect chased to somewhere else or a bug in how a
	// URL was built, and neither is a thing to let through on the strength of
	// its tail happening to look familiar.
	path, ok := underPrefix(full, g.prefix)
	if !ok {
		return nil, fmt.Errorf(
			"graylog: refusing %s %s; it is not under this instance's API root (%s)",
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
	// Worth saying separately: it is the shape a bug in this package takes,
	// and naming what the path *is* for is what makes it findable.
	if len(known) > 0 {
		return nil, fmt.Errorf(
			"graylog: refusing %s %s; this integration only reads, and %s is "+
				"only ever called with %s",
			req.Method, path, path, strings.Join(known, " or "))
	}
	return nil, fmt.Errorf(
		"graylog: refusing %s %s; it is not one of the endpoints this "+
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

// underPrefix strips the API root, requiring a whole-segment match.
//
// CutPrefix alone would accept "/apifoo/search/messages" against a prefix of
// "/api" and hand the allow-list "foo/search/messages", so what remains has to
// begin a new segment.
func underPrefix(path, prefix string) (string, bool) {
	rest, ok := strings.CutPrefix(path, prefix)
	if !ok {
		return "", false
	}
	if rest == "" {
		// The API root itself. Nothing calls it and no pattern matches it;
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
// basePath is the path component of the configured address -- empty for a
// Graylog at the root of its host, "/graylog" for one behind a reverse proxy.
//
// A copy: the host's HTTP client is shared, and a transport that refuses
// everything but a named list of reads belongs to this plugin rather than to
// everything using that client.
func readOnly(c *http.Client, basePath string) *http.Client {
	g := guard{prefix: normalisePath(strings.TrimSuffix(basePath, "/") + apiPrefix)}
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
// Two reasons, and the second is the one that matters. A Graylog behind an
// authenticating proxy answers an unauthenticated call with a 302 to a sign-in
// page, and following it turns a diagnosable "your credential was not
// accepted" into an HTML page parsed as JSON. And a redirect is the one thing
// that could carry a request past the guard: the guard runs per request and
// would check the new location too, but a redirect to a different *host*
// would carry the Authorization header somewhere the operator never named.
// Not following at all is simpler than reasoning about which redirects are
// safe.
func dontFollow(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}
