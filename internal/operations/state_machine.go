package operations

import "time"

// Trigger names the cause of a transition. Triggers exist so the transition
// table can distinguish, for example, an operator cancelling a proposal from
// the reaper expiring it, even though both leave the same state.
type Trigger string

const (
	TriggerSubmit    Trigger = "submit"
	TriggerApprove   Trigger = "approve"
	TriggerReject    Trigger = "reject"
	TriggerCancel    Trigger = "cancel"
	TriggerExpire    Trigger = "expire"
	TriggerClaim     Trigger = "claim"
	TriggerSettle    Trigger = "settle"
	TriggerReconcile Trigger = "reconcile"
)

// GuardContext carries everything a transition guard may inspect. It is passed
// by value; guards must not mutate it.
type GuardContext struct {
	Now time.Time

	// Actor is the principal (or "system:<component>") requesting the change.
	Actor string

	// RecomputedHash is the payload hash derived from the operation as loaded
	// from storage. The claim guard compares it against the stored hash.
	RecomputedHash string

	// PreconditionsMatch reports whether the plugin's freshly observed
	// preconditions equal those captured at proposal time. Only consulted for
	// TriggerClaim.
	PreconditionsMatch bool

	// RequireDistinctApprover activates separation of duties for this
	// operation's risk level.
	RequireDistinctApprover bool

	// IdentityDistinguishable reports whether the active authentication mode
	// can tell two principals apart. When separation of duties is required and
	// this is false, approval is refused rather than silently degraded.
	IdentityDistinguishable bool
}

// transition is one row of the transition table.
type transition struct {
	from    OperationState
	to      OperationState
	trigger Trigger
	guard   func(op *Operation, gc GuardContext) error
}

// table is the single authoritative definition of legal state changes.
// Nothing outside this slice may move an operation between states.
var table = []transition{
	{StateDraft, StatePendingApproval, TriggerSubmit, guardSubmit},
	{StateDraft, StateCancelled, TriggerCancel, guardActorIsRequester},

	{StatePendingApproval, StateApproved, TriggerApprove, guardApprove},
	{StatePendingApproval, StateRejected, TriggerReject, nil},
	{StatePendingApproval, StateCancelled, TriggerCancel, guardActorIsRequester},
	{StatePendingApproval, StateExpired, TriggerExpire, guardProposalExpired},

	{StateApproved, StateExecuting, TriggerClaim, guardClaim},
	{StateApproved, StateCancelled, TriggerCancel, guardNoLeaseHeld},
	// Added beyond the original design: an approval that is never executed
	// must expire on its own deadline. Without this, an approval granted weeks
	// ago remains executable against a network that has since changed.
	{StateApproved, StateExpired, TriggerExpire, guardApprovalExpired},

	{StateExecuting, StateSucceeded, TriggerSettle, nil},
	{StateExecuting, StateFailed, TriggerSettle, nil},
	{StateExecuting, StateIndeterminate, TriggerSettle, nil},

	{StateIndeterminate, StateSucceeded, TriggerReconcile, nil},
	{StateIndeterminate, StateFailed, TriggerReconcile, nil},
}

// CanTransition reports whether a transition is structurally legal, ignoring
// guards. It exists for exhaustive testing and for rendering the state graph.
func CanTransition(from, to OperationState, trigger Trigger) bool {
	for _, t := range table {
		if t.from == from && t.to == to && t.trigger == trigger {
			return true
		}
	}
	return false
}

// Transitions returns every legal target state from the given state. The result
// is freshly allocated and safe for the caller to retain.
func Transitions(from OperationState) []OperationState {
	var out []OperationState
	seen := make(map[OperationState]bool)
	for _, t := range table {
		if t.from == from && !seen[t.to] {
			seen[t.to] = true
			out = append(out, t.to)
		}
	}
	return out
}

// Validate checks a proposed transition against both the transition table and
// the relevant guard.
//
// A *TransitionError means the transition is not in the table at all, which is
// a programming error. A *GuardError means the transition is legal but its
// preconditions were not met, which is the system correctly refusing.
//
// Validate is advisory: it is the domain-level check. Storage still applies the
// same condition inside the UPDATE's WHERE clause, so a concurrent writer
// cannot slip between validation and commit.
func Validate(op *Operation, to OperationState, trigger Trigger, gc GuardContext) error {
	if op == nil {
		return &TransitionError{To: to, Reason: "nil operation"}
	}
	if !to.Valid() {
		return &TransitionError{From: op.State, To: to, Reason: "unknown target state"}
	}
	if op.State.IsTerminal() {
		return &TransitionError{From: op.State, To: to, Reason: "source state is terminal"}
	}
	for _, t := range table {
		if t.from != op.State || t.to != to || t.trigger != trigger {
			continue
		}
		if t.guard == nil {
			return nil
		}
		return t.guard(op, gc)
	}
	return &TransitionError{From: op.State, To: to, Reason: "no such trigger"}
}

// --- guards ---------------------------------------------------------------

func guardSubmit(op *Operation, gc GuardContext) error {
	if op.PayloadHash == "" {
		return guard(CodePayloadMismatch, "payload hash not computed")
	}
	if op.ExpiresAt.IsZero() || !op.ExpiresAt.After(gc.Now) {
		return guard(CodeProposalExpired, "expiry must be in the future")
	}
	return nil
}

func guardActorIsRequester(op *Operation, gc GuardContext) error {
	if gc.Actor != op.RequestedBy && !isSystem(gc.Actor) {
		return guard(CodeNotAuthorized, "only the requester may cancel")
	}
	return nil
}

func guardNoLeaseHeld(op *Operation, gc GuardContext) error {
	if op.LeaseExpiresAt != nil && op.LeaseExpiresAt.After(gc.Now) {
		return guard(CodeNotAuthorized, "operation is claimed for execution")
	}
	return nil
}

func guardProposalExpired(op *Operation, gc GuardContext) error {
	if gc.Now.Before(op.ExpiresAt) {
		return guard(CodeProposalExpired, "proposal has not expired")
	}
	return nil
}

func guardApprovalExpired(op *Operation, gc GuardContext) error {
	if op.ApprovalExpiresAt == nil {
		return guard(CodeApprovalExpired, "approved operation has no execute-by deadline")
	}
	if gc.Now.Before(*op.ApprovalExpiresAt) {
		return guard(CodeApprovalExpired, "approval has not expired")
	}
	return nil
}

func guardApprove(op *Operation, gc GuardContext) error {
	if !gc.Now.Before(op.ExpiresAt) {
		return guard(CodeProposalExpired, "proposal expired before approval")
	}
	if !gc.RequireDistinctApprover {
		return nil
	}
	// Separation of duties fails closed. If the active authentication mode
	// cannot distinguish principals, the rule cannot be enforced, and silently
	// disabling it would leave the policy claiming a guarantee it is not
	// providing.
	if !gc.IdentityDistinguishable {
		return guard(CodeIdentityIndistinct,
			"risk level requires a distinct approver, but the active auth mode cannot distinguish principals")
	}
	if gc.Actor == op.RequestedBy {
		return guard(CodeSelfApproval, "requester may not approve their own operation at this risk level")
	}
	return nil
}

func guardClaim(op *Operation, gc GuardContext) error {
	if op.PayloadHash == "" || gc.RecomputedHash == "" {
		return guard(CodePayloadMismatch, "missing payload hash")
	}
	if op.PayloadHash != gc.RecomputedHash {
		return guard(CodePayloadMismatch, "stored payload hash does not match recomputed hash")
	}
	if op.ApprovalExpiresAt == nil || !gc.Now.Before(*op.ApprovalExpiresAt) {
		return guard(CodeApprovalExpired, "approval is no longer valid for execution")
	}
	if !gc.PreconditionsMatch {
		return guard(CodePreconditionChanged, "target state changed since the operation was proposed")
	}
	return nil
}

func isSystem(actor string) bool {
	return actor == SystemActor || (len(actor) > len(SystemActor) &&
		actor[:len(SystemActor)+1] == SystemActor+":")
}

// subjectFor maps a state to the event subject announcing it.
//
// The mapping lives here, next to the states themselves, so that adding a
// state without deciding what it publishes is a compile-time omission rather
// than a silent one. The messaging package owns the subject strings; this
// duplicates the small set the domain needs so that operations does not depend
// on the transport layer.
func subjectFor(state OperationState) string {
	switch state {
	case StatePendingApproval:
		return "mcp.operation.proposed"
	case StateApproved:
		return "mcp.operation.approved"
	case StateRejected:
		return "mcp.operation.rejected"
	case StateCancelled:
		return "mcp.operation.cancelled"
	case StateExpired:
		return "mcp.operation.expired"
	case StateExecuting:
		return "mcp.operation.executing"
	case StateSucceeded:
		return "mcp.operation.succeeded"
	case StateFailed:
		return "mcp.operation.failed"
	case StateIndeterminate:
		return "mcp.operation.indeterminate"
	default:
		return ""
	}
}
