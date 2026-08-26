package graylog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// maxErrorBody bounds how much of a failure response is read. Enough for a
// message, not enough for a Java stack trace or a page of HTML from the
// reverse proxy that answered instead of Graylog.
const maxErrorBody = 8 << 10

// apiError is Graylog's failure envelope.
//
//	{"type":"ApiError","message":"Unable to parse query"}
//
// Some endpoints answer with a plain string body and some with neither, which
// is why message() falls back rather than assuming.
type apiError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (e apiError) message() string { return strings.TrimSpace(e.Message) }

// summarise renders a failure response for a human.
//
// The body is parsed as the documented envelope and falls back to raw text. A
// request that never reached Graylog -- a proxy, a load balancer, an
// authenticating gateway -- answers with something else entirely, and that is
// exactly the case where seeing the body helps most.
func summarise(status int, body []byte) string {
	var e apiError
	if err := json.Unmarshal(body, &e); err == nil {
		if msg := e.message(); msg != "" {
			return fmt.Sprintf("%s (HTTP %d)", msg, status)
		}
	}
	text := strings.TrimSpace(string(body))
	// An HTML body means something other than the API answered. Saying which
	// is more useful than quoting the first 200 bytes of a <head>.
	lower := strings.ToLower(text)
	if strings.HasPrefix(lower, "<!doctype") || strings.HasPrefix(lower, "<html") {
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

// explainRequestFailure turns an API failure into something a person can act
// on and a model can repeat without leaking anything.
//
// The bodies are quoted where they are quotable: a Graylog rejecting a query
// says which part it could not parse, and that message is the entire fix. What
// is never quoted is a *successful* response, which is somebody's log data.
func explainRequestFailure(status int, path string, body []byte) error {
	// A redirect is the most informative failure this integration gets. The
	// API does not redirect; something in front of it does, and that is
	// usually an authenticating proxy that never let the request through.
	if status >= 300 && status < 400 {
		return fmt.Errorf("graylog: %s redirected instead of answering. The API "+
			"itself does not redirect, so something in front of it did -- "+
			"usually a proxy or single sign-on gateway that wants a browser "+
			"session. mcpd needs to reach Graylog's own address, with its own "+
			"credential, rather than going through a gateway that only speaks "+
			"to browsers", path)
	}

	switch status {
	case http.StatusBadRequest:
		// The two 400s that mean entirely different things. Graylog rejects a
		// POST with no X-Requested-By as a 400 naming the header, which is a
		// bug in this package rather than anything an operator did, and
		// reporting it as "your query was invalid" would send them looking in
		// the wrong place for a long time.
		if mentionsRequestedBy(body) {
			return fmt.Errorf("graylog: %s was refused for a missing "+
				"X-Requested-By header. That is Graylog's cross-site guard and "+
				"mcpd is supposed to send it on every call, so this is a bug "+
				"here rather than a configuration problem: %s",
				path, summarise(status, body))
		}
		return fmt.Errorf("graylog: %s would not accept that request: %s. If it "+
			"names a field, the query or the time range is the thing to fix",
			path, summarise(status, body))

	case http.StatusUnauthorized:
		return fmt.Errorf("graylog: the API rejected our credentials. An access "+
			"token expires when it reaches the TTL it was created with, and "+
			"reads as revoked from here when it does; a username and password "+
			"stop working when the password changes. Reaching %s",
			redactURL(path))

	case http.StatusForbidden:
		return fmt.Errorf("graylog: not permitted to read %s. Graylog authorises "+
			"per entity, so this is the permissions of the account the token "+
			"belongs to rather than anything about the token itself -- a "+
			"stream, an event definition or a system page that account cannot "+
			"see is refused here rather than returned empty", path)

	case http.StatusNotFound:
		return fmt.Errorf("graylog: %s returned nothing. Either it does not "+
			"exist, or this account cannot see it -- Graylog reports an entity "+
			"outside a user's permissions as absent rather than forbidden on "+
			"some endpoints, so the two are indistinguishable from here", path)

	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return fmt.Errorf("graylog: %s timed out. A search over a wide time "+
			"range is a scan of every index in it; narrow the range, name the "+
			"streams, or make the query more selective", path)
	}

	if status >= 500 {
		return fmt.Errorf("graylog: the API failed on %s: %s. A 500 from a "+
			"search is usually the query rather than the server -- a field "+
			"that is not indexed, or a sort on a field with no mapping",
			path, summarise(status, body))
	}
	return fmt.Errorf("graylog: %s: %s", path, summarise(status, body))
}

// mentionsRequestedBy reports whether a failure body is the CSRF refusal.
//
// Matched on the header name rather than on the sentence around it: the
// wording has changed between versions and the header name has not.
func mentionsRequestedBy(body []byte) bool {
	return strings.Contains(strings.ToLower(string(body)), "x-requested-by")
}
