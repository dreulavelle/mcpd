package flowroute

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// rule is one read this integration may make.
type rule struct {
	path *regexp.Regexp
	// why is quoted back in a refusal, so a reader of the error learns what
	// the path is for rather than only that it is not allowed.
	why string
}

// allowed is the complete set of reads this integration may make.
//
// This list is what makes the integration read-only, and it is the only thing
// that does. A Flowroute API key is not scoped: the same access key and secret
// that read a number can release it, repoint its inbound route, or replace the
// address emergency services are given for it. There is no read-only
// credential to ask for, so the guarantee has to live here.
//
// Every entry is a GET, and the method is checked separately -- there is no
// signing exchange to exempt, because Basic authentication has no handshake.
// Grouped by the question a tool asks.
var allowed = []rule{
	// What the account holds, and everything about one number.
	{regexp.MustCompile(`^/v2/numbers$`), "listing the account's numbers"},
	{regexp.MustCompile(`^/v2/numbers/[0-9]+$`), "reading one number"},

	// Where a number rings. A route is the record that decides which of the
	// customer's systems a call lands on, so this is the answer to most of
	// "why is nobody getting these calls".
	{regexp.MustCompile(`^/v2/routes$`), "listing inbound routes"},
	{regexp.MustCompile(`^/v2/routes/edge_strategies$`), "listing the edge strategies a route can use"},

	// Emergency calling. Read-only matters more here than anywhere else in
	// this package: these records are the address dispatch is given.
	{regexp.MustCompile(`^/v2/e911s$`), "listing emergency-calling addresses"},
	{regexp.MustCompile(`^/v2/e911s/[0-9]+$`), "reading one emergency-calling address"},

	// The name that shows on somebody else's handset.
	{regexp.MustCompile(`^/v2/cnams$`), "listing caller-ID name records"},
	{regexp.MustCompile(`^/v2/cnams/[0-9]+$`), "reading one caller-ID name record"},

	// Numbers arriving from another carrier, and why one is stuck.
	{regexp.MustCompile(`^/v2/portorders$`), "listing port orders"},
	{regexp.MustCompile(`^/v2/portorders/[0-9]+$`), "reading one port order"},
	{regexp.MustCompile(`^/v2/portorders/[0-9]+/status$`), "reading one port order's status"},

	// Call detail records are produced by an export job rather than a query.
	// Only the reads are here: requesting an export is a POST, and this
	// integration does not make one.
	{regexp.MustCompile(`^/v2/cdrs/exports$`), "listing call-detail export jobs"},
	{regexp.MustCompile(`^/v2/cdrs/exports/[0-9]+$`), "reading one call-detail export job"},
}

// guard refuses any request that is not on the list.
//
// It also pins the address. Basic authentication puts the credential on every
// single request rather than exchanging it once for a token, so a request that
// leaves for another host carries the account's access key and secret with it.
// The only ways to produce one are a redirect chased somewhere else or a bug
// in how a URL was built, and neither is a reason to hand the credential to a
// stranger.
type guard struct {
	base         http.RoundTripper
	scheme, host string
}

// readOnly wraps a client so every request it makes goes through the guard.
//
// On the client rather than at each call site, because a call site can be
// forgotten and a transport cannot: everything this plugin sends, including a
// redirect it did not write, is checked.
func readOnly(c *http.Client, base string) (*http.Client, error) {
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("flowroute: the API address %q is not a URL: %w", base, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("flowroute: the API address %q needs a scheme and a host", base)
	}
	clone := *c
	clone.Transport = guard{base: c.Transport, scheme: u.Scheme, host: u.Host}
	return &clone, nil
}

func (g guard) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := g.check(req); err != nil {
		return nil, err
	}
	rt := g.base
	if rt == nil {
		rt = http.DefaultTransport
	}
	return rt.RoundTrip(req)
}

// check is the whole of the read-only guarantee.
func (g guard) check(req *http.Request) error {
	if !strings.EqualFold(req.URL.Host, g.host) || !strings.EqualFold(req.URL.Scheme, g.scheme) {
		return fmt.Errorf("flowroute: refusing to send a request to %s://%s -- this "+
			"integration talks to %s://%s and nowhere else, and the credential "+
			"travels on every request",
			req.URL.Scheme, req.URL.Host, g.scheme, g.host)
	}
	if req.Method != http.MethodGet {
		return fmt.Errorf("flowroute: refusing to send %s %s -- this integration "+
			"is read-only and makes only GET requests",
			req.Method, req.URL.Path)
	}
	for _, r := range allowed {
		if r.path.MatchString(req.URL.Path) {
			return nil
		}
	}
	return fmt.Errorf("flowroute: refusing to read %s -- it is not one of the "+
		"endpoints this integration is allowed to read. Adding one is a "+
		"deliberate change to the list in transport.go", req.URL.Path)
}
