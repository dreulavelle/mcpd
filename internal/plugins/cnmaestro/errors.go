package cnmaestro

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// maxErrorBody bounds how much of a failure response is read. Enough to
// contain a message, not enough for a stack trace or a page of HTML from a
// proxy that answered instead of the API.
const maxErrorBody = 8 << 10

// apiError is cnMaestro's error envelope.
type apiError struct {
	Error struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error"`
	Message string `json:"message"`
}

// message extracts whatever the body actually said.
func (e apiError) message() string {
	if m := strings.TrimSpace(e.Error.Message); m != "" {
		return m
	}
	return strings.TrimSpace(e.Message)
}

// summarise renders a failure response for a human.
//
// The body is parsed as the documented envelope and falls back to raw text,
// because a request that never reached the API — a proxy, a captive portal, a
// load balancer — answers with something else entirely, and that is exactly
// the case where seeing the body helps most.
func summarise(status int, body []byte) string {
	var e apiError
	if err := json.Unmarshal(body, &e); err == nil {
		if msg := e.message(); msg != "" {
			return fmt.Sprintf("%s (HTTP %d)", msg, status)
		}
	}
	text := strings.TrimSpace(string(body))
	if len(text) > 200 {
		text = text[:200] + "…"
	}
	if text == "" {
		return fmt.Sprintf("HTTP %d with an empty body", status)
	}
	return fmt.Sprintf("HTTP %d: %s", status, text)
}

// redactURL strips any credential embedded in a URL before it reaches a log
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
// on, and a model can repeat without leaking anything.
func explainRequestFailure(status int, path string, body []byte) error {
	switch status {
	case http.StatusUnauthorized:
		return fmt.Errorf("cnmaestro: the API rejected our credentials. " +
			"They may have been revoked or rotated in cnMaestro")
	case http.StatusForbidden:
		return fmt.Errorf("cnmaestro: not permitted to read %s. "+
			"Check the API client's role, and managed_account if this is an MSP account", path)
	case http.StatusNotFound:
		return fmt.Errorf("cnmaestro: %s does not exist upstream", path)
	case http.StatusTooManyRequests:
		return fmt.Errorf("cnmaestro: rate limited by the API. " +
			"Lower requests_per_second, or narrow the request")
	}
	if status >= 500 {
		return fmt.Errorf("cnmaestro: the API failed on %s: %s", path, summarise(status, body))
	}
	return fmt.Errorf("cnmaestro: %s: %s", path, summarise(status, body))
}
