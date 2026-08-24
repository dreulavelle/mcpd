package observium

import (
	"fmt"
	"net/http"
	"strings"
)

// readOnlyTransport is the last thing every Observium request passes through,
// and the only place the read-only guarantee is actually enforced.
//
// Checking at the point where paths are built is checking one place among
// several, and the wrong one. A path is a string until it becomes a URL, and
// the two are not compared the same way: "/devices/491%2F" does not match an
// anchored pattern containing a slash, while the server that receives it
// decodes the escape and routes on the result. Checking here means checking
// URL.Path, which net/url has already decoded -- the same form Observium will
// route on.
//
// The method check is the read-only guarantee itself. This integration reads;
// nothing it does needs to write, and a transport that refuses to write cannot
// be talked into it by a tool that gets a path wrong.
//
// # Widening this later
//
// Mutations are intended -- scheduled maintenance windows first. Adding them
// means allowing specific methods on specific paths *here*, not removing the
// check: the deny-list in denylist.go is written for exactly that day and
// keeps standing between a tool and DELETE /devices/491/?delete_rrd=1. Widen
// by naming what is now permitted, the way the token endpoint is named in
// other integrations, rather than by inverting the default.
type readOnlyTransport struct {
	base http.RoundTripper
}

func (t readOnlyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// URL.Path, not RawPath: percent-escapes are decoded here, so a blocked
	// endpoint reached by a different spelling is compared in the form it will
	// actually be served as.
	path := req.URL.Path

	// The deny-list runs before the method check so that its message is the
	// one a caller sees. "This endpoint destroys monitoring history" is more
	// use than "this integration only reads" when both are true.
	if err := checkPath(req.Method, strings.TrimPrefix(path, apiPrefix)); err != nil {
		return nil, err
	}
	if req.Method != http.MethodGet {
		return nil, fmt.Errorf(
			"observium: refusing a %s to %s; this integration only reads",
			req.Method, path)
	}
	return t.roundTrip(req)
}

func (t readOnlyTransport) roundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// readOnly wraps a client so every request it makes goes through the guard.
//
// A copy: the host's HTTP client is shared, and a transport that refuses to
// write belongs to this plugin rather than to everything using that client.
func readOnly(c *http.Client) *http.Client {
	if c == nil {
		return &http.Client{Transport: readOnlyTransport{}}
	}
	clone := *c
	clone.Transport = readOnlyTransport{base: c.Transport}
	return &clone
}
