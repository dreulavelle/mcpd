// Package operations owns the mutation domain: operation state, the transition
// rules that govern it, risk classification, and the canonical hashing that
// makes an approved payload tamper-evident.
//
// This package performs no I/O. It depends on no HTTP client, no database
// driver, and no message bus. Persistence and transport are supplied through
// interfaces declared here and implemented elsewhere, which is what allows the
// entire approval model to be tested without a database.
package operations

import (
	"encoding/json"
	"time"
)

// OperationState enumerates every state a mutation may occupy. The zero value
// is intentionally invalid so an uninitialised Operation cannot masquerade as a
// draft.
type OperationState string

const (
	StateDraft           OperationState = "draft"
	StatePendingApproval OperationState = "pending_approval"
	StateApproved        OperationState = "approved"
	StateExecuting       OperationState = "executing"
	StateSucceeded       OperationState = "succeeded"
	StateFailed          OperationState = "failed"
	StateRejected        OperationState = "rejected"
	StateExpired         OperationState = "expired"
	StateCancelled       OperationState = "cancelled"

	// StateIndeterminate records that execution began and its outcome is
	// genuinely unknown: the process died mid-write, the upstream call timed
	// out, or a lease expired without settlement.
	//
	// It is deliberately distinct from StateFailed. Recording an ambiguous
	// outcome as a failure invites a retry, and the retry double-applies the
	// mutation. Nothing leaves this state automatically; a reconciler observes
	// upstream reality, or a human does.
	StateIndeterminate OperationState = "indeterminate"
)

// String implements fmt.Stringer.
func (s OperationState) String() string { return string(s) }

// IsTerminal reports whether the state admits no further transitions.
//
// StateIndeterminate is not terminal: it is resolvable by observation, and
// treating it as final would strand operations that did in fact succeed.
func (s OperationState) IsTerminal() bool {
	switch s {
	case StateSucceeded, StateFailed, StateRejected, StateExpired, StateCancelled:
		return true
	}
	return false
}

// Valid reports whether s is a state this package recognises.
func (s OperationState) Valid() bool {
	switch s {
	case StateDraft, StatePendingApproval, StateApproved, StateExecuting,
		StateSucceeded, StateFailed, StateRejected, StateExpired,
		StateCancelled, StateIndeterminate:
		return true
	}
	return false
}

// RiskLevel classifies the blast radius of a mutation. Policy may raise the
// risk of an operation; nothing may lower it.
type RiskLevel string

const (
	RiskLow      RiskLevel = "low"
	RiskMedium   RiskLevel = "medium"
	RiskHigh     RiskLevel = "high"
	RiskCritical RiskLevel = "critical"
)

// String implements fmt.Stringer.
func (r RiskLevel) String() string { return string(r) }

// rank orders risk levels for comparison.
//
// An empty level ranks lowest because it means "not specified" -- an absent
// policy override, a zero-valued field. A non-empty but unrecognised level
// ranks highest, because that is a genuinely unknown classification and
// treating it as harmless would be the wrong way to fail.
//
// The distinction matters: conflating the two makes every missing override
// outrank the risk a plugin actually declared.
func (r RiskLevel) rank() int {
	switch r {
	case "":
		return 0
	case RiskLow:
		return 1
	case RiskMedium:
		return 2
	case RiskHigh:
		return 3
	case RiskCritical:
		return 4
	default:
		return 5
	}
}

// Valid reports whether r is a recognised risk level. An unset level is not
// valid: callers must decide what to do about it rather than defaulting.
func (r RiskLevel) Valid() bool {
	rank := r.rank()
	return rank >= 1 && rank <= 4
}

// AtLeast reports whether r is at least as severe as other.
func (r RiskLevel) AtLeast(other RiskLevel) bool { return r.rank() >= other.rank() }

// MaxRisk returns the most severe of the supplied levels. It is the only
// sanctioned way to combine a plugin's declared risk with policy overrides,
// and it enforces invariant I8: risk may be raised, never lowered.
//
// Unset levels are ignored so that an absent override does not outrank a
// declared risk. If nothing at all is specified the result is RiskLow, which
// is the only sensible floor -- an operation with no risk classification still
// has to have one.
func MaxRisk(levels ...RiskLevel) RiskLevel {
	out := RiskLevel("")
	for _, l := range levels {
		if l == "" {
			continue
		}
		if l.rank() > out.rank() {
			out = l
		}
	}
	if out == "" {
		return RiskLow
	}
	return out
}

// Change describes a single field-level difference between observed and
// requested state. Changes are what an approver actually reads, so they must
// describe the mutation faithfully and completely.
type Change struct {
	Field string `json:"field"`
	From  any    `json:"from"`
	To    any    `json:"to"`
}

// Assurance names what an operation's record actually proves.
//
// Two different things get called an approval, and they are not the same
// evidence. A reviewed change is one mcpd planned itself: the approver saw the
// exact fields, drift between proposal and execution is detectable, and the
// outcome is confirmed by re-reading the target. A gated call is one a human
// authorised without mcpd being able to describe or confirm what it did -- all
// the record proves is that somebody said yes and that the call was made.
//
// Calling both "approved" lets the second borrow the first's credibility,
// which is the whole reason the word is worth splitting.
type Assurance string

const (
	// AssuranceReviewedChange carries every proof: exact fields, drift
	// detection, and a confirmed outcome.
	AssuranceReviewedChange Assurance = "reviewed_change"
	// AssuranceGatedCall carries only authorisation and the fact of the call.
	AssuranceGatedCall Assurance = "gated_call"
)

// String implements fmt.Stringer.
func (a Assurance) String() string { return string(a) }

// Operation is the authoritative record of a proposed infrastructure mutation.
//
// Every field between Target and Verifiable is frozen once the operation
// leaves StateDraft. The storage layer enforces this with a trigger as well as
// by convention, because a payload that can change after approval makes
// approval meaningless.
type Operation struct {
	ID     string         `json:"id"`
	Plugin string         `json:"plugin"`
	Action string         `json:"action"`
	State  OperationState `json:"state"`
	Risk   RiskLevel      `json:"risk"`

	// --- immutable after leaving draft ---------------------------------
	Target        json.RawMessage `json:"target"`
	Params        json.RawMessage `json:"params"`
	PayloadHash   string          `json:"payload_hash"`
	Before        json.RawMessage `json:"before,omitempty"`
	Desired       json.RawMessage `json:"desired,omitempty"`
	Preconditions json.RawMessage `json:"preconditions,omitempty"`
	Rollback      json.RawMessage `json:"rollback,omitempty"`
	Changes       []Change        `json:"changes,omitempty"`
	Impact        string          `json:"impact"`
	// Verifiable is the mutation's own declaration that re-reading the target
	// after the write confirms the result. It is declared by the plugin rather
	// than inferred from an empty field, because an absent desired state is
	// ambiguous: for a delete it means "the target should be gone", which is
	// a meaningful thing to observe, and for a mutation that simply cannot
	// verify it means nothing at all.
	//
	// The executor reads this from the stored row rather than from the plugin
	// that is about to run, for the same reason it reads the payload and the
	// approval from there: what the operation claims about itself must not be
	// changeable by the code being checked.
	Verifiable bool `json:"verifiable"`
	// -------------------------------------------------------------------

	RequestedBy string    `json:"requested_by"`
	RequestedAt time.Time `json:"requested_at"`
	ExpiresAt   time.Time `json:"expires_at"`

	ApprovedBy        string     `json:"approved_by,omitempty"`
	ApprovedAt        *time.Time `json:"approved_at,omitempty"`
	ApprovalExpiresAt *time.Time `json:"approval_expires_at,omitempty"`
	// AuthorizedByRule names the standing rule that approved this operation
	// without anyone being asked. Empty means a person decided.
	//
	// It is on the row rather than only in the audit detail because it is a
	// fact about the operation that every reader needs: the executor's refusal
	// to run a change whose risk was revised upward after nobody saw it turns
	// on this, and so does the dashboard's obligation not to render "approved
	// by system:policy" as though somebody clicked.
	AuthorizedByRule string `json:"authorized_by_rule,omitempty"`

	AttemptCount   int        `json:"attempt_count"`
	LeaseOwner     string     `json:"lease_owner,omitempty"`
	LeaseExpiresAt *time.Time `json:"lease_expires_at,omitempty"`

	TerminalAt      *time.Time      `json:"terminal_at,omitempty"`
	OutcomeVerified *bool           `json:"outcome_verified,omitempty"`
	Observed        json.RawMessage `json:"observed,omitempty"`
	ErrorCode       string          `json:"error_code,omitempty"`
	ErrorDetail     string          `json:"error_detail,omitempty"`

	CorrelationID  string `json:"correlation_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

// Transition is an audit-grade record of one state change.
type Transition struct {
	Seq           int64          `json:"seq"`
	OperationID   string         `json:"operation_id"`
	From          OperationState `json:"from,omitempty"`
	To            OperationState `json:"to"`
	Actor         string         `json:"actor"`
	Reason        string         `json:"reason,omitempty"`
	At            time.Time      `json:"at"`
	CorrelationID string         `json:"correlation_id"`
}

// Attempt records one execution attempt against an operation. Attempts are
// append-only and exist so that an ambiguous outcome can be reconciled against
// what was actually tried.
type Attempt struct {
	ID             string          `json:"id"`
	OperationID    string          `json:"operation_id"`
	AttemptNo      int             `json:"attempt_no"`
	InstanceID     string          `json:"instance_id"`
	StartedAt      time.Time       `json:"started_at"`
	FinishedAt     *time.Time      `json:"finished_at,omitempty"`
	Outcome        OperationState  `json:"outcome,omitempty"`
	UpstreamRef    string          `json:"upstream_ref,omitempty"`
	UpstreamStatus int             `json:"upstream_status,omitempty"`
	Verified       *bool           `json:"verified,omitempty"`
	Observed       json.RawMessage `json:"observed,omitempty"`
	ErrorCode      string          `json:"error_code,omitempty"`
	ErrorDetail    string          `json:"error_detail,omitempty"`
}

// DriftChecked reports whether this operation carries a precondition snapshot
// that a re-plan can be compared against. A mutation declaring none is not
// drift-checked, however cleanly its execution reports.
func (o *Operation) DriftChecked() bool { return Declared(o.Preconditions) }

// Assurance reports which of the two things called an approval this is.
//
// A mutation is a reviewed change only while it holds all three proofs. Drop
// one -- no declared preconditions, no way to confirm the outcome -- and what
// the record proves is what a gated call proves: it was authorised, and it
// happened. That deserves the smaller word.
func (o *Operation) Assurance() Assurance {
	if o.Verifiable && o.DriftChecked() {
		return AssuranceReviewedChange
	}
	return AssuranceGatedCall
}

// AutoApproved reports whether this operation was authorised by a standing
// rule rather than by a person.
func (o *Operation) AutoApproved() bool { return o.AuthorizedByRule != "" }

// SystemActor identifies transitions performed by mcpd itself rather than by a
// principal. Background components use a suffixed form, e.g. "system:reaper".
const SystemActor = "system"
