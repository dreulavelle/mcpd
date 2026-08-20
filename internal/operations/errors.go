package operations

import "errors"

// Sentinel errors for conditions callers routinely branch on.
var (
	// ErrNotFound reports that no operation exists with the requested ID.
	ErrNotFound = errors.New("operation not found")

	// ErrClaimLost reports that a guarded claim matched zero rows because
	// another worker won the race, or the operation left the claimable state.
	// It is an expected outcome under concurrency, not a failure.
	ErrClaimLost = errors.New("execution claim lost")

	// ErrIndeterminate is returned by a plugin's Apply when it cannot
	// establish whether the upstream mutation took effect. Returning it moves
	// the operation to StateIndeterminate rather than StateFailed, which
	// suppresses any retry.
	ErrIndeterminate = errors.New("upstream outcome indeterminate")
)

// Typed error codes persisted on an operation and surfaced to callers. They are
// stable identifiers, not prose, so clients and dashboards can branch on them.
const (
	CodeInvalidTransition   = "INVALID_TRANSITION"
	CodePayloadMismatch     = "PAYLOAD_MISMATCH"
	CodePreconditionChanged = "PRECONDITION_CHANGED"
	CodeProposalExpired     = "PROPOSAL_EXPIRED"
	CodeApprovalExpired     = "APPROVAL_EXPIRED"
	CodeSelfApproval        = "SELF_APPROVAL_FORBIDDEN"
	CodeIdentityIndistinct  = "IDENTITY_INDISTINCT"
	CodeNotAuthorized       = "NOT_AUTHORIZED"
	CodeUpstreamFailed      = "UPSTREAM_FAILED"
	CodeIndeterminate       = "INDETERMINATE"
	CodeVerificationFailed  = "VERIFICATION_FAILED"
	CodeLeaseExpired        = "LEASE_EXPIRED"
)

// TransitionError reports a rejected state change. It carries both states so
// callers can render a useful message without re-deriving context.
type TransitionError struct {
	From   OperationState
	To     OperationState
	Reason string
}

func (e *TransitionError) Error() string {
	msg := "illegal transition " + e.From.String() + " -> " + e.To.String()
	if e.Reason != "" {
		msg += ": " + e.Reason
	}
	return msg
}

// Code implements the coded-error convention used across mcpd.
func (e *TransitionError) Code() string { return CodeInvalidTransition }

// GuardError reports that a transition is structurally legal but its guard
// rejected it — an expired proposal, a payload hash mismatch, drifted
// preconditions. The distinction matters: a TransitionError is a programming
// error, a GuardError is the system working correctly.
type GuardError struct {
	ErrCode string
	Detail  string
}

func (e *GuardError) Error() string {
	if e.Detail == "" {
		return e.ErrCode
	}
	return e.ErrCode + ": " + e.Detail
}

// Code returns the stable error code.
func (e *GuardError) Code() string { return e.ErrCode }

// guard is a convenience constructor for GuardError.
func guard(code, detail string) error { return &GuardError{ErrCode: code, Detail: detail} }
