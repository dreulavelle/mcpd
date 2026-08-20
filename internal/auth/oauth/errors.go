package oauth

import (
	"errors"
	"fmt"
	"net/http"
)

// RFC 6749 section 5.2 error codes. These reach the client verbatim, so they
// are deliberately coarse: distinguishing "unknown code" from "expired code"
// hands an attacker an oracle.
const (
	ErrCodeInvalidRequest       = "invalid_request"
	ErrCodeInvalidClient        = "invalid_client"
	ErrCodeInvalidGrant         = "invalid_grant"
	ErrCodeUnauthorizedClient   = "unauthorized_client"
	ErrCodeUnsupportedGrantType = "unsupported_grant_type"
	ErrCodeInvalidScope         = "invalid_scope"
	ErrCodeAccessDenied         = "access_denied"
	ErrCodeServerError          = "server_error"
)

// Sentinel errors for internal branching.
var (
	// ErrInvalidGrant covers every way a code, token, or verifier fails to
	// check out. It is one error on purpose.
	ErrInvalidGrant = errors.New("oauth: invalid grant")
	// ErrClientNotFound reports an unknown or disabled client.
	ErrClientNotFound = errors.New("oauth: client not found")
	// ErrUserNotFound reports an unknown or disabled user.
	ErrUserNotFound = errors.New("oauth: user not found")
	// ErrTokenReuse reports a refresh token replayed after rotation, which
	// indicates the token was captured. The whole lineage is revoked.
	ErrTokenReuse = errors.New("oauth: refresh token reuse detected")
)

// Error is an OAuth protocol error rendered to the client.
type Error struct {
	Code        string `json:"error"`
	Description string `json:"error_description,omitempty"`
	// status is the HTTP status to send; it is not serialised.
	status int
}

func (e *Error) Error() string {
	if e.Description == "" {
		return e.Code
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Description)
}

// Status returns the HTTP status for this error.
func (e *Error) Status() int {
	if e.status != 0 {
		return e.status
	}
	return http.StatusBadRequest
}

func oauthErr(code, description string, status int) *Error {
	return &Error{Code: code, Description: description, status: status}
}

// Common protocol errors.
func errInvalidRequest(desc string) *Error {
	return oauthErr(ErrCodeInvalidRequest, desc, http.StatusBadRequest)
}

func errInvalidClient(desc string) *Error {
	return oauthErr(ErrCodeInvalidClient, desc, http.StatusUnauthorized)
}

// errInvalidGrant carries no detail by design. Every failed exchange looks
// identical to the client, so a probe cannot learn whether a code existed, had
// expired, was already used, or belonged to another client.
func errInvalidGrant() *Error {
	return oauthErr(ErrCodeInvalidGrant,
		"the authorization grant is invalid, expired, or already used",
		http.StatusBadRequest)
}

func errServer(desc string) *Error {
	return oauthErr(ErrCodeServerError, desc, http.StatusInternalServerError)
}
