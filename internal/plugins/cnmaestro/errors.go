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

// oauthProblem extracts the OAuth-style error from a token rejection.
//
// The token endpoint does not use the envelope the rest of the API uses: it
// answers {"error":"invalid_client"}, where "error" is a string rather than an
// object. Unmarshalling that into apiError fails outright, which is why a
// token failure used to carry no upstream detail at all -- and "check your
// credentials" is a poor thing to say to someone whose credentials are right.
func oauthProblem(body []byte) string {
	var e struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &e); err == nil {
		code, desc := strings.TrimSpace(e.Error), strings.TrimSpace(e.Description)
		switch {
		case code != "" && desc != "":
			return code + ": " + desc
		case code != "":
			return code
		case desc != "":
			return desc
		}
	}
	// The envelope form, for a deployment that answers with it here.
	var env apiError
	if err := json.Unmarshal(body, &env); err == nil {
		return env.message()
	}
	return ""
}

// mainAccountName mirrors MainAccount, kept here so errors.go does not depend
// on declaration order in config.go.
const mainAccountName = MainAccount

// mentionsManagedAccount reports the API's own message when a failure is about
// the managed account, so a generic status can be turned into a specific
// explanation. Matching the message rather than the status is deliberate: the
// same statuses arrive for entirely unrelated reasons.
func mentionsManagedAccount(body []byte) string {
	var e apiError
	if err := json.Unmarshal(body, &e); err != nil {
		return ""
	}
	msg := e.message()
	if strings.Contains(strings.ToLower(msg), "managed_account") ||
		strings.Contains(strings.ToLower(msg), "msp feature") {
		return msg
	}
	return ""
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
	// The managed_account failures are worth naming individually. All three
	// arrive as a bare status against an ordinary read, and each has a
	// different fix that the status alone does not suggest.
	if msg := mentionsManagedAccount(body); msg != "" {
		switch status {
		case http.StatusBadRequest:
			return fmt.Errorf("cnmaestro: this account does not have the MSP "+
				"feature, so the only managed_account it accepts is %q. "+
				"Clear managed_account, or set it to that", mainAccountName)
		case http.StatusForbidden:
			return fmt.Errorf("cnmaestro: that managed account is disabled. " +
				"cnmaestro_list_managed_accounts reports each tenant's status; a " +
				"disabled tenant can own visible data and still reject every " +
				"call naming it")
		case http.StatusNotFound:
			return fmt.Errorf("cnmaestro: no managed account by that name. "+
				"Matching is exact and case-sensitive -- cnmaestro_list_managed_accounts "+
				"lists the tenants, and the Main Account is %q", mainAccountName)
		}
	}

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
