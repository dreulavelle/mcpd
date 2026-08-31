package bandwidth

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/spoked/mcpd/internal/plugins"
)

// What a messaging error code means.
//
// A table rather than a call, because Bandwidth does not serve this over the
// API -- it is a documentation page. Embedding it makes the lookup free and
// available when the API is not, at the cost of going stale when Bandwidth
// adds a code. That trade is stated to the caller rather than hidden: an
// unknown code says it is unknown to *this table*, and points at the page,
// instead of implying the code does not exist.
//
// Transcribed from https://dev.bandwidth.com/docs/messaging/errors/ on
// 2026-08-31.

// errorClass groups codes by who reported the failure and whether it is worth
// retrying, which is the first thing anybody asks after "what does it mean".
type errorClass struct {
	Category string
	// Transient reports whether the same message might succeed on a retry.
	//
	// Derived from the class rather than per code: Bandwidth's 5xxx are
	// service failures, its own and carriers', and those are the ones that
	// pass. A 4xxx is a decision about this message -- the number is wrong,
	// the campaign is not registered, the recipient opted out -- and sending
	// it again produces the same decision.
	Transient bool
	// Advice is what to do about the whole class, where there is something
	// useful to say beyond the code's own words.
	Advice string
}

var errorClasses = map[string]errorClass{
	"client": {
		Category:  "Bandwidth detected a client error",
		Transient: false,
		Advice: "Bandwidth refused this before it reached a carrier. The " +
			"message, the numbers or the account configuration has to change; " +
			"resending as-is produces the same answer.",
	},
	"carrier-client": {
		Category:  "The carrier reported a client error",
		Transient: false,
		Advice: "The carrier refused it. Retrying rarely helps — these are " +
			"decisions about the destination, the content or the registration " +
			"behind the sending number.",
	},
	"service": {
		Category:  "Bandwidth service failure",
		Transient: true,
		Advice: "A failure on Bandwidth's side. Retrying is reasonable, and a " +
			"run of these is worth raising with Bandwidth support.",
	},
	"carrier-service": {
		Category:  "The carrier reported a service failure",
		Transient: true,
		Advice: "A failure on the carrier's side. Retrying is reasonable; " +
			"persistent failures to one carrier are worth reporting.",
	},
	"ambiguous": {
		Category:  "Carrier error with an ambiguous cause",
		Transient: true,
		Advice: "The carrier did not say enough to tell a refusal from a " +
			"failure. Treat a single one as transient and a pattern as a " +
			"refusal worth investigating.",
	},
}

// messagingErrors is the published set, code to meaning and class.
var messagingErrors = map[int]struct {
	Meaning string
	Class   string
}{
	4001: {"service-not-allowed", "client"},
	4301: {"malformed-invalid-encoding", "client"},
	4302: {"malformed-invalid-from-number", "client"},
	4303: {"malformed-invalid-to-number", "client"},
	4350: {"malformed-for-destination", "client"},
	4360: {"message-not-sent-expiration-date-passed", "client"},
	4401: {"rejected-routing-error", "client"},
	4403: {"rejected-forbidden-from-number", "client"},
	4404: {"rejected-forbidden-to-number", "client"},
	4405: {"rejected-unallocated-from-number", "client"},
	4406: {"rejected-unallocated-to-number", "client"},
	4407: {"rejected-account-not-defined-from-number", "client"},
	4408: {"rejected-account-not-defined-to-number", "client"},
	4409: {"rejected-invalid-from-profile", "client"},
	4410: {"media-unavailable", "client"},
	4411: {"rejected-message-size-limit-exceeded", "client"},
	4412: {"media-content-invalid", "client"},
	4420: {"rejected-carrier-does-not-exist", "client"},
	4421: {"rejected-forbidden-no-destination", "client"},
	4431: {"rejected-forbidden-shortcode", "client"},
	4432: {"rejected-forbidden-country", "client"},
	4433: {"rejected-forbidden-tollfree", "client"},
	4434: {"rejected-forbidden-tollfree-for-recipient", "client"},
	4435: {"forbidden-too-many-recipients", "client"},
	4451: {"rejected-wrong-user-id", "client"},
	4452: {"rejected-wrong-application-id", "client"},
	4470: {"rejected-spam-detected", "client"},
	4475: {"destination-rejected-due-to-user-opt-out", "client"},
	4476: {"blocked-unregistered", "client"},
	4481: {"rejected-from-number-in-blacklist", "client"},
	4482: {"rejected-to-number-in-blacklist", "client"},
	4492: {"reject-emergency", "client"},
	4493: {"rejected-unauthorized", "client"},

	4700: {"invalid-service-type", "carrier-client"},
	4701: {"destination-service-unavailable", "carrier-client"},
	4702: {"destination-subscriber-unavailable", "carrier-client"},
	4710: {"media-unavailable", "carrier-client"},
	4711: {"rejected-message-size-limit-exceeded", "carrier-client"},
	4712: {"media-content-invalid", "carrier-client"},
	4720: {"invalid-destination-address", "carrier-client"},
	4721: {"destination-tn-deactivated", "carrier-client"},
	4730: {"no-route-to-destination-carrier", "carrier-client"},
	4735: {"too-many-recipients", "carrier-client"},
	4740: {"invalid-source-address-address", "carrier-client"},
	4750: {"destination-rejected-message", "carrier-client"},
	4751: {"destination-rejected-message-size-invalid", "carrier-client"},
	4752: {"destination-rejected-malformed", "carrier-client"},
	4753: {"destination-rejected-handset", "carrier-client"},
	4754: {"unsupported-channel", "carrier-client"},
	4770: {"destination-spam-detected", "carrier-client"},
	4771: {"rejected-shortened-url", "carrier-client"},
	4772: {"rejected-tn-blocked", "carrier-client"},
	4773: {"inactive-campaign", "carrier-client"},
	4774: {"provisioning-issue", "carrier-client"},
	4775: {"destination-rejected-due-to-user-opt-out", "carrier-client"},
	4780: {"volume-violation-tmo", "carrier-client"},
	4781: {"volume-violation-att", "carrier-client"},
	4785: {"volumetric-violation", "carrier-client"},
	4790: {"destination-rejected-sc-not-allowed", "carrier-client"},
	4791: {"destination-rejected-campaign-not-allowed", "carrier-client"},
	4792: {"destination-rejected-sc-not-provisioned", "carrier-client"},
	4793: {"destination-rejected-sc-expired", "carrier-client"},
	4794: {"destination-rejected-expired", "carrier-client"},
	4795: {"tfn-not-verified", "carrier-client"},

	5100: {"temporary-app-error", "service"},
	5101: {"temporary-app-shutdown", "service"},
	5106: {"impossible-to-route", "service"},
	5111: {"temporary-app-connection-closed", "service"},
	5201: {"temporary-rout-error-retries-exceeded", "service"},
	5211: {"temporary-app-error-app-busy", "service"},
	5220: {"temporary-store-error", "service"},
	5231: {"discarded-concatenation-timeout", "service"},
	5500: {"message-send-failed", "service"},
	5501: {"message-send-failed", "service"},
	5999: {"unknown-error", "service"},

	5600: {"destination-carrier-queue-full", "carrier-service"},
	5610: {"submit-sm-or-submit-multi-failed", "carrier-service"},
	5620: {"destination-app-error", "carrier-service"},
	5630: {"message-not-acknowledged", "carrier-service"},
	5650: {"destination-failed", "carrier-service"},

	9902: {"delivery-receipt-expired", "ambiguous"},
	9999: {"unknown-error", "ambiguous"},
}

func (p *Plugin) registerErrorCodeTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_error_reason",
		Title: "Explain a messaging error code",
		Description: "What a Bandwidth messaging error code means, who reported " +
			"it, and whether the message is worth sending again. Pair it with " +
			"the error_code on a result from search_messages — that field is a " +
			"number and nothing else, and the number is the whole explanation " +
			"of why a text did not arrive. Answered from a table held here, so " +
			"it costs nothing and works when the API does not.",
		Idempotent: true,
	}, p.getErrorReason)
}

// ErrorReasonInput names the code to explain.
type ErrorReasonInput struct {
	// Code is a string rather than an int so that "4720" and 4720 both work.
	// A model relaying a field it read elsewhere should not have to know which
	// this wanted, and a number arriving as text is not an error worth making
	// somebody debug.
	Code string `json:"code" jsonschema:"the error code, such as 4720"`
}

// ErrorReasonOutput explains one code.
type ErrorReasonOutput struct {
	Code    int    `json:"code"`
	Meaning string `json:"meaning"`
	// Category says who refused: Bandwidth, or the carrier beyond it. That
	// decides who can do anything about it.
	Category string `json:"category"`
	// Transient reports whether sending the same message again might work.
	Transient bool   `json:"transient"`
	Advice    string `json:"advice"`
	Note      string `json:"note,omitempty"`
}

func (p *Plugin) getErrorReason(_ context.Context, in ErrorReasonInput) (ErrorReasonOutput, error) {
	raw := strings.TrimSpace(in.Code)
	if raw == "" {
		return ErrorReasonOutput{}, fmt.Errorf("bandwidth: an error code is " +
			"required, such as 4720")
	}
	code, err := strconv.Atoi(raw)
	if err != nil {
		return ErrorReasonOutput{}, fmt.Errorf("bandwidth: %q is not an error "+
			"code; Bandwidth's messaging codes are numbers such as 4720", raw)
	}

	entry, known := messagingErrors[code]
	if !known {
		// Unknown to this table, which is not the same as unknown to
		// Bandwidth, and saying so is the difference between an operator
		// checking the page and an operator concluding the code is invented.
		return ErrorReasonOutput{
			Code: code,
			Note: fmt.Sprintf("%d is not in this table, which is Bandwidth's "+
				"published messaging codes as of 31 August 2026. It may be "+
				"newer, or a code from another part of the API: check "+
				"https://dev.bandwidth.com/docs/messaging/errors/%s", code,
				nearestClassHint(code)),
		}, nil
	}

	class := errorClasses[entry.Class]
	return ErrorReasonOutput{
		Code:      code,
		Meaning:   entry.Meaning,
		Category:  class.Category,
		Transient: class.Transient,
		Advice:    class.Advice,
	}, nil
}

// nearestClassHint says what a code's range implies when the code itself is
// not in the table.
//
// The ranges are Bandwidth's own grouping and they are stable even when
// individual codes are added, so the range still carries the one fact worth
// having: whether to try again.
func nearestClassHint(code int) string {
	switch {
	case code >= 4000 && code < 4700:
		return ". A 4000-series code is Bandwidth refusing the message, which " +
			"a retry will not change"
	case code >= 4700 && code < 5000:
		return ". A 4700-series code is a carrier refusing the message, which " +
			"a retry will not usually change"
	case code >= 5000 && code < 5600:
		return ". A 5000-series code is a Bandwidth service failure, which a " +
			"retry may clear"
	case code >= 5600 && code < 6000:
		return ". A 5600-series code is a carrier service failure, which a " +
			"retry may clear"
	}
	return ""
}

// knownErrorCodes lists the table's codes in order, for tests and for a
// message that needs to say how much is covered.
func knownErrorCodes() []int {
	out := make([]int, 0, len(messagingErrors))
	for code := range messagingErrors {
		out = append(out, code)
	}
	sort.Ints(out)
	return out
}
