package threecx

import (
	"encoding/json"
	"fmt"
	"strings"
)

// maxErrorBody bounds how much of a failure response is read. Enough for the
// OData envelope, not enough for a page of HTML from whatever answered instead
// of the API.
const maxErrorBody = 8 << 10

// odataError is the failure envelope 3CX's OData stack sends:
//
//	{"error":{"code":"","message":"The query specified in the URI is not valid.
//	 Could not find a property named 'Nope' on type 'Pbx.User'.","details":[]}}
//
// The code is empty in every refusal seen from a live system, so the message
// is the whole of what there is to read.
type odataError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Details []struct {
			Message string `json:"message"`
			Target  string `json:"target"`
		} `json:"details"`
	} `json:"error"`
}

// summarise renders a failure response for a human.
//
// The body is parsed as the OData envelope and falls back to raw text. A
// request that never reached 3CX -- a proxy, a firewall, a sign-in page --
// answers with something else entirely, and that is exactly the case where
// seeing the shape of the body helps most.
func summarise(status int, body []byte) string {
	var e odataError
	if err := json.Unmarshal(body, &e); err == nil && strings.TrimSpace(e.Error.Message) != "" {
		msg := strings.TrimSpace(e.Error.Message)
		if len(e.Error.Details) > 0 && e.Error.Details[0].Message != "" {
			msg += " (" + strings.TrimSpace(e.Error.Details[0].Message) + ")"
		}
		return fmt.Sprintf("%s (HTTP %d)", msg, status)
	}

	text := strings.TrimSpace(string(body))
	lower := strings.ToLower(text)
	if strings.HasPrefix(lower, "<!doctype") || strings.HasPrefix(lower, "<html") {
		return fmt.Sprintf("HTTP %d, and the response was an HTML page rather than "+
			"the API's JSON -- the address may be reaching a web server, a proxy "+
			"or a sign-in page rather than the phone system", status)
	}
	if len(text) > 200 {
		text = text[:200] + "…"
	}
	if text == "" {
		return fmt.Sprintf("HTTP %d with an empty body", status)
	}
	return fmt.Sprintf("HTTP %d: %s", status, text)
}

// explainRequestFailure turns a failed read into a sentence that says what to
// do about it.
//
// The status codes 3CX uses mean specific things here, and naming them is the
// difference between an error a model can act on and one it retries three
// times. 401 is a token the PBX no longer accepts, which the client handles
// before this is reached; 403 is the extension lacking the system owner role,
// which is a configuration fix; 404 on an allow-listed path is a build too old
// to offer it.
func explainRequestFailure(status int, path string, body []byte) error {
	switch status {
	case 401:
		return fmt.Errorf("3cx: the phone system no longer accepts our sign-in " +
			"(HTTP 401); the token was refreshed and refused again, so check the " +
			"extension and password on the Plugins page")
	case 403:
		return fmt.Errorf("3cx: the phone system refused to list %s (HTTP 403). "+
			"The extension this integration signs in as does not have the System "+
			"Owner role, which every read here needs -- grant it in the 3CX console "+
			"under Users, or sign in as one that has it", path)
	case 404:
		return fmt.Errorf("3cx: this phone system does not offer %s (HTTP 404); "+
			"it may be an older build than the v20 API this integration reads", path)
	case 429:
		return fmt.Errorf("3cx: the phone system is rate limiting us (HTTP 429); " +
			"wait a few seconds before asking again")
	}
	if status >= 500 {
		return fmt.Errorf("3cx: the phone system failed answering %s: %s", path, summarise(status, body))
	}
	return fmt.Errorf("3cx: the phone system refused %s: %s", path, summarise(status, body))
}
