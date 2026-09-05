package bookstack

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// rule is one request this integration may make.
type rule struct {
	method string
	path   *regexp.Regexp
	// why is quoted back in a refusal, so a reader of the error learns what
	// the endpoint is for rather than only that it is not allowed.
	why string
}

// allowed is the complete set of requests this integration may make.
//
// # What this list is for, given that this integration writes
//
// The read-only integrations use a list like this as the whole of their
// guarantee. This one cannot: writing is the point. So the guarantee here is
// narrower and worth stating exactly.
//
// What the approval path guarantees is that a *change* is planned, shown and
// approved before it happens. What this list guarantees is that no request
// reaches an endpoint nobody meant it to -- a mistyped path, a URL built from
// a caller's string, a redirect chased somewhere unexpected. Default-deny, so
// a new endpoint is a deliberate line here.
//
// The two are independent, and both are needed. A write reaching an allowed
// endpoint without approval would be a bug in the mutation wiring; a write
// reaching an endpoint not on this list would be a bug in a URL. Neither
// covers the other.
var allowed = []rule{
	// The shelf, book, chapter and page hierarchy: the knowledge base itself.
	{http.MethodGet, regexp.MustCompile(`^/api/shelves$`), "listing shelves"},
	{http.MethodGet, regexp.MustCompile(`^/api/shelves/[0-9]+$`), "reading one shelf"},
	{http.MethodPost, regexp.MustCompile(`^/api/shelves$`), "creating a shelf"},
	{http.MethodPut, regexp.MustCompile(`^/api/shelves/[0-9]+$`), "updating a shelf"},
	{http.MethodDelete, regexp.MustCompile(`^/api/shelves/[0-9]+$`), "sending a shelf to the recycle bin"},

	{http.MethodGet, regexp.MustCompile(`^/api/books$`), "listing books"},
	{http.MethodGet, regexp.MustCompile(`^/api/books/[0-9]+$`), "reading one book"},
	{http.MethodGet, regexp.MustCompile(`^/api/books/[0-9]+/export/(html|markdown|plaintext)$`), "exporting a book"},
	{http.MethodPost, regexp.MustCompile(`^/api/books$`), "creating a book"},
	{http.MethodPut, regexp.MustCompile(`^/api/books/[0-9]+$`), "updating a book"},
	{http.MethodDelete, regexp.MustCompile(`^/api/books/[0-9]+$`), "sending a book to the recycle bin"},

	{http.MethodGet, regexp.MustCompile(`^/api/chapters$`), "listing chapters"},
	{http.MethodGet, regexp.MustCompile(`^/api/chapters/[0-9]+$`), "reading one chapter"},
	{http.MethodGet, regexp.MustCompile(`^/api/chapters/[0-9]+/export/(html|markdown|plaintext)$`), "exporting a chapter"},
	{http.MethodPost, regexp.MustCompile(`^/api/chapters$`), "creating a chapter"},
	{http.MethodPut, regexp.MustCompile(`^/api/chapters/[0-9]+$`), "updating a chapter"},
	{http.MethodDelete, regexp.MustCompile(`^/api/chapters/[0-9]+$`), "sending a chapter to the recycle bin"},

	{http.MethodGet, regexp.MustCompile(`^/api/pages$`), "listing pages"},
	{http.MethodGet, regexp.MustCompile(`^/api/pages/[0-9]+$`), "reading one page"},
	{http.MethodGet, regexp.MustCompile(`^/api/pages/[0-9]+/export/(html|markdown|plaintext)$`), "exporting a page"},
	{http.MethodPost, regexp.MustCompile(`^/api/pages$`), "creating a page"},
	{http.MethodPut, regexp.MustCompile(`^/api/pages/[0-9]+$`), "updating a page"},
	{http.MethodDelete, regexp.MustCompile(`^/api/pages/[0-9]+$`), "sending a page to the recycle bin"},

	// Finding things, and the tag vocabulary somebody has already established.
	{http.MethodGet, regexp.MustCompile(`^/api/search$`), "searching the knowledge base"},
	{http.MethodGet, regexp.MustCompile(`^/api/tags/names$`), "listing tag names"},
	{http.MethodGet, regexp.MustCompile(`^/api/tags/values-for-name$`), "listing a tag's values"},

	// Conversation on a page.
	{http.MethodGet, regexp.MustCompile(`^/api/comments$`), "listing comments"},
	{http.MethodGet, regexp.MustCompile(`^/api/comments/[0-9]+$`), "reading one comment"},
	{http.MethodPost, regexp.MustCompile(`^/api/comments$`), "adding a comment"},
	{http.MethodPut, regexp.MustCompile(`^/api/comments/[0-9]+$`), "editing a comment"},
	{http.MethodDelete, regexp.MustCompile(`^/api/comments/[0-9]+$`), "deleting a comment"},

	// Files hung off a page, and the image library pages draw from.
	{http.MethodGet, regexp.MustCompile(`^/api/attachments$`), "listing attachments"},
	{http.MethodGet, regexp.MustCompile(`^/api/attachments/[0-9]+$`), "reading one attachment"},
	{http.MethodPost, regexp.MustCompile(`^/api/attachments$`), "adding an attachment"},
	{http.MethodPut, regexp.MustCompile(`^/api/attachments/[0-9]+$`), "updating an attachment"},
	{http.MethodDelete, regexp.MustCompile(`^/api/attachments/[0-9]+$`), "deleting an attachment"},

	{http.MethodGet, regexp.MustCompile(`^/api/image-gallery$`), "listing images"},
	{http.MethodGet, regexp.MustCompile(`^/api/image-gallery/[0-9]+$`), "reading one image"},
	{http.MethodPost, regexp.MustCompile(`^/api/image-gallery$`), "adding an image"},
	{http.MethodPut, regexp.MustCompile(`^/api/image-gallery/[0-9]+$`), "updating an image"},
	{http.MethodDelete, regexp.MustCompile(`^/api/image-gallery/[0-9]+$`), "deleting an image"},

	// Who can see what. Reading these answers "why can this person not open
	// that page", which is the support question; writing them is how it is
	// fixed.
	{http.MethodGet, regexp.MustCompile(`^/api/content-permissions/(bookshelf|book|chapter|page)/[0-9]+$`), "reading an item's permissions"},
	{http.MethodPut, regexp.MustCompile(`^/api/content-permissions/(bookshelf|book|chapter|page)/[0-9]+$`), "changing an item's permissions"},

	// People and the roles they hold.
	{http.MethodGet, regexp.MustCompile(`^/api/users$`), "listing users"},
	{http.MethodGet, regexp.MustCompile(`^/api/users/[0-9]+$`), "reading one user"},
	{http.MethodPost, regexp.MustCompile(`^/api/users$`), "creating a user"},
	{http.MethodPut, regexp.MustCompile(`^/api/users/[0-9]+$`), "updating a user"},
	{http.MethodDelete, regexp.MustCompile(`^/api/users/[0-9]+$`), "deleting a user"},

	{http.MethodGet, regexp.MustCompile(`^/api/roles$`), "listing roles"},
	{http.MethodGet, regexp.MustCompile(`^/api/roles/[0-9]+$`), "reading one role"},
	{http.MethodPost, regexp.MustCompile(`^/api/roles$`), "creating a role"},
	{http.MethodPut, regexp.MustCompile(`^/api/roles/[0-9]+$`), "updating a role"},
	{http.MethodDelete, regexp.MustCompile(`^/api/roles/[0-9]+$`), "deleting a role"},

	// What was deleted, and putting it back.
	{http.MethodGet, regexp.MustCompile(`^/api/recycle-bin$`), "listing the recycle bin"},
	{http.MethodPut, regexp.MustCompile(`^/api/recycle-bin/[0-9]+$`), "restoring something from the recycle bin"},
	{http.MethodDelete, regexp.MustCompile(`^/api/recycle-bin/[0-9]+$`), "permanently destroying something in the recycle bin"},

	// What has been happening.
	{http.MethodGet, regexp.MustCompile(`^/api/audit-log$`), "reading the audit log"},

	// The instance itself. The startup probe.
	{http.MethodGet, regexp.MustCompile(`^/api/system$`), "reading the instance's version and name"},
}

// guard refuses any request that is not on the list, and any request addressed
// anywhere but the configured instance.
//
// The address is pinned because the token pair travels on every request rather
// than being exchanged for a session, so a request that leaves for another
// host carries a credential to the whole knowledge base with it. The only ways
// to produce one are a redirect chased somewhere else or a bug in how a URL
// was built, and neither is a reason to hand the token to a stranger.
type guard struct {
	base         http.RoundTripper
	scheme, host string
}

// guarded wraps a client so every request it makes goes through the guard.
//
// On the client rather than at each call site, because a call site can be
// forgotten and a transport cannot: everything this plugin sends, including a
// redirect it did not write, is checked.
func guarded(c *http.Client, root string) (*http.Client, error) {
	u, err := url.Parse(root)
	if err != nil {
		return nil, fmt.Errorf("bookstack: the address %q is not a URL: %w", root, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("bookstack: the address %q needs a scheme and a host", root)
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

// check is the endpoint guarantee.
func (g guard) check(req *http.Request) error {
	if !strings.EqualFold(req.URL.Host, g.host) || !strings.EqualFold(req.URL.Scheme, g.scheme) {
		return fmt.Errorf("bookstack: refusing to send a request to %s://%s -- this "+
			"instance is %s://%s and nowhere else, and the token travels on every "+
			"request", req.URL.Scheme, req.URL.Host, g.scheme, g.host)
	}
	// BookStack may be served under a path, so the API path is whatever
	// follows it. Matching on the suffix from /api keeps the rules readable
	// and works either way.
	path := apiPath(req.URL.Path)
	for _, r := range allowed {
		if r.method == req.Method && r.path.MatchString(path) {
			return nil
		}
	}
	return fmt.Errorf("bookstack: refusing to send %s %s -- it is not one of the "+
		"endpoints this integration is allowed to reach. Adding one is a "+
		"deliberate change to the list in transport.go", req.Method, path)
}

// apiPath reduces a URL path to the part from /api onward, so an instance
// served under a sub-path matches the same rules as one at the root.
func apiPath(p string) string {
	if i := strings.Index(p, apiPrefix+"/"); i >= 0 {
		return p[i:]
	}
	if strings.HasSuffix(p, apiPrefix) {
		return apiPrefix
	}
	return p
}
