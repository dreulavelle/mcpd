// Package storage declares the persistence contracts the domain depends on.
// Implementations live in subpackages; nothing here imports a database driver.
package storage

import (
	"context"
	"encoding/json"
	"time"

	"github.com/spoked/mcpd/internal/operations"
)

// Event is an envelope queued in the outbox as part of a state change. It
// becomes a message on the bus once the publisher drains it.
type Event struct {
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
	FromState     operations.OperationState
	ToState       operations.OperationState
	Risk          operations.RiskLevel
	CorrelationID string
	// Detail must already be redacted. The storage layer will not strip
	// credentials it does not know how to recognise.
	Detail json.RawMessage
}

// ProposeRequest carries everything TX-1 writes.
type ProposeRequest struct {
	Operation *operations.Operation
	Audit     AuditEntry
	Event     Event
	// RequestHash lets a repeated idempotency key with a different body be
	// rejected rather than silently returning the first operation.
	RequestHash string
	// IdempotencyTTL bounds how long the record is honoured.
	IdempotencyTTL time.Duration
}

// TransitionRequest carries a guarded state change: TX-2 and TX-6.
type TransitionRequest struct {
	OperationID string
	From        operations.OperationState
	To          operations.OperationState
	Actor       string
	Reason      string
	Audit       AuditEntry
	Event       Event

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
	Event          Event
}

// SettleRequest carries TX-4: the outcome of an execution attempt.
type SettleRequest struct {
	OperationID string
	AttemptID   string
	To          operations.OperationState
	Actor       string
	Reason      string
	Verified    *bool
	Observed    json.RawMessage
	UpstreamRef string
	ErrorCode   string
	ErrorDetail string
	Audit       AuditEntry
	Event       Event
}

// ListFilter narrows an operation listing.
type ListFilter struct {
	Plugin string
	States []operations.OperationState
	Limit  int
	Offset int
}

// OperationRepository persists operations and their state changes. Every
// method that changes state does so atomically with the corresponding
// transition, audit entry, and outbox event.
type OperationRepository interface {
	// Propose writes a new operation in pending_approval (TX-1). It returns
	// ErrIdempotencyConflict if the key was reused with a different payload,
	// and the existing operation if the key was reused with the same payload.
	Propose(ctx context.Context, req ProposeRequest) (*operations.Operation, error)

	Get(ctx context.Context, id string) (*operations.Operation, error)
	List(ctx context.Context, f ListFilter) ([]*operations.Operation, error)

	// Transition applies a guarded state change (TX-2, TX-6). It returns
	// ErrStateConflict when the operation is no longer in the expected state.
	Transition(ctx context.Context, req TransitionRequest) (*operations.Operation, error)

	// Claim moves an approved operation into executing (TX-3). It returns
	// operations.ErrClaimLost when another worker won the race.
	Claim(ctx context.Context, req ClaimRequest) (*operations.Operation, error)

	// Settle records the outcome of an execution attempt (TX-4).
	Settle(ctx context.Context, req SettleRequest) (*operations.Operation, error)

	// DueForExpiry returns operations whose proposal or approval deadline has
	// passed, and executing operations whose lease has lapsed.
	DueForExpiry(ctx context.Context, now time.Time, limit int) ([]*operations.Operation, error)

	// Claimable returns approved operations awaiting execution. This is both
	// the executor's startup scan and the polling fallback that keeps work
	// moving when an event is missed.
	Claimable(ctx context.Context, limit int) ([]*operations.Operation, error)
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
