package threecx

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
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

// notOffered is a read this build of the phone system does not serve.
//
// A type rather than a sentence, because "this build does not have it" and
// "something went wrong reading it" call for different answers from a tool: a
// question about audio can still be answered from the endpoints that do exist,
// and saying so is the difference between an incomplete answer a reader can
// trust and one that looks complete but quietly is not.
type notOffered struct{ path string }

func (n *notOffered) Error() string {
	return fmt.Sprintf("3cx: this phone system does not offer %s (HTTP 404); "+
		"it may be an older build than the v20 API this integration reads", n.path)
}

// missing reports whether err is a read the phone system does not serve.
func missing(err error) bool {
	var n *notOffered
	return errors.As(err, &n)
}

// unknownProperty is a $select naming a field this build of 3CX does not have.
//
// A type rather than a sentence, for the same reason as notOffered: "this
// build does not carry that field" and "the query was wrong" need different
// answers. 3CX refuses the whole read when one named property is unknown, so a
// field added in a later build takes every queue on an older one down with it
// -- which is what ComfortPrompts did. Naming the property is what lets the
// read be made again without it.
type unknownProperty struct {
	name string
	typ  string
	text string
}

func (u *unknownProperty) Error() string { return u.text }

// missingProperty reports the property an error says does not exist, if that
// is what it says.
func missingProperty(err error) (string, bool) {
	var u *unknownProperty
	if errors.As(err, &u) {
		return u.name, true
	}
	return "", false
}

// unknownPropertyPattern matches the OData refusal 3CX sends for a $select
// naming a property a type does not have:
//
//	The query specified in the URI is not valid. Could not find a property
//	named 'ComfortPrompts' on type 'Pbx.Queue'.
//
// Matched on the message rather than on a code because the code is empty in
// every refusal a live system has sent.
var unknownPropertyPattern = regexp.MustCompile(
	`Could not find a property named '([^']+)' on type '([^']+)'`)

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
		return &notOffered{path: path}
	case 400:
		// A named property this build does not have. Reported as itself so the
		// read can be made again without it; the sentence is unchanged, so a
		// 400 that is not this still reads as it always did.
		text := fmt.Sprintf("3cx: the phone system refused %s: %s", path, summarise(status, body))
		if m := unknownPropertyPattern.FindSubmatch(body); m != nil {
			return &unknownProperty{name: string(m[1]), typ: string(m[2]), text: text}
		}
		return errors.New(text)
	case 429:
		return fmt.Errorf("3cx: the phone system is rate limiting us (HTTP 429); " +
			"wait a few seconds before asking again")
	}
	// A gateway status is the phone system's own front end giving up on its
	// back end, not the back end failing. It matters because the two call for
	// opposite things: a 500 is worth reporting, and this is worth asking again
	// for less. 3CX answers it with an HTML error page, which the generic
	// summary reads as "the address may be reaching a web server rather than
	// the phone system" -- true of a misconfigured host and badly wrong here,
	// where the address is right and the query is simply too wide. One
	// customer's CallHistoryView answers a one-hour window in 15 seconds, a
	// whole day in 50, and is cut off at 60 with this status; no timeout on our
	// side can change that, because the limit is theirs.
	if status == 502 || status == 503 || status == 504 {
		return fmt.Errorf("3cx: the phone system took too long over %s and its own "+
			"front end cut the request off (HTTP %d). The address is fine and nothing "+
			"is broken -- the query is more than this system can answer in the time it "+
			"allows itself. Ask for less: a narrower time window is what helps most, "+
			"then fewer rows. Filtering by extension or number alone does not, because "+
			"the phone system reads the whole history before it filters", path, status)
	}
	if status >= 500 {
		return fmt.Errorf("3cx: the phone system failed answering %s: %s", path, summarise(status, body))
	}
	return fmt.Errorf("3cx: the phone system refused %s: %s", path, summarise(status, body))
}
