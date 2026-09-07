package textable

import (
	"net/http"
	"strings"

	"github.com/spoked/mcpd/internal/plugins/httpguard"
)

// The read-only guarantee is enforced by httpguard; what is this plugin's own
// is the table below.

// allowed is the complete set of requests this integration may make.
// Everything else is refused before it reaches the network.
//
// Every entry is a GET, and every one is a read a service account is documented
// to be permitted. Grouped by the question a tool asks.
var allowed = []httpguard.Rule{
	// Whether the instance is up at all. Unauthenticated, and the first half of
	// the startup probe: it separates "cannot reach it" from "it did not like
	// the token".
	httpguard.Get(`^/health$`, "reading the instance's health"),

	// The tenant listing. Undocumented as a GET -- the specification describes
	// only POST /api/v2/tenants -- and it is the endpoint that makes the rest
	// work, because it is the only source of Textable's internal tenant id.
	httpguard.Get(`^/api/v2/tenants$`, "listing tenants"),

	// The billing report, which doubles as the user directory. Spelled the way
	// the API serves it: the specification writes "biling", and that path
	// answers 404 on a live instance.
	httpguard.Get(`^/api/v2/billing/tenantReport$`,
		"listing every tenant's users"),
	httpguard.Get(`^/api/v2/billing/tenantReport/[^/]+$`,
		"reading one tenant's licensing and users"),

	// Organizations, which need a tenant id to list and an id of their own to
	// read.
	httpguard.Get(`^/api/v2/organizations$`,
		"listing a tenant's organizations"),
	httpguard.Get(`^/api/v2/organizations/[^/]+$`,
		"reading one organization"),

	// One contact, by id. There is no user read here: GET /api/v2/users/{id}
	// answers 401 to a service account token whatever the specification says,
	// so naming it would permit a call that cannot work.
	//
	// Anchoring still matters on what remains: /api/v2/users/{id}/token mints a
	// long-lived credential, and an unanchored pattern for any /api/v2/users
	// path would reach it.
	httpguard.Get(`^/api/v2/contacts/[^/]+$`, "reading one contact"),
}

// readOnly wraps a client so every Textable request is checked against allowed.
//
// The prefix is the configured address's own path, empty for the ordinary case
// of an API at the root of its host.
func readOnly(c *http.Client, basePath string) *http.Client {
	prefix := strings.TrimSuffix(httpguard.Normalise(basePath), "/")
	if prefix == "/" {
		prefix = ""
	}
	return httpguard.Client(c, "textable", prefix, allowed)
}
