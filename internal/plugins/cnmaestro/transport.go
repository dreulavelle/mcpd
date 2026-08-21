package cnmaestro

import (
	"fmt"
	"net/http"
	"strings"
)

// readOnlyTransport is the last thing every cnMaestro request passes through,
// and the only place the read-only guarantee is actually enforced.
//
// The deny-list was checked where paths are built, which is one place among
// several and the wrong one. A path is a string until it becomes a URL, and
// the two are not compared the same way: "/devices/AA%2Fcli" does not match
// an anchored pattern containing a slash, while the server that receives it
// decodes the escape and routes it to the command endpoint. Checking here
// means checking `URL.Path`, which net/url has already decoded -- the same
// form the server will route on.
//
// The method matters for the same reason. This integration reads; nothing it
// does needs to write, and a transport that refuses to write cannot be talked
// into it by a tool that gets a path wrong. Adding a write surface later means
// deliberately widening this, which is exactly the amount of friction the
// decision deserves on a production network.
//
// The one exception is the token endpoint, which is a POST by definition of
// OAuth client credentials. It is named exactly rather than matched loosely.
type readOnlyTransport struct {
	base http.RoundTripper
}

func (t readOnlyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// URL.Path, not RawPath: percent-escapes are decoded here, so a blocked
	// endpoint reached by a different spelling is compared in the form it
	// will actually be served as.
	path := req.URL.Path

	if req.Method == http.MethodPost && path == tokenPath {
		return t.roundTrip(req)
	}
	if req.Method != http.MethodGet {
		return nil, fmt.Errorf(
			"cnmaestro: refusing a %s to %s; this integration only reads",
			req.Method, path)
	}
	if err := checkPath(strings.TrimPrefix(path, apiPrefix)); err != nil {
		return nil, err
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
