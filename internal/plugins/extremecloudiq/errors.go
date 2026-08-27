package extremecloudiq

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// maxErrorBody bounds how much of a failure response is read. Enough for a
// message, not enough for a stack trace or a page of HTML from whatever
// answered instead of the API.
const maxErrorBody = 8 << 10

// apiError is ExtremeCloud IQ's failure envelope.
//
//	{"error_code":"...","error_id":"...","error_message":"..."}
//
// error_id is the one worth keeping beside the message: it is the handle
// Extreme's own support asks for, and it is the only part of a 500 that means
// anything to anybody.
type apiError struct {
	ErrorCode        string `json:"error_code"`
	ErrorID          string `json:"error_id"`
	ErrorMessage     string `json:"error_message"`
	ErrorDescription string `json:"error_message_description"`
}

// message renders the envelope, preferring the detail over the code.
func (e apiError) message() string {
	msg := strings.TrimSpace(e.ErrorMessage)
	if msg == "" {
		msg = strings.TrimSpace(e.ErrorDescription)
	}
	if msg == "" {
		return ""
	}
	if id := strings.TrimSpace(e.ErrorID); id != "" {
		return msg + " (reference " + id + ")"
	}
	return msg
}

// summarise renders a failure response for a human.
//
// The body is parsed as the documented envelope and falls back to raw text. A
// request that never reached the API -- a proxy, a captive gateway, a
// corporate TLS interceptor -- answers with something else entirely, and that
// is exactly the case where seeing the body helps most.
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
			"than JSON -- something between this host and the API answered, "+
			"usually a proxy or a sign-in page", status)
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
// The bodies are quoted where they are quotable: the API rejecting a parameter
// says which one, and that message is the entire fix. What is never quoted is
// a *successful* response, which is somebody's estate.
func explainRequestFailure(status int, path string, body []byte) error {
	// A redirect is the most informative failure this integration gets. The
	// API does not redirect; something in front of it does.
	if status >= 300 && status < 400 {
		return fmt.Errorf("extremecloudiq: %s redirected instead of answering. "+
			"The API itself does not redirect, so something in front of it "+
			"did -- usually a proxy or a sign-in gateway that wants a browser "+
			"session. mcpd needs to reach the API directly, with its own "+
			"token, rather than through a gateway that only speaks to browsers",
			path)
	}

	switch status {
	case http.StatusBadRequest:
		return fmt.Errorf("extremecloudiq: %s would not accept that request: %s. "+
			"If it names a parameter, that parameter is the thing to fix; a "+
			"window given in the wrong unit is the usual cause, since every "+
			"time here is milliseconds since the epoch rather than seconds",
			path, summarise(status, body))

	case http.StatusUnauthorized:
		return fmt.Errorf("extremecloudiq: the API rejected our token. An "+
			"ExtremeCloud IQ API token expires at the time it was created "+
			"with, and an expired one is refused exactly like a revoked one -- "+
			"so check its expiry under Global Settings, API Token Management, "+
			"as well as whether it still exists. Reaching %s", redactURL(path))

	case http.StatusForbidden:
		return fmt.Errorf("extremecloudiq: not permitted to read %s. "+
			"ExtremeCloud IQ authorises by the role and the scopes the token "+
			"was issued with, so this is about how the token was created "+
			"rather than whether it is valid -- a token scoped to one part of "+
			"the account is refused here rather than returned empty", path)

	case http.StatusNotFound:
		return fmt.Errorf("extremecloudiq: %s returned nothing. Either it does "+
			"not exist in this account, or this token cannot see it -- a "+
			"device id from one account means nothing in another, and the API "+
			"reports both cases the same way", path)

	case http.StatusTooManyRequests:
		return fmt.Errorf("extremecloudiq: rate limited on %s. ExtremeCloud IQ "+
			"meters API calls per account per hour, so this is the account's "+
			"budget rather than this host's -- ask for fewer rows, or a "+
			"narrower window, rather than retrying immediately", path)

	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return fmt.Errorf("extremecloudiq: %s timed out. A listing over a large "+
			"estate walks a page at a time; narrow it with a location, a "+
			"device, or a shorter window", path)
	}

	if status >= 500 {
		return fmt.Errorf("extremecloudiq: the API failed on %s: %s. If it "+
			"carries a reference, that is the handle Extreme support asks for",
			path, summarise(status, body))
	}
	return fmt.Errorf("extremecloudiq: %s: %s", path, summarise(status, body))
}
