package operations

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// The repository contracts live here, beside the domain that consumes them,
// rather than in the storage package. Declaring an interface next to its
// caller is the Go convention, and here it also breaks what would otherwise be
// a cycle: the domain would import storage for these types while storage
// imported the domain for Operation.

// Persistence errors callers routinely branch on.
var (
	// ErrStateConflict reports that a guarded transition found the operation
	// in a different state than expected. Under concurrency this is an
	// ordinary outcome: another actor got there first.
	ErrStateConflict = errors.New("operations: operation is no longer in the expected state")

	// ErrIdempotencyConflict reports a key that cannot be used for a new
	// proposal: either it was reused with a different request body, or an
	// operation for the same intent is still live.
	//
	// Both readings are refusals to guess. Returning the first operation for a
	// changed body would execute something the caller did not ask for, and
	// proposing a second copy of a live intent would queue the same change
	// twice. A settled intent is not a conflict -- see 0005 for the index that
	// decides which is which.
	ErrIdempotencyConflict = errors.New("operations: idempotency key is in use by a live operation, or was reused with a different payload")
)

// OutboxEvent is an envelope queued in the outbox as part of a state change. It
// becomes a message on the bus once the publisher drains it.
type OutboxEvent struct {
	ID            string
	Subject       string
	OperationID   string
	CorrelationID string
	Payload       json.RawMessage
}

// AuditEntry is one append-only record. The storage layer assigns the sequence
// number and hash chain; callers supply the semantic fields.
type AuditEntry struct {
	EventID       string
	Kind          string
	OperationID   string
	Plugin        string
	Action        string
	Actor         string
	FromState     OperationState
	ToState       OperationState
	Risk          RiskLevel
	CorrelationID string
	// Detail must already be redacted. The storage layer will not strip
	// credentials it does not know how to recognise.
	Detail json.RawMessage
}

// RepoProposeRequest carries everything TX-1 writes.
type RepoProposeRequest struct {
	Operation *Operation
	Audit     AuditEntry
	Event     OutboxEvent
	// RequestHash lets a repeated idempotency key with a different body be
	// rejected rather than silently returning the first operation.
	RequestHash string
	// IdempotencyTTL bounds how long the record is honoured.
	IdempotencyTTL time.Duration
}

// TransitionRequest carries a guarded state change: TX-2 and TX-6.
type TransitionRequest struct {
	OperationID string
	From        OperationState
	To          OperationState
	Actor       string
	Reason      string
	Audit       AuditEntry
	Event       OutboxEvent

	// Approval is set when To is StateApproved.
	Approval *ApprovalFields
	// Terminal marks the target state as final, stamping terminal_at.
	Terminal bool
	// ErrorCode and ErrorDetail are recorded when the transition represents a
	// refusal or failure.
	ErrorCode   string
	ErrorDetail string
}

// ApprovalFields records who approved an operation and how long the approval
// remains executable.
type ApprovalFields struct {
	ApprovedBy        string
	ApprovedAt        time.Time
	ApprovalExpiresAt time.Time
	// AuthorizedByRule names the standing rule that authorised the change when
	// nobody was asked. Empty for a decision a person made, and written in the
	// same guarded statement as the approval itself so an operation cannot end
	// up approved with no account of what authorised it.
	AuthorizedByRule string
}

// ClaimRequest carries TX-3: the guarded transition into execution.
type ClaimRequest struct {
	OperationID string
	// ExpectedHash must equal the stored payload hash. Including it in the
	// UPDATE's WHERE clause means a tampered payload cannot be executed even
	// if it slipped past the domain-level guard.
	ExpectedHash   string
	InstanceID     string
	LeaseExpiresAt time.Time
	AttemptID      string
	Audit          AuditEntry
	Event          OutboxEvent
}

// SettleRequest carries TX-4: the outcome of an execution attempt.
type SettleRequest struct {
	OperationID string
	AttemptID   string
	To          OperationState
	Actor       string
	Reason      string
	Verified    *bool
	Observed    json.RawMessage
	UpstreamRef string
	ErrorCode   string
	ErrorDetail string
	Audit       AuditEntry
	Event       OutboxEvent
}

// ListFilter narrows an operation listing.
type ListFilter struct {
	Plugin string
	States []OperationState
	Limit  int
	Offset int
}

// Repository persists operations and their state changes. Every
// method that changes state does so atomically with the corresponding
// transition, audit entry, and outbox event.
type Repository interface {
	// Propose writes a new operation in pending_approval (TX-1). It returns
	// ErrIdempotencyConflict if the key was reused with a different payload,
	// and the existing operation if the key was reused with the same payload.
	Propose(ctx context.Context, req RepoProposeRequest) (*Operation, error)

	Get(ctx context.Context, id string) (*Operation, error)
	List(ctx context.Context, f ListFilter) ([]*Operation, error)

	// Transition applies a guarded state change (TX-2, TX-6). It returns
	// ErrStateConflict when the operation is no longer in the expected state.
	Transition(ctx context.Context, req TransitionRequest) (*Operation, error)

	// Claim moves an approved operation into executing (TX-3). It returns
	// ErrClaimLost when another worker won the race.
	Claim(ctx context.Context, req ClaimRequest) (*Operation, error)

	// Settle records the outcome of an execution attempt (TX-4).
	Settle(ctx context.Context, req SettleRequest) (*Operation, error)

	// DueForExpiry returns operations whose proposal or approval deadline has
	// passed, and executing operations whose lease has lapsed.
	DueForExpiry(ctx context.Context, now time.Time, limit int) ([]*Operation, error)

	// Claimable returns approved operations awaiting execution. This is both
	// the executor's startup scan and the polling fallback that keeps work
	// moving when an event is missed.
	Claimable(ctx context.Context, limit int) ([]*Operation, error)
}

// OutboxRepository drains queued events onto the bus.
type OutboxRepository interface {
	// Pending returns unpublished events whose retry time has arrived, in
	// sequence order so that per-subject ordering is preserved.
	Pending(ctx context.Context, now time.Time, limit int) ([]PendingEvent, error)
	// MarkPublished records a successful publish (TX-5).
	MarkPublished(ctx context.Context, eventID string, at time.Time) error
	// MarkFailed schedules a retry with backoff.
	MarkFailed(ctx context.Context, eventID string, nextAttempt time.Time, cause string) error
	// PendingCount reports backlog depth for the readiness endpoint.
	PendingCount(ctx context.Context) (int, error)
}

// PendingEvent is an outbox row awaiting publication.
type PendingEvent struct {
	Seq           int64
	EventID       string
	Subject       string
	OperationID   string
	CorrelationID string
	Payload       json.RawMessage
	Attempts      int
}

// AuditRepository reads the append-only trail. Writes happen only as part of
// an operation transaction, so there is no Append method here by design.
type AuditRepository interface {
	ByOperation(ctx context.Context, operationID string) ([]AuditRecord, error)
	Recent(ctx context.Context, limit int) ([]AuditRecord, error)
	// VerifyChain walks the hash chain and reports the first sequence number
	// where it breaks, or zero if intact.
	VerifyChain(ctx context.Context) (brokenAt int64, err error)
}

// AuditRecord is a stored audit entry with its chain metadata.
type AuditRecord struct {
	Seq       int64
	EventID   string
	At        time.Time
	Entry     AuditEntry
	PrevHash  string
	EntryHash string
}
