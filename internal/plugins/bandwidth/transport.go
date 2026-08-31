package bandwidth

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// rule is one request this integration may make.
type rule struct {
	method string
	path   *regexp.Regexp
	why    string
}

// allowed is the complete set of requests this integration may make.
//
// This list is what makes the integration read-only, and it is the only thing
// that does. Bandwidth's roles are not split into read and write: "Campaign
// management" grants creating a campaign as well as reading one, "Ordering"
// grants placing an order, and there is no role that grants looking without
// touching. So a credential scoped for the reads below can also write, and the
// guarantee has to live here rather than in the credential.
//
// Two consequences worth stating. Adding a read means adding a line, on
// purpose. And a bug that builds the wrong URL is refused rather than sent.
var allowed = []rule{
	// The credential itself. The startup probe, and the cheapest authenticated
	// call there is: it reads no part of anybody's estate.
	{http.MethodPost, regexp.MustCompile(`^/api/v1/oauth2/token$`), "exchanging the credential for a token"},

	// What has been happening on the voice side.
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/calls$`), "listing calls"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/calls/[^/]+$`), "reading one call"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/calls/[^/]+/recordings$`), "listing one call's recordings"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/calls/[^/]+/recordings/[^/]+$`), "reading one recording"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/calls/[^/]+/recordings/[^/]+/transcription$`), "reading one recording's transcription"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/calls/[^/]+/transcriptions$`), "listing one call's live transcriptions"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/calls/[^/]+/transcriptions/[^/]+$`), "reading one live transcription"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/recordings$`), "listing the account's recordings"},

	// Conferences.
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/conferences$`), "listing conferences"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/conferences/[^/]+$`), "reading one conference"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/conferences/[^/]+/members/[^/]+$`), "reading one conference member"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/conferences/[^/]+/recordings$`), "listing one conference's recordings"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/conferences/[^/]+/recordings/[^/]+$`), "reading one conference recording"},

	// How much of the account's voice capacity is in use.
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/statistics$`), "reading account call statistics"},

	// Messaging. Note the different noun in the path: Bandwidth's messaging
	// API says users where the voice API says accounts, for the same id.
	{http.MethodGet, regexp.MustCompile(`^/api/v2/users/[^/]+/messages$`), "searching messages"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/users/[^/]+/media$`), "listing stored media"},

	// Toll-free verification: whether a number may send, and why not.
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/phoneNumbers/[^/]+/tollFreeVerification$`), "reading one number's toll-free verification"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/tollFreeVerification/webhooks/subscriptions$`), "listing toll-free verification webhooks"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/tollFreeVerification/useCases$`), "listing toll-free use cases"},

	// The v2 root rather than /api/v2. Bandwidth serves these two from the
	// same host under different prefixes, which is theirs to explain.
	{http.MethodGet, regexp.MustCompile(`^/v2/accounts/[^/]+/endpoints$`), "listing endpoints"},
	{http.MethodGet, regexp.MustCompile(`^/v2/accounts/[^/]+/endpoints/[^/]+$`), "reading one endpoint"},
	{http.MethodGet, regexp.MustCompile(`^/v2/accounts/[^/]+/phoneNumberLookup/bulk/[^/]+$`), "reading a number lookup result"},
}

// guard refuses any request that is not on the list.
type guard struct {
	base http.RoundTripper
}

// readOnly wraps a client so every request it makes goes through the guard.
//
// On the client rather than at each call site, because a call site can be
// forgotten and a transport cannot: everything this plugin sends, including a
// redirect it did not write, is checked.
func readOnly(c *http.Client) *http.Client {
	clone := *c
	clone.Transport = guard{base: c.Transport}
	return &clone
}

func (g guard) RoundTrip(req *http.Request) (*http.Response, error) {
	path := normalisePath(req.URL.Path)

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
	// and naming what the path is *for* is what makes it findable.
	if len(known) > 0 {
		return nil, fmt.Errorf(
			"bandwidth: refusing %s %s; this integration only reads, and %s is "+
				"only ever called with %s",
			req.Method, path, path, strings.Join(known, " or "))
	}
	return nil, fmt.Errorf(
		"bandwidth: refusing %s %s; it is not one of the endpoints this "+
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

// normalisePath collapses the ways one path can be written.
//
// A trailing slash and a doubled separator are the same resource to a server
// and different strings to a regular expression, so they are removed before
// the list is consulted rather than spelled out in every pattern.
func normalisePath(p string) string {
	if p == "" {
		return "/"
	}
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
	}
	if p == "" {
		return "/"
	}
	return p
}
