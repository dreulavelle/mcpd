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

	// The Dashboard API, served from the gateway under /api/v2. Everything
	// below is XML, and everything below is a GET -- the Dashboard's writes
	// (ordering numbers, submitting a port, disconnecting a line) are exactly
	// what this integration exists not to do.
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/portins$`), "listing port-in orders"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/portins/[^/]+$`), "reading one port-in order"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/portins/[^/]+/history$`), "reading a port-in order's history"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/portins/[^/]+/notes$`), "reading a port-in order's notes"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/portins/[^/]+/loas$`), "checking a port-in order's letter of authorisation"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/bulkPortins$`), "listing bulk port-in orders"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/bulkPortins/[^/]+$`), "reading one bulk port-in order"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/bulkPortins/[^/]+/tnList$`), "reading a bulk port-in order's numbers"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/tollFreePortingValidations$`), "listing toll-free porting validations"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/tollFreePortingValidations/[^/]+$`), "reading one toll-free porting validation"},

	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/inserviceNumbers$`), "listing numbers in service"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/inserviceNumbers/totals$`), "counting numbers in service"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/discnumbers$`), "listing disconnected numbers"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/discnumbers/totals$`), "counting disconnected numbers"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/availableNumbers$`), "searching numbers available to order"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/tnoptions$`), "listing per-number options"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/tnoptions/[^/]+$`), "reading one number's options"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/orders$`), "listing number orders"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/orders/[^/]+$`), "reading one number order"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/disconnects$`), "listing disconnect orders"},

	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/sites$`), "listing sites"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/sites/[^/]+$`), "reading one site"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/sites/[^/]+/totaltns$`), "counting one site's numbers"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/sites/[^/]+/inserviceNumbers$`), "listing one site's numbers"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/sites/[^/]+/sippeers$`), "listing a site's SIP peers"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/sites/[^/]+/sippeers/[^/]+$`), "reading one SIP peer"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/sites/[^/]+/sippeers/[^/]+/tns$`), "listing a SIP peer's numbers"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/sites/[^/]+/sippeers/[^/]+/totaltns$`), "counting a SIP peer's numbers"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/sites/[^/]+/sippeers/[^/]+/products/messaging/applicationSettings$`), "reading a SIP peer's messaging application"},

	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/applications$`), "listing applications"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/applications/[^/]+$`), "reading one application"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/applications/[^/]+/associatedsippeers$`), "reading which SIP peers use an application"},

	// Emergency calling. Read-only matters more here than anywhere: these
	// records are the address emergency services are given.
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/e911s$`), "listing emergency-calling endpoints"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/e911s/locations$`), "listing emergency-service addresses"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/e911s/locations/[^/]+$`), "reading one emergency-service address"},

	// 10DLC. JSON rather than XML, on the same host and the same prefix.
	// Campaign management, under /api rather than /api/v2. The /api/v2/…/tendlc
	// paths these replaced are the Registration Center, a different product
	// that these accounts are not enabled for -- so every 10DLC read failed
	// with an entitlement error for a campaign that plainly exists.
	// One telephone number, from every angle. These hang off /tns rather than
	// off an account, because a number exists in the numbering plan before it
	// exists on anybody's account -- which is what lets get_number answer about
	// a number that has left.
	{http.MethodGet, regexp.MustCompile(`^/api/v2/tns/[0-9]+$`), "reading one number"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/tns/[0-9]+/tndetails$`), "reading a number's details"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/tns/[0-9]+/e911$`), "reading a number's E911 record"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/tns/[0-9]+/ratecenter$`), "reading a number's rate centre"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/tns/[0-9]+/lata$`), "reading a number's LATA"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/tns/[0-9]+/lca$`), "reading a number's local calling area"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/tns/[0-9]+/sites$`), "reading where a number is routed"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/tns/[0-9]+/sippeers$`), "reading a number's SIP peers"},

	// Caller-ID name, directory listings, and the customer service records a
	// port is built from. All order-shaped: the question is what happened to
	// the request, not what the value is.
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/lidbs$`), "listing caller-ID name orders"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/lidbs/[^/]+$`), "reading one caller-ID name order"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/dldas$`), "listing directory listing orders"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/dldas/[^/]+$`), "reading one directory listing order"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/dldas/[^/]+/history$`), "reading a directory listing order's history"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/csrs$`), "listing customer service record requests"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/csrs/[^/]+$`), "reading one customer service record request"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/csrs/[^/]+/notes$`), "reading a customer service record's notes"},

	// One disconnect order and why it failed. The listing says an order
	// exists; the notes say what went wrong with it.
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/disconnects/[^/]+$`), "reading one disconnect order"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/disconnects/[^/]+/notes$`), "reading a disconnect order's notes"},

	// Port-out protection. This returns the passcode itself, so the tool that
	// reads it is capability-gated -- see list_portout_passcodes.
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/tnPortoutPasscodes$`), "listing port-out protection passcodes"},

	// Where Bandwidth sends order notifications.
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/subscriptions$`), "listing notification subscriptions"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/subscriptions/[^/]+$`), "reading one notification subscription"},

	// Individual calls as Insights recorded them, on the Insights host.
	{http.MethodGet, regexp.MustCompile(`^/api/v1/voice/calls$`), "searching individual calls"},

	// What the account is entitled to. Read when a refusal might be an
	// entitlement rather than a credential; the two look alike from the error.
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/products$`), "listing the account's enabled products"},

	// Port-outs: numbers leaving. The mirror of the port-in reads above, and
	// the half that explains a number that stopped working without any fault.
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/portouts$`), "listing port-out orders"},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/accounts/[^/]+/portouts/[^/]+$`), "reading one port-out order"},

	{http.MethodGet, regexp.MustCompile(`^/api/accounts/[^/]+/campaignManagement/10dlc/campaigns$`), "listing 10DLC campaigns"},
	{http.MethodGet, regexp.MustCompile(`^/api/accounts/[^/]+/campaignManagement/10dlc/campaigns/[^/]+$`), "reading one 10DLC campaign"},
	{http.MethodGet, regexp.MustCompile(`^/api/accounts/[^/]+/campaignManagement/10dlc/campaigns/[^/]+/tn$`), "listing a campaign's numbers"},
	{http.MethodGet, regexp.MustCompile(`^/api/accounts/[^/]+/campaignManagement/10dlc/brands$`), "listing 10DLC brands"},
	{http.MethodGet, regexp.MustCompile(`^/api/accounts/[^/]+/campaignManagement/10dlc/brands/details$`), "listing 10DLC brands in detail"},
	{http.MethodGet, regexp.MustCompile(`^/api/accounts/[^/]+/campaignManagement/10dlc/brands/[^/]+$`), "reading one 10DLC brand"},
	{http.MethodGet, regexp.MustCompile(`^/api/accounts/[^/]+/campaignManagement/10dlc/brands/[^/]+/vetting$`), "reading a brand's vetting record"},

	// Insights, on its own host. One path with the monitor as its last
	// segment, so a monitor added by Bandwidth needs no change here -- but a
	// path outside /v1/monitors/voice still does.
	{http.MethodGet, regexp.MustCompile(`^/v1/monitors/voice/[^/]+$`), "reading a voice traffic aggregate"},

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
