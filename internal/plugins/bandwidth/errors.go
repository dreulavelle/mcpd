package bandwidth

import (
	"fmt"
	"net/http"
	"strings"
)

// roleFor names the Bandwidth role a path needs, for a 403.
//
// Bandwidth authorises by role rather than by endpoint, and its 403 says only
// that permission was refused. An operator then has to guess which of thirteen
// roles to tick, on a credential they may have to recreate to change. Naming
// the likely one turns that into a single edit.
//
// Best effort by design: a wrong guess costs a misleading sentence in an error
// that was already unhelpful, and the message says it is a guess.
func roleFor(path string) string {
	switch {
	case strings.Contains(path, "/phoneNumberLookup"):
		return "TN lookup"
	case strings.Contains(path, "/tollFreeVerification"), strings.Contains(path, "/tendlc"):
		return "Campaign management"
	case strings.Contains(path, "/portins"), strings.Contains(path, "/bulkPortins"):
		return "Porting"
	case strings.Contains(path, "/orders"), strings.Contains(path, "/availableNumbers"):
		return "Ordering"
	case strings.Contains(path, "/disconnects"), strings.Contains(path, "/discnumbers"):
		return "Disconnect"
	case strings.Contains(path, "/tnoptions"):
		return "Line features"
	case strings.Contains(path, "/messages"), strings.Contains(path, "/media"):
		return "Messaging insights"
	case strings.Contains(path, "/statistics"):
		return "Reporting"
	case strings.Contains(path, "/calls"), strings.Contains(path, "/conferences"),
		strings.Contains(path, "/recordings"), strings.Contains(path, "/transcriptions"):
		return "Basic access"
	case strings.Contains(path, "/endpoints"):
		return "Configuration"
	}
	return ""
}

// explainTokenFailure turns a failed credential exchange into a sentence that
// says what to change.
//
// Separate from a product API's failures because the causes are different and
// so are the fixes: nothing here is about what the credential may read, only
// about whether it is a credential at all.
func explainTokenFailure(status int, host string) error {
	switch status {
	case http.StatusUnauthorized, http.StatusBadRequest:
		return fmt.Errorf("bandwidth: %s refused the API credential. Check the "+
			"client id and secret, and check the secret has not passed the "+
			"expiry date set when it was created -- an expired secret is "+
			"refused exactly like a wrong one. A new secret is issued from the "+
			"same credential in the Bandwidth console; the client id does not "+
			"change", host)
	case http.StatusForbidden:
		return fmt.Errorf("bandwidth: %s accepted the credential but refused to "+
			"issue a token for it. That is usually a credential with no roles: "+
			"it must have at least one, and at least one account", host)
	case http.StatusTooManyRequests:
		return fmt.Errorf("bandwidth: %s is rate limiting the token endpoint. "+
			"Tokens are cached here and renewed about a minute before they "+
			"expire, so this normally means something else is exchanging the "+
			"same credential in a loop", host)
	}
	if status >= 500 {
		return fmt.Errorf("bandwidth: %s answered %d issuing a token; that is "+
			"Bandwidth's side, and retrying is the only remedy from here",
			host, status)
	}
	return fmt.Errorf("bandwidth: %s answered %d issuing a token", host, status)
}

// explainRequestFailure turns an upstream status into something an operator
// can act on.
//
// Bodies are summarised rather than passed through: Bandwidth's errors carry a
// request id worth keeping and prose that is not, and an error page from
// something that is not the API could carry anything at all.
func explainRequestFailure(status int, path string, body []byte) error {
	switch status {
	case http.StatusUnauthorized:
		return fmt.Errorf("bandwidth: the API rejected our token reading %s. "+
			"The token is minted here and renewed automatically, so this is "+
			"not an expiry to wait out -- it usually means the credential's "+
			"secret was rotated or revoked in the Bandwidth console", path)

	case http.StatusBadRequest:
		// The body leads here. Bandwidth says which parameter it objected to,
		// and no guess made from the path beats being told.
		return fmt.Errorf("bandwidth: %s rejected the request: %s", path,
			summarise(status, body))

	case http.StatusForbidden:
		msg := fmt.Sprintf("bandwidth: not permitted to read %s. Bandwidth "+
			"authorises by the roles and accounts the API credential was "+
			"created with, so this is about how the credential was made "+
			"rather than whether it is valid", path)
		// What Bandwidth said, before what this package guesses. A 403 body
		// names the missing entitlement often enough to be worth quoting, and
		// it is an error description rather than anybody's data.
		if detail := summarise(status, body); detail != "" && len(body) > 0 {
			msg += ". Bandwidth said: " + detail
		}
		if role := roleFor(path); role != "" {
			msg += fmt.Sprintf(". If the credential is missing a role, this "+
				"read most likely wants %q", role)
		}
		// Said plainly, because the opposite advice wastes the most time. A
		// role guess reads as a diagnosis, and somebody whose credential
		// already has every role will go looking for a role that does not
		// exist. A 403 on a credential that is fully scoped is not about the
		// credential at all: it is Bandwidth saying this account is not
		// provisioned for the product behind that path.
		return fmt.Errorf("%s. If it already has every role, this is not a "+
			"role: a fully scoped credential that is still refused means the "+
			"account is not enabled for the product this path belongs to, "+
			"which is something only Bandwidth can turn on", msg)

	case http.StatusNotFound:
		return fmt.Errorf("bandwidth: %s returned nothing. Either it does not "+
			"exist in this account, or this credential's accounts do not "+
			"include the one it belongs to -- an id from one account means "+
			"nothing in another, and the API reports both cases the same way",
			path)

	case http.StatusTooManyRequests:
		return fmt.Errorf("bandwidth: %s is rate limited. Bandwidth meters per "+
			"account, so another application on the same account can spend "+
			"this budget too", path)
	}

	if status >= 500 {
		return fmt.Errorf("bandwidth: %s answered %d. %s", path, status,
			summarise(status, body))
	}
	return fmt.Errorf("bandwidth: %s answered %d. %s", path, status,
		summarise(status, body))
}

// summarise reduces an upstream body to the part worth quoting.
//
// A bounded slice of it, because the alternative is copying whatever an
// intermediary chose to return -- an HTML error page, or a body carrying
// somebody else's data -- into this host's log.
func summarise(status int, body []byte) string {
	const limit = 200
	text := strings.TrimSpace(string(body))
	if text == "" {
		return fmt.Sprintf("The response had no body (%d).", status)
	}
	if len(text) > limit {
		text = text[:limit] + "…"
	}
	// Collapsed to one line: a multi-line body turns one log record into
	// several, and the ones after the first have no context on them.
	return strings.Join(strings.Fields(text), " ")
}
