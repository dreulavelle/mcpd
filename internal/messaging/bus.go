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

// Bus delivers events to subscribers.
//
// The interface exists so that a JetStream implementation can be dropped in
// when a second mcpd instance exists, without any caller changing. On one node
// the in-process implementation is strictly better: a channel send instead of
// a broker round trip, with the same delivery guarantee because the outbox --
// not the bus -- is what makes delivery durable.
type Bus interface {
	// Publish delivers an event. It must be idempotent on e.ID.
	Publish(ctx context.Context, e Event) error

	// Subscribe registers a handler for subjects matching a pattern. The
	// durable name identifies the subscription across restarts; it is unused
	// in-process and becomes the JetStream consumer name later.
	Subscribe(durable, pattern string, h Handler) error

	// Close releases resources and stops delivery.
	Close() error
}

// MatchSubject reports whether a subject matches a NATS-style pattern.
//
// The pattern grammar is fixed now, even though nothing external consumes it
// yet, so that subjects written today keep meaning the same thing if a broker
// is introduced later. "*" matches one token; ">" matches one or more trailing
// tokens and is only valid last.
func MatchSubject(pattern, subject string) bool {
	if pattern == subject {
		return true
	}
	p := splitTokens(pattern)
	s := splitTokens(subject)

	for i, tok := range p {
		if tok == ">" {
			// ">" must consume at least one token, and must be final.
			return i == len(p)-1 && len(s) > i
		}
		if i >= len(s) {
			return false
		}
		if tok != "*" && tok != s[i] {
			return false
		}
	}
	return len(p) == len(s)
}

func splitTokens(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := range len(s) {
		if s[i] == '.' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}
