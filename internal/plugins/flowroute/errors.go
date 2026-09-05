package flowroute

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// maxErrorBody bounds how much of a failure response is read. Enough for the
// JSON:API envelope, not enough for a page of HTML from whatever answered
// instead of the API.
const maxErrorBody = 8 << 10

// apiError is one entry of Flowroute's JSON:API error envelope:
//
//	{"errors":[{"detail":"No such port order","id":"2faefde3-…",
//	            "status":404,"title":"Resource not found"}]}
//
// Status is deliberately not an int. Flowroute sends a number for an error
// about a resource and a *string* for one about a URL it does not serve, and a
// struct that insists on the number fails to parse exactly the response whose
// shape carries the most information.
type apiError struct {
	Detail json.RawMessage `json:"detail"`
	ID     string          `json:"id"`
	Status json.RawMessage `json:"status"`
	Title  string          `json:"title"`
}

// errorEnvelope is what a failed Flowroute call answers with.
type errorEnvelope struct {
	Errors []apiError `json:"errors"`
}

// ErrNotFound reports a resource that is genuinely absent, as opposed to a
// path this package should not have asked for.
var ErrNotFound = errors.New("flowroute: not found")

// ErrBadPath reports a 404 that names no resource: Flowroute does not serve
// the URL at all.
//
// Separate from ErrNotFound on purpose. "No such port order" is an answer, and
// a tool should say so; "the requested URL was not found on the server" means
// this package built a path Flowroute has never served, which is a bug here
// and must not be reported to somebody as an empty result.
var ErrBadPath = errors.New("flowroute: the API does not serve that path")

// routingFailure reports whether a 404 body is the shape Flowroute uses for a
// URL it does not serve.
//
// The tell is the type of `status`. A resource error carries the number 404
// alongside a title and a detail; a routing error carries the whole HTTP
// status line as a string and nothing else.
func routingFailure(body []byte) bool {
	var env errorEnvelope
	if err := json.Unmarshal(body, &env); err != nil || len(env.Errors) == 0 {
		// A 404 that is not the envelope at all did not come from the API.
		// Treating it as a routing failure is the safer reading: it says the
		// address is wrong rather than that somebody's port order is missing.
		return true
	}
	for _, e := range env.Errors {
		var s string
		if json.Unmarshal(e.Status, &s) == nil && strings.TrimSpace(e.Title) == "" {
			return true
		}
	}
	return false
}

// summarise renders a failure response for a human.
//
// The body is parsed as the JSON:API envelope and falls back to raw text. A
// request that never reached Flowroute -- a proxy, a captive portal, a
// corporate TLS interceptor -- answers with something else entirely, and that
// is exactly the case where seeing the shape of the body helps most.
func summarise(status int, body []byte) string {
	var env errorEnvelope
	if err := json.Unmarshal(body, &env); err == nil && len(env.Errors) > 0 {
		parts := make([]string, 0, len(env.Errors))
		for _, e := range env.Errors {
			if msg := describe(e); msg != "" {
				parts = append(parts, msg)
			}
		}
		if len(parts) > 0 {
			return fmt.Sprintf("%s (HTTP %d)", strings.Join(parts, "; "), status)
		}
	}

	text := strings.TrimSpace(string(body))
	lower := strings.ToLower(text)
	if strings.HasPrefix(lower, "<!doctype") || strings.HasPrefix(lower, "<html") {
		return fmt.Sprintf("HTTP %d, and the response was an HTML page rather than "+
			"the API's JSON -- something between here and Flowroute answered "+
			"instead of the API", status)
	}
	if len(text) > 200 {
		text = text[:200] + "…"
	}
	if text == "" {
		return fmt.Sprintf("HTTP %d with an empty body", status)
	}
	return fmt.Sprintf("HTTP %d: %s", status, text)
}

// describe renders one error entry.
//
// Detail is also polymorphic: a validation failure sends an object keyed by
// field -- {"start_date":["Missing data for required field."]} -- where
// everything else sends a sentence. Both are rendered, because the field name
// is the whole of what makes a 422 actionable.
func describe(e apiError) string {
	var parts []string
	if t := strings.TrimSpace(e.Title); t != "" {
		parts = append(parts, t)
	}
	if d := renderDetail(e.Detail); d != "" {
		parts = append(parts, d)
	}
	if len(parts) == 0 {
		var s string
		if json.Unmarshal(e.Status, &s) == nil {
			return strings.TrimSpace(s)
		}
		return ""
	}
	return strings.Join(parts, ": ")
}

// renderDetail flattens the two shapes `detail` arrives in.
func renderDetail(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var fields map[string][]string
	if json.Unmarshal(raw, &fields) == nil && len(fields) > 0 {
		names := make([]string, 0, len(fields))
		for k := range fields {
			names = append(names, k)
		}
		sortStrings(names)
		out := make([]string, 0, len(names))
		for _, k := range names {
			out = append(out, k+" "+strings.Join(fields[k], " "))
		}
		return strings.Join(out, "; ")
	}
	return strings.TrimSpace(string(raw))
}

// explainRequestFailure turns a failed read into a sentence that says what to
// do about it.
//
// The status codes Flowroute uses mean specific things here, and naming them
// is the difference between an error a model can act on and one it retries
// three times.
func explainRequestFailure(status int, body []byte) error {
	msg := summarise(status, body)
	switch status {
	case 401:
		return fmt.Errorf("flowroute refused the credential (401): check the access "+
			"key and secret key on the Plugins page -- Flowroute sends both on "+
			"every request, so a rotated key stops every read at once. %s", msg)
	case 403:
		return fmt.Errorf("flowroute refused the request (403): the credential is "+
			"valid but is not permitted this read. %s", msg)
	case 404:
		if routingFailure(body) {
			return fmt.Errorf("%w: %s", ErrBadPath, msg)
		}
		return fmt.Errorf("%w: %s", ErrNotFound, msg)
	case 422:
		return fmt.Errorf("flowroute rejected the request as malformed (422): %s", msg)
	case 429:
		return fmt.Errorf("flowroute is rate limiting this account (429): ask for "+
			"less at a time, or wait and retry. %s", msg)
	}
	if status >= 500 {
		return fmt.Errorf("flowroute failed to answer (HTTP %d); this is their side "+
			"rather than the request. %s", status, msg)
	}
	return fmt.Errorf("flowroute answered %s", msg)
}

// sortStrings is sort.Strings, kept local so error rendering pulls in nothing.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
