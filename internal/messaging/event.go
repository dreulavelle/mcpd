// Package messaging carries events from the outbox to whatever consumes them.
//
// Nothing here is authoritative. An event means "operation X may need
// attention", never "operation X is approved" -- every consumer reloads and
// revalidates from the database on receipt. That is what makes a lost,
// duplicated, or delayed event cost latency rather than correctness, and it is
// why a single node needs no broker at all.
package messaging

import (
	"context"
	"encoding/json"
	"time"
)

// Event is one thing that happened, as recorded in the outbox.
type Event struct {
	// ID is the outbox event id. It is the deduplication key: a consumer that
	// sees the same ID twice is seeing a redelivery, not a second event.
	ID string
	// Subject names what happened, e.g. "mcp.operation.approved".
	Subject string
	// OperationID is empty for plugin-domain events.
	OperationID string
	// CorrelationID ties the event back to the request that caused it.
	CorrelationID string
	// OccurredAt is when the originating transaction committed.
	OccurredAt time.Time
	// Payload carries event-specific detail. It is deliberately thin: a
	// consumer needing more must read the database, which is the only place
	// the truth lives.
	Payload json.RawMessage
}

// Handler processes one event.
//
// Returning an error causes redelivery, so a handler must be safe to run more
// than once on the same event. In practice that is automatic: handlers reload
// state and act on guarded transitions, which no-op when the work is already
// done.
type Handler func(ctx context.Context, e Event) error
