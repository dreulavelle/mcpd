package textable

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// maxErrorBody bounds how much of a failure response is read. Enough for a
// message and a reference code, not enough for a Node stack trace or a page of
// HTML from whatever answered instead of the API.
const maxErrorBody = 8 << 10

// apiError is Textable's failure envelope, as the live API actually sends it:
//
//	{"_errType":"TXBDEV_API_ERROR_V1",
//	 "message":"Invalid API Credentials",
//	 "userFriendlyMessage":"",
//	 "referenceCode":"b36eed05-40dd-4a81-a669-9797523b4c0c",
//	 "reason":"Authorization header is missing"}
//
// Four of the five fields are worth something and they are not interchangeable.
// `message` is the class of failure and is often the same for causes that need
// different fixes -- a missing header, a malformed one and a revoked key are
// all "Invalid API Credentials". `reason` is the one that separates them, and
// it is present only sometimes. `referenceCode` is unique to the one failure
// and is the only string somebody can quote to Textable support.
type apiError struct {
	ErrType             string `json:"_errType"`
	Message             string `json:"message"`
	UserFriendlyMessage string `json:"userFriendlyMessage"`
	ReferenceCode       string `json:"referenceCode"`
	Reason              string `json:"reason"`
	// Errors is a second, undocumented envelope some routes answer with
	// instead:
	//
	//	{"errors":["User must be admin to access this endpoint."]}
	//
	// Seen live from GET /api/organizations with a non-admin key, as a 400
	// rather than the 403 the same refusal produces elsewhere. It carries no
	// reference code and none of the fields above, so a reader that only knew
	// the documented shape fell through to quoting the raw body -- which is
	// how the clearest error message this API produces was very nearly the one
	// nobody saw.
	Errors []string `json:"errors"`
}

// text renders the envelope's prose, preferring the field that says the most.
//
// userFriendlyMessage first where it is filled in, because it is written for
// somebody to read; the live API sends it empty more often than not, which is
// why it is a preference rather than the only source.
func (e apiError) text() string {
	msg := strings.TrimSpace(e.UserFriendlyMessage)
	if msg == "" {
		msg = strings.TrimSpace(e.Message)
	}
	if msg == "" && len(e.Errors) > 0 {
		msg = strings.TrimSpace(strings.Join(e.Errors, "; "))
	}
	if reason := strings.TrimSpace(e.Reason); reason != "" {
		if msg == "" {
			return reason
		}
		// Kept as two clauses rather than folded: the message names the class
		// and the reason names this instance of it, and a reader needs both.
		return msg + " (" + reason + ")"
	}
	return msg
}

// summarise renders a failure response for a human.
//
// The body is parsed as the documented envelope and falls back to raw text. A
// request that never reached Textable -- a proxy, a load balancer, a WAF --
// answers with something else entirely, and that is exactly the case where
// seeing the body helps most.
func summarise(status int, body []byte) string {
	var e apiError
	if err := json.Unmarshal(body, &e); err == nil {
		if msg := e.text(); msg != "" {
			if e.ReferenceCode != "" {
				// The reference code goes in every message it is available in.
				// It is what turns "the API refused us" into something
				// Textable can look up, and it costs a dozen characters.
				return fmt.Sprintf("%s (HTTP %d, Textable reference %s)",
					msg, status, e.ReferenceCode)
			}
			return fmt.Sprintf("%s (HTTP %d)", msg, status)
		}
	}
	text := strings.TrimSpace(string(body))
	// An HTML body means something other than the API's own handler answered.
	// What that implies depends entirely on the status, and conflating the two
	// produced the worst error message this package had.
	//
	// A 5xx with an HTML body is the gateway in front of a working API
	// reporting that the API failed -- on this upstream, an unknown or
	// malformed document id in the path reliably produces exactly that. Telling
	// somebody their address might be wrong sends them to re-check a
	// configuration that is fine, while the actual cause is in the argument they
	// passed. It is the difference between a five-minute fix and an afternoon.
	lower := strings.ToLower(text)
	if strings.HasPrefix(lower, "<!doctype") || strings.HasPrefix(lower, "<html") {
		if status >= 500 {
			return fmt.Sprintf("HTTP %d, and the response was an error page from "+
				"the gateway rather than the API's own JSON -- the API is "+
				"reachable and something behind it failed on this request", status)
		}
		return fmt.Sprintf("HTTP %d, and the response was an HTML page rather "+
			"than JSON -- the address may be reaching a web server, a proxy or "+
			"a sign-in page rather than the API", status)
	}
	if len(text) > 200 {
		text = text[:200] + "…"
	}
	if text == "" {
		return fmt.Sprintf("HTTP %d with an empty body", status)
	}
	return fmt.Sprintf("HTTP %d: %s", status, text)
}

// redactURL strips any credential and query string before a URL reaches a log
// or an error a model will read back.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "the configured address"
	}
	u.User = nil
	u.RawQuery = ""
	return u.String()
}

// explainRequestFailure turns an API failure into something a person can act on
// and a model can repeat without leaking anything.
//
// The bodies are quoted where they are quotable. What is never quoted is a
// *successful* response, which is somebody's contact list.
func explainRequestFailure(status int, path string, body []byte) error {
	// A redirect is the most informative failure this integration gets. The API
	// does not redirect; something in front of it does.
	if status >= 300 && status < 400 {
		return fmt.Errorf("textable: %s redirected instead of answering. The "+
			"API itself does not redirect, so something in front of it did -- "+
			"usually a gateway or WAF that wants a browser session. mcpd needs "+
			"to reach the instance's own address with its own key, rather than "+
			"going through something that only speaks to browsers", path)
	}

	switch status {
	case http.StatusBadRequest:
		// This API answers one refusal two ways. GET /api/organizations with a
		// non-admin key is a 400 whose body says "User must be admin to access
		// this endpoint", while the same refusal elsewhere is a 403. Reported
		// as a malformed request, it would send somebody looking at their
		// arguments for a problem that is in their credential.
		if mentionsAdmin(body) {
			return fmt.Errorf("textable: %s needs a key belonging to an admin: "+
				"%s. Textable answers this refusal as a 400 on some routes and "+
				"a 403 on others; either way it is the account the key was made "+
				"under, not the request", path, summarise(status, body))
		}
		return fmt.Errorf("textable: %s would not accept that request: %s",
			path, summarise(status, body))

	case http.StatusUnauthorized:
		// The most likely failure by a wide margin, and the one whose stock
		// message is least useful: "Invalid API Credentials" is what a missing
		// header, a malformed one and a revoked key all say. The envelope's
		// `reason` is quoted by summarise where it exists, and the sentence
		// below covers the case where it does not.
		return fmt.Errorf("textable: the API rejected our key: %s. The key is "+
			"the pair accountUid:apiKey, so half of it pasted on its own fails "+
			"exactly like a revoked one -- check the shape before assuming it "+
			"was revoked. Reaching %s",
			summarise(status, body), redactURL(path))

	case http.StatusForbidden:
		// The failure that means the plugin is working and the *key* is the
		// limit. Worth spelling out, because the fix is never in mcpd.
		return fmt.Errorf("textable: not permitted to read %s: %s. Textable "+
			"scopes a key to the user it was created under, and widens it only "+
			"for an admin -- listings of users and organizations are admin-only, "+
			"and contacts are always the key owner's own. This is that limit "+
			"rather than anything about mcpd, so it is fixed by using a key "+
			"belonging to an account that can see what was asked for",
			path, summarise(status, body))

	case http.StatusNotFound:
		return fmt.Errorf("textable: %s returned nothing. Either it does not "+
			"exist, or this key cannot see it -- an id outside a key's scope "+
			"can be reported as absent rather than forbidden, so the two are "+
			"indistinguishable from here: %s", path, summarise(status, body))

	case http.StatusTooManyRequests:
		return fmt.Errorf("textable: %s was rate limited: %s. Lower "+
			"requests_per_second on this instance's settings, or ask for less "+
			"in one call", path, summarise(status, body))

	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		// Measured, not guessed: /api/contacts against an account holding over a
		// million contacts answers 408 from Textable's own thirty-second timeout,
		// every time, and no pagination parameter changes it -- limit, per_page,
		// pageSize, page, count and take were all tried.
		//
		// So this is not a slow call to retry. It is an endpoint that cannot
		// complete for this account, and saying "try again" would have a model
		// do exactly that, repeatedly, against a production instance that has
		// already spent thirty seconds on each attempt.
		return fmt.Errorf("textable: %s timed out inside Textable (%s). This is "+
			"not transient: the contact listing takes no page or limit "+
			"parameter, so a large account's whole list has to be built in one "+
			"response, and past a few tens of thousands of contacts it cannot "+
			"finish inside Textable's own thirty-second limit. Do not "+
			"retry -- the answer will be the same and each attempt costs the "+
			"instance thirty seconds. Read a contact by id instead where one is "+
			"known", path, summarise(status, body))
	}

	if status >= 500 {
		// Measured on this upstream: a read by id with an id that does not
		// exist -- an organization, a contact -- answers 502 with the gateway's
		// HTML rather than a 404. So on a by-id path this is very often the
		// argument rather than the service, and saying so is the difference
		// between checking an id and paging somebody about an outage.
		//
		// Hedged rather than asserted, because a real outage produces the same
		// thing and this cannot tell them apart from one response.
		if looksLikeReadByID(path) {
			return fmt.Errorf("textable: %s failed: %s. On this API a read by id "+
				"answers 502 rather than 404 when the id does not exist, so check "+
				"the id before concluding the service is down -- ids come from "+
				"the listing tools and are not guessable", path, summarise(status, body))
		}
		return fmt.Errorf("textable: the API failed on %s: %s", path,
			summarise(status, body))
	}
	return fmt.Errorf("textable: %s: %s", path, summarise(status, body))
}

// mentionsAdmin reports whether a failure body is the admin refusal.
//
// Matched on the word rather than on the sentence around it: this envelope is
// undocumented, so its wording is not something to depend on holding still.
func mentionsAdmin(body []byte) bool {
	return strings.Contains(strings.ToLower(string(body)), "must be admin")
}

// looksLikeReadByID reports whether a path ends in an identifier somebody
// supplied, as opposed to a collection.
//
// Used only to choose the wording of a 5xx, so a wrong answer costs a slightly
// less helpful sentence rather than a wrong behaviour. The collections this
// integration reads are all fixed paths, so "last segment is not one of them"
// is a sound enough test.
func looksLikeReadByID(path string) bool {
	switch path {
	case "/health", "/api/v2/tenants", "/api/v2/organizations",
		"/api/v2/billing/tenantReport":
		return false
	}
	return strings.Contains(strings.TrimPrefix(path, "/"), "/")
}
