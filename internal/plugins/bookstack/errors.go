package bookstack

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// maxErrorBody bounds how much of a failure response is read. Enough for the
// error envelope, not enough for a page of HTML from whatever answered instead
// of the API.
const maxErrorBody = 8 << 10

// apiError is BookStack's failure envelope:
//
//	{"error":{"message":"No authorization token found on the request","code":401}}
type apiError struct {
	Error struct {
		Message string `json:"message"`
		Code    int    `json:"code"`
	} `json:"error"`
}

// validationError is the other shape: a 422 keyed by the field that failed.
//
//	{"error":{"message":"The given data was invalid.",
//	          "validation":{"name":["The name field is required."]},"code":422}}
type validationError struct {
	Error struct {
		Message    string              `json:"message"`
		Validation map[string][]string `json:"validation"`
		Code       int                 `json:"code"`
	} `json:"error"`
}

// ErrNotFound reports a resource that is not there.
//
// Separate from an ordinary failure because it is an answer rather than a
// fault: a page that has been deleted, an id that never existed. Tools turn it
// into a sentence naming what was looked for.
var ErrNotFound = errors.New("bookstack: not found")

// ErrForbidden reports that the token's user is not permitted this.
//
// Its own sentinel because it is the failure an operator meets most, and the
// fix is never in mcpd: BookStack applies the permissions of the user the
// token belongs to, so the answer is always either a different token or a
// change to that user's role.
var ErrForbidden = errors.New("bookstack: the token's user is not permitted this")

// summarise renders a failure response for a human.
func summarise(status int, body []byte) string {
	var v validationError
	if err := json.Unmarshal(body, &v); err == nil && len(v.Error.Validation) > 0 {
		fields := make([]string, 0, len(v.Error.Validation))
		for name, msgs := range v.Error.Validation {
			fields = append(fields, name+": "+strings.Join(msgs, " "))
		}
		sort.Strings(fields)
		return fmt.Sprintf("%s (HTTP %d)", strings.Join(fields, "; "), status)
	}
	var e apiError
	if err := json.Unmarshal(body, &e); err == nil && strings.TrimSpace(e.Error.Message) != "" {
		return fmt.Sprintf("%s (HTTP %d)", strings.TrimSpace(e.Error.Message), status)
	}

	text := strings.TrimSpace(string(body))
	lower := strings.ToLower(text)
	if strings.HasPrefix(lower, "<!doctype") || strings.HasPrefix(lower, "<html") {
		return fmt.Sprintf("HTTP %d, and the response was an HTML page rather than "+
			"the API's JSON -- the address may be reaching a reverse proxy or a "+
			"sign-in page rather than BookStack's API", status)
	}
	if len(text) > 200 {
		text = text[:200] + "…"
	}
	if text == "" {
		return fmt.Sprintf("HTTP %d with an empty body", status)
	}
	return fmt.Sprintf("HTTP %d: %s", status, text)
}

// explainRequestFailure turns a failed call into a sentence that says what to
// do about it.
//
// The status codes BookStack uses mean specific things here, and naming them
// is the difference between an error a model can act on and one it retries
// three times.
func explainRequestFailure(status int, body []byte) error {
	msg := summarise(status, body)
	switch status {
	case 401:
		return fmt.Errorf("bookstack refused the token (401): check the token ID and "+
			"secret on the Plugins page. A token that has been revoked or has "+
			"passed its expiry stops every read and every change at once. %s", msg)
	case 403:
		// Not "you are not allowed" in the abstract: BookStack answers 403 for
		// a role that lacks the permission *and* for content the user cannot
		// see, and the second reads as the first.
		return fmt.Errorf("%w (403): the token belongs to a BookStack user, and this "+
			"is either a permission that user's role does not hold or content it "+
			"cannot see. %s", ErrForbidden, msg)
	case 404:
		return fmt.Errorf("%w: %s", ErrNotFound, msg)
	case 422:
		return fmt.Errorf("bookstack rejected the request as invalid (422): %s", msg)
	case 429:
		// BookStack throttles the API per minute, and the plugin's own limiter
		// is meant to stay under it. Reaching this means something else is
		// using the same instance.
		return fmt.Errorf("bookstack is throttling this token (429): its API limit "+
			"is per minute and something else may be using the same instance. "+
			"Lower requests per second on the Plugins page, or wait. %s", msg)
	}
	if status >= 500 {
		return fmt.Errorf("bookstack failed to answer (HTTP %d); this is the "+
			"instance rather than the request. %s", status, msg)
	}
	return fmt.Errorf("bookstack answered %s", msg)
}

// isNotFound reports the absent-resource failure.
func isNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

// isForbidden reports the permission failure, for a caller that wants to treat
// it as an answer rather than a fault.
func isForbidden(err error) bool { return errors.Is(err, ErrForbidden) }
