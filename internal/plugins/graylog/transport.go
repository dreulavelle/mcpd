package graylog

import (
	"net/http"
	"strings"

	"github.com/spoked/mcpd/internal/plugins/httpguard"
)

// The read-only guarantee is enforced by httpguard; what is graylog's own is
// the table below.
//
// Why an allow-list and not "GET only", which is all observium needs: Graylog's
// search, aggregation and event endpoints are POSTs -- a question about a
// million log lines does not fit in a query string -- and those three are most
// of the reason this integration exists. A method check would refuse them, and
// widening it to "GET, or POST" would permit every write in the API in the same
// breath.
//
// It also removes the need for the separate deny-list observium carries. That
// list exists to survive its guard being widened from "GET only" to something
// broader; there is nothing equivalent to survive here, because widening this
// guard *is* naming a path.

// allowed is the complete set of requests this integration may make, under
// /api. Everything else is refused before it reaches the network.
//
// Grouped the way the tools are: what a question is asked with, then what the
// answer needs to be understood.
var allowed = []httpguard.Rule{
	// The scripting API. These are the reads that have to be POSTs.
	httpguard.Post(`^/search/messages$`, "running a search"),
	httpguard.Post(`^/search/aggregate$`, "running an aggregation"),
	httpguard.Post(`^/events/search$`, "searching events and alerts"),

	// Field types. GET is every field in the system; POST is the same
	// question narrowed to a set of streams, which on a large installation is
	// the difference between a usable answer and ten thousand names.
	//
	// Anchored, deliberately: /views/fields/poll is a POST that triggers a
	// cluster-wide refresh of the field type cache, and it must not be
	// reachable by a tool that means to ask what fields exist.
	httpguard.Get(`^/views/fields$`, "listing field names"),
	httpguard.Post(`^/views/fields$`, "listing field names for streams"),

	// What this Graylog is: version, node id, lifecycle. The cheapest
	// authenticated call there is, which is what makes it the startup probe.
	httpguard.Get(`^/system$`, "reading the server's version and state"),

	// How the installation is arranged.
	httpguard.Get(`^/streams/paginated$`, "listing streams"),
	httpguard.Get(`^/streams/[^/]+$`, "reading one stream"),
	httpguard.Get(`^/events/definitions$`, "listing event definitions"),
	httpguard.Get(`^/events/definitions/[^/]+$`, "reading one event definition"),

	// How the installation is. /cluster is the node overview; the indexer
	// health is the one that says whether messages are being written at all.
	httpguard.Get(`^/cluster$`, "reading the node overview"),
	httpguard.Get(`^/system/cluster/nodes$`, "listing nodes"),
	httpguard.Get(`^/system/indexer/cluster/health$`, "reading indexer health"),
	httpguard.Get(`^/system/inputs$`, "listing inputs"),
	httpguard.Get(`^/system/notifications$`, "listing system notifications"),
	httpguard.Get(`^/system/indices/index_sets$`, "listing index sets"),
}

// readOnly wraps a client so every Graylog request is checked against allowed.
//
// The prefix is the configured address's own path plus /api, because Graylog
// behind a reverse proxy at /graylog is an ordinary deployment and its requests
// arrive as /graylog/api/search/messages.
func readOnly(c *http.Client, basePath string) *http.Client {
	prefix := httpguard.Normalise(strings.TrimSuffix(basePath, "/") + apiPrefix)
	return httpguard.Client(c, "graylog", prefix, allowed)
}
