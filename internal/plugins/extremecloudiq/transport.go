package extremecloudiq

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
// Grouped the way the tools are: who the token is, what is deployed, who is
// connected, what has gone wrong, and how the estate is arranged.
//
// It names what is reached and nothing else. An entry for an endpoint no tool
// calls is a permission granted in advance for a read nobody has argued for,
// which is the habit this list exists to prevent -- /locations/site and
// /account/home were both here and both went, because the location tree and
// the token probe already answer what they were for.
var allowed = []httpguard.Rule{
	// Who this token is. The cheapest authenticated call there is, and the
	// startup probe: it names the account, the data centre and the expiry
	// without reading a single row of anybody's estate.
	httpguard.Get(`^/auth/apitoken/info$`, "reading who this token belongs to"),

	// What is deployed.
	httpguard.Get(`^/devices$`, "listing devices"),
	httpguard.Get(`^/devices/stats$`, "counting devices"),
	httpguard.Get(`^/devices/[0-9]+$`, "reading one device"),
	httpguard.Get(`^/devices/[0-9]+/location$`, "reading where one device is"),
	httpguard.Get(`^/devices/[0-9]+/network-policy$`, "reading one device's network policy"),

	// How one device is. The three series a troubleshooting question needs,
	// and nothing that would run a command on it.
	httpguard.Get(`^/devices/[0-9]+/alarms$`, "listing one device's alarms"),
	httpguard.Get(`^/devices/[0-9]+/history/cpu-mem$`, "reading one device's processor and memory history"),
	httpguard.Get(`^/devices/[0-9]+/interfaces/wifi$`, "reading one device's radio statistics"),

	// Who is connected.
	httpguard.Get(`^/clients/active$`, "listing connected clients"),
	httpguard.Get(`^/clients/active/count$`, "counting connected clients"),
	httpguard.Get(`^/clients/summary$`, "summarising connected clients"),

	// What has gone wrong, and who changed something before it did.
	httpguard.Get(`^/alerts$`, "listing alerts"),
	httpguard.Get(`^/alerts/count-by-(SEVERITY|CATEGORY|ALERT_TYPE)$`, "counting alerts by group"),
	httpguard.Get(`^/logs/audit$`, "listing who changed what"),

	// How one client has actually been getting on. The connectivity trail is
	// the only place the API says *why* a client failed rather than that it
	// did: which stage broke, and how long each one took.
	httpguard.Get(`^/clients/byMac/[^/]+$`, "finding one client by MAC address"),
	httpguard.Get(`^/client-details/overview/info/[0-9]+$`, "reading one client's details"),
	httpguard.Get(`^/client-details/client-trail/connectivity-experience/[0-9]+$`, "reading one client's connection attempts"),
	httpguard.Get(`^/client-details/client-trail/roaming-trail/grid/[0-9]+$`, "reading one client's roaming history"),
	httpguard.Get(`^/d360/device/issues$`, "counting one device's client failures"),

	// The diagnostics grids. These are the POSTs: the filter is a list of site
	// and device ids, which is why they are not GETs.
	httpguard.Post(`^/dashboard/wireless/device-health/grid$`, "listing unwell access points"),
	httpguard.Post(`^/dashboard/wired/device-health/grid$`, "listing unwell switches"),
	httpguard.Post(`^/dashboard/wireless/client-health/grid$`, "listing wireless clients with problems"),
	httpguard.Post(`^/dashboard/wired/client-health/grid$`, "listing wired clients with problems"),
	httpguard.Post(`^/dashboard/wireless/usage-capacity/grid$`, "listing access points that are saturated"),
	httpguard.Post(`^/dashboard/wired/usage-capacity/grid$`, "listing switches that are saturated"),
	httpguard.Post(`^/dashboard/sites-with-issues$`, "listing sites with problems"),

	// Scores per site, and the anomaly analysis over the estate.
	httpguard.Get(`^/network-scorecard/(networkHealth|clientHealth|deviceHealth|wifiHealth|servicesHealth)/[0-9]+$`, "reading a site's health scores"),
	httpguard.Get(`^/copilot/anomalies/anomalies-by-category$`, "counting anomalies by site, severity and kind"),

	// How the estate is arranged.
	httpguard.Get(`^/locations/tree$`, "reading the site hierarchy"),
	httpguard.Get(`^/network-policies$`, "listing network policies"),
	httpguard.Get(`^/network-policies/[0-9]+/ssids$`, "listing one policy's SSIDs"),
	httpguard.Get(`^/ssids$`, "listing SSIDs"),
}

// readOnly wraps a client so every request is checked against allowed.
//
// The prefix is the configured address's own path, empty for an API at the root
// of its host and "/gateway" for one behind a proxy.
func readOnly(c *http.Client, basePath string) *http.Client {
	prefix := strings.TrimSuffix(strings.TrimSpace(basePath), "/")
	if prefix != "" {
		prefix = httpguard.Normalise(prefix)
	}
	return httpguard.Client(c, "extremecloudiq", prefix, allowed)
}
