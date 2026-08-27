package extremecloudiq

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// guard is the last thing every ExtremeCloud IQ request passes through, and
// the only place the read-only guarantee is actually enforced.
//
// # Why an allow-list, when every read here is a GET
//
// It would be one line to refuse everything that is not a GET, and it would
// cover every call this integration makes. It is still the wrong guard, and
// the size of this API is why: 520 paths, 293 GETs among them, and among those
// are
//
//	GET /account/viq/default-device-password
//	GET /acct-api-token/export
//	GET /packetcaptures/files
//	GET /endusers
//
// -- the default password every device on the estate is onboarded with, every
// API token in the account exported to CSV, captured packets, and the end-user
// directory. Each is a read in the HTTP sense and a credential dump in every
// other. A method check permits all four, and would go on permitting whatever
// the next release adds beside them.
//
// So a request is refused unless its method *and* its path are both named
// below. Default-deny is the stronger guarantee: adding a tool that reaches a
// new endpoint means naming that endpoint here, in front of this comment,
// which is the amount of friction the decision deserves against somebody's
// production wireless estate.
//
// # Why patterns rather than string equality
//
// Several of these carry a device id. Anchoring both ends is what keeps
// `^/devices/[0-9]+$` from also permitting `/devices/{id}/gallery-image`, and
// the id is matched as digits rather than as `[^/]+` so that `/devices/stats`
// and `/devices/digital-twin` are decided by the rules that name them rather
// than by a pattern that was aiming at something else.
type guard struct {
	base http.RoundTripper
	// prefix is the path component of the configured address. Normally empty
	// -- the API lives at the root of api.extremecloudiq.com -- but an
	// installation reached through a gateway that prefixes a path is an
	// ordinary deployment, and trimming a fixed "" would leave its requests
	// unmatched by every pattern below.
	prefix string
}

// rule is one request this integration may make.
type rule struct {
	method string
	path   *regexp.Regexp
	// why is quoted back when a *different* method is tried on a path that is
	// otherwise known, so "POST /devices is refused" can say what /devices is
	// for rather than only that it is not allowed.
	why string
}

// allowed is the complete set of requests this integration may make.
// Everything else is refused before it reaches the network.
//
// Grouped the way the tools are: who the token is, what is deployed, who is
// connected, what has gone wrong, and how the estate is arranged.
//
// It names what is reached and nothing else. An entry for an endpoint no tool
// calls is a permission granted in advance for a read nobody has argued for,
// which is the habit this list exists to prevent -- /locations/site and
// /account/home were both here and both went, because the location tree and
// the token probe already answer what they were for.
var allowed = []rule{
	// Who this token is. The cheapest authenticated call there is, and the
	// startup probe: it names the account, the data centre and the expiry
	// without reading a single row of anybody's estate.
	{http.MethodGet, regexp.MustCompile(`^/auth/apitoken/info$`), "reading who this token belongs to"},

	// What is deployed.
	{http.MethodGet, regexp.MustCompile(`^/devices$`), "listing devices"},
	{http.MethodGet, regexp.MustCompile(`^/devices/stats$`), "counting devices"},
	{http.MethodGet, regexp.MustCompile(`^/devices/[0-9]+$`), "reading one device"},
	{http.MethodGet, regexp.MustCompile(`^/devices/[0-9]+/location$`), "reading where one device is"},
	{http.MethodGet, regexp.MustCompile(`^/devices/[0-9]+/network-policy$`), "reading one device's network policy"},

	// How one device is. The three series a troubleshooting question needs,
	// and nothing that would run a command on it.
	{http.MethodGet, regexp.MustCompile(`^/devices/[0-9]+/alarms$`), "listing one device's alarms"},
	{http.MethodGet, regexp.MustCompile(`^/devices/[0-9]+/history/cpu-mem$`), "reading one device's processor and memory history"},
	{http.MethodGet, regexp.MustCompile(`^/devices/[0-9]+/interfaces/wifi$`), "reading one device's radio statistics"},

	// Who is connected.
	{http.MethodGet, regexp.MustCompile(`^/clients/active$`), "listing connected clients"},
	{http.MethodGet, regexp.MustCompile(`^/clients/active/count$`), "counting connected clients"},
	{http.MethodGet, regexp.MustCompile(`^/clients/summary$`), "summarising connected clients"},

	// What has gone wrong, and who changed something before it did.
	{http.MethodGet, regexp.MustCompile(`^/alerts$`), "listing alerts"},
	{http.MethodGet, regexp.MustCompile(`^/alerts/count-by-(SEVERITY|CATEGORY|ALERT_TYPE)$`), "counting alerts by group"},
	{http.MethodGet, regexp.MustCompile(`^/logs/audit$`), "listing who changed what"},

	// How one client has actually been getting on. The connectivity trail is
	// the only place the API says *why* a client failed rather than that it
	// did: which stage broke, and how long each one took.
	{http.MethodGet, regexp.MustCompile(`^/clients/byMac/[^/]+$`), "finding one client by MAC address"},
	{http.MethodGet, regexp.MustCompile(`^/client-details/overview/info/[0-9]+$`), "reading one client's details"},
	{http.MethodGet, regexp.MustCompile(`^/client-details/client-trail/connectivity-experience/[0-9]+$`), "reading one client's connection attempts"},
	{http.MethodGet, regexp.MustCompile(`^/client-details/client-trail/roaming-trail/grid/[0-9]+$`), "reading one client's roaming history"},
	{http.MethodGet, regexp.MustCompile(`^/d360/device/issues$`), "counting one device's client failures"},

	// The diagnostics grids. These are the POSTs: the filter is a list of site
	// and device ids, which is why they are not GETs.
	{http.MethodPost, regexp.MustCompile(`^/dashboard/wireless/device-health/grid$`), "listing unwell access points"},
	{http.MethodPost, regexp.MustCompile(`^/dashboard/wired/device-health/grid$`), "listing unwell switches"},
	{http.MethodPost, regexp.MustCompile(`^/dashboard/wireless/client-health/grid$`), "listing wireless clients with problems"},
	{http.MethodPost, regexp.MustCompile(`^/dashboard/wired/client-health/grid$`), "listing wired clients with problems"},
	{http.MethodPost, regexp.MustCompile(`^/dashboard/wireless/usage-capacity/grid$`), "listing access points that are saturated"},
	{http.MethodPost, regexp.MustCompile(`^/dashboard/wired/usage-capacity/grid$`), "listing switches that are saturated"},
	{http.MethodPost, regexp.MustCompile(`^/dashboard/sites-with-issues$`), "listing sites with problems"},

	// Scores per site, and the anomaly analysis over the estate.
	{http.MethodGet, regexp.MustCompile(`^/network-scorecard/(networkHealth|clientHealth|deviceHealth|wifiHealth|servicesHealth)/[0-9]+$`), "reading a site's health scores"},
	{http.MethodGet, regexp.MustCompile(`^/copilot/anomalies/anomalies-by-category$`), "counting anomalies by site, severity and kind"},

	// How the estate is arranged.
	{http.MethodGet, regexp.MustCompile(`^/locations/tree$`), "reading the site hierarchy"},
	{http.MethodGet, regexp.MustCompile(`^/network-policies$`), "listing network policies"},
	{http.MethodGet, regexp.MustCompile(`^/network-policies/[0-9]+/ssids$`), "listing one policy's SSIDs"},
	{http.MethodGet, regexp.MustCompile(`^/ssids$`), "listing SSIDs"},
}

func (g guard) RoundTrip(req *http.Request) (*http.Response, error) {
	// URL.Path, not RawPath: percent-escapes are decoded here, so a path
	// reaching this check by a different spelling is compared in the form the
	// API will actually route on. "/devices/1%2Freboot" is one segment to an
	// anchored pattern written against RawPath and two segments to the server,
	// which is the whole of how an allow-list gets walked past.
	full := normalisePath(req.URL.Path)

	// A request that is not under this instance's own API root is refused
	// outright rather than trimmed to something that might match. The only way
	// to produce one is a redirect chased to somewhere else or a bug in how a
	// URL was built, and neither is a thing to let through on the strength of
	// its tail happening to look familiar.
	path, ok := underPrefix(full, g.prefix)
	if !ok {
		return nil, fmt.Errorf(
			"extremecloudiq: refusing %s %s; it is not under this instance's API root (%q)",
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
			"extremecloudiq: refusing %s %s; this integration only reads, and %s "+
				"is only ever called with %s",
			req.Method, path, path, strings.Join(known, " or "))
	}
	return nil, fmt.Errorf(
		"extremecloudiq: refusing %s %s; it is not one of the endpoints this "+
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
// An empty prefix -- the ordinary case, since the API lives at the root of its
// host -- accepts every path unchanged. A non-empty one has to be followed by
// a separator: CutPrefix alone would accept "/gatewayfoo/devices" against a
// prefix of "/gateway" and hand the allow-list "foo/devices".
func underPrefix(path, prefix string) (string, bool) {
	if prefix == "" || prefix == "/" {
		return path, true
	}
	rest, ok := strings.CutPrefix(path, prefix)
	if !ok {
		return "", false
	}
	if rest == "" {
		// The API root itself. Nothing calls it and no pattern matches it;
		// returning "/" keeps the refusal in the ordinary path above rather
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
	prefix := strings.TrimSuffix(strings.TrimSpace(basePath), "/")
	if prefix != "" {
		prefix = normalisePath(prefix)
	}
	g := guard{prefix: prefix}
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
// Two reasons, and the second is the one that matters. An account reached
// through a corporate proxy answers an unauthenticated call with a 302 to a
// sign-in page, and following it turns a diagnosable "your token was not
// accepted" into an HTML page parsed as JSON. And a redirect is the one thing
// that could carry a request past the guard: the guard runs per request and
// would check the new location too, but a redirect to a different *host* would
// carry the bearer token somewhere the operator never named. Not following at
// all is simpler than reasoning about which redirects are safe.
func dontFollow(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}
