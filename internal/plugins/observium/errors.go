package observium

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// maxErrorBody bounds how much of a failure response is read. Enough for a
// message, not enough for a PHP stack trace or a page of HTML from the reverse
// proxy that answered instead of Observium.
const maxErrorBody = 8 << 10

// apiError is Observium's failure envelope. It is the success envelope with
// status set to "failed", which is why the same struct carries both.
type apiError struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	// Errors appears on validation failures, keyed by field.
	Errors map[string]string `json:"errors"`
}

// message extracts whatever the body actually said, preferring the specific
// field error over the generic sentence above it.
func (e apiError) message() string {
	if m := strings.TrimSpace(e.Message); m != "" {
		return m
	}
	for field, msg := range e.Errors {
		if msg = strings.TrimSpace(msg); msg != "" {
			return field + ": " + msg
		}
	}
	return ""
}

// summarise renders a failure response for a human.
//
// The body is parsed as the documented envelope and falls back to raw text.
// A request that never reached Observium -- a proxy, an expired session
// redirect, an Apache error page -- answers with something else entirely, and
// that is exactly the case where seeing the body helps most.
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
	if strings.HasPrefix(strings.ToLower(text), "<!doctype") ||
		strings.HasPrefix(strings.ToLower(text), "<html") {
		return fmt.Sprintf("HTTP %d, and the response was an HTML page rather "+
			"than JSON -- the address may be reaching a web server or a login "+
			"page rather than the API", status)
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
// Observium's statuses carry more meaning than most, because the API is
// permission-aware in a way that collapses several different problems onto one
// code. 404 in particular is not only "no such thing": a device the token's
// account cannot read is reported as absent rather than forbidden, which is
// good security and a confusing thing to be told.
func explainRequestFailure(status int, path string, body []byte) error {
	// A redirect is the most informative failure this integration gets, and it
	// has one overwhelmingly likely cause. The API is a subscription feature;
	// a Community Edition installation has no /api/v0 at all and bounces the
	// request to its sign-in page. Saying "unexpected status 302" there would
	// be true and useless -- the operator has picked the wrong edition, and
	// there is a working setting one dropdown away.
	if status >= 300 && status < 400 {
		return fmt.Errorf("observium: %s redirected instead of answering, "+
			"which is what an Observium with no API does -- it is sending the "+
			"request to its sign-in page. The REST API is a subscription "+
			"feature, so if this is Community Edition, change \"Which "+
			"Observium is this\" to Community Edition and give it the database "+
			"connection instead. If you do have a subscription, check that "+
			"$config['api']['enable'] = TRUE is set in config.php", path)
	}

	switch status {
	case http.StatusUnauthorized:
		return fmt.Errorf("observium: the API rejected our credentials. The " +
			"token may have been revoked, or the account's password changed")

	case http.StatusForbidden:
		return fmt.Errorf("observium: not permitted to read %s. Observium gates "+
			"endpoints on the account's user level -- VLANs need level 7 and "+
			"maintenance windows need level 8, so a read-only token on a "+
			"low-level account is refused here rather than returning less", path)

	case http.StatusNotFound:
		return fmt.Errorf("observium: %s returned nothing. Either it does not "+
			"exist, or this account is not permitted to see it -- Observium "+
			"reports an entity outside a user's permissions as absent rather "+
			"than forbidden, so these are indistinguishable from here", path)

	case http.StatusTooManyRequests:
		// Observium throttles failed *authentication* with the same status it
		// uses for load. Conflating them would have somebody lowering their
		// request rate to fix a wrong password.
		return fmt.Errorf("observium: refused with 429. If the credentials were "+
			"recently changed this is the authentication throttle, which trips "+
			"after api.auth_fail_limit failures and clears on its own; "+
			"otherwise lower requests_per_second (currently reaching %s)",
			redactURL(path))
	}

	if status >= 500 {
		return fmt.Errorf("observium: the API failed on %s: %s", path,
			summarise(status, body))
	}
	return fmt.Errorf("observium: %s: %s", path, summarise(status, body))
}

// explainEnvelopeFailure reports a 200 response that says it failed.
//
// Observium answers some errors with HTTP 200 and status "failed" in the body.
// A client that only checks the status code treats those as success and hands
// the model an empty collection, which reads as "there are none" rather than
// "the question was refused".
func explainEnvelopeFailure(path, message string) error {
	if message == "" {
		message = "no reason given"
	}
	return fmt.Errorf("observium: %s was refused: %s", path, message)
}
