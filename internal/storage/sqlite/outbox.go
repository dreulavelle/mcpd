package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/spoked/mcpd/internal/messaging"
	"github.com/spoked/mcpd/internal/operations"
)

// OutboxStore implements storage.OutboxRepository.
type OutboxStore struct {
	db  *DB
	now func() time.Time
}

// NewOutboxStore returns an outbox repository backed by db.
func NewOutboxStore(db *DB, now func() time.Time) *OutboxStore {
	if now == nil {
		now = time.Now
	}
	return &OutboxStore{db: db, now: now}
}

// Pending returns unpublished events whose retry time has arrived.
//
// Ordering by seq preserves the order in which state changes were committed,
// so a consumer never sees an operation succeed before it sees it start.
func (s *OutboxStore) Pending(ctx context.Context, now time.Time, limit int) ([]operations.PendingEvent, error) {
	if limit <= 0 {
		limit = 128
	}
	rows, err := s.db.Reader().QueryContext(ctx, `
		SELECT seq, event_id, subject, COALESCE(operation_id,''), correlation_id, payload_json, attempts
		FROM outbox_events
		WHERE published_at IS NULL AND next_attempt_at <= ?
		ORDER BY seq ASC
		LIMIT ?`, now.UnixMilli(), limit)
	if err != nil {
		return nil, fmt.Errorf("sqlite: read outbox: %w", err)
	}
	defer rows.Close()

	var out []operations.PendingEvent
	for rows.Next() {
		var e operations.PendingEvent
		var payload string
		if err := rows.Scan(&e.Seq, &e.EventID, &e.Subject, &e.OperationID,
			&e.CorrelationID, &payload, &e.Attempts); err != nil {
			return nil, err
		}
		e.Payload = json.RawMessage(payload)
		out = append(out, e)
	}
	return out, rows.Err()
}

// MarkPublished implements TX-5.
//
// This is deliberately its own transaction rather than part of the business
// transaction that queued the event. Folding it in would mean holding the
// single writer connection open across a network call, turning bus latency
// into database contention and a bus stall into a total write outage.
//
// The cost is at-least-once rather than exactly-once delivery, which is
// acceptable precisely because no consumer carries authority: every one of
// them reloads and revalidates from the database on receipt.
func (s *OutboxStore) MarkPublished(ctx context.Context, eventID string, at time.Time) error {
	return s.db.WriteTx(ctx, at.UnixMilli(), func(u *UnitOfWork) error {
		_, err := u.exec(
			`UPDATE outbox_events SET published_at = ?, last_error = NULL
			 WHERE event_id = ? AND published_at IS NULL`,
			at.UnixMilli(), eventID)
		return err
	})
}

// MarkFailed schedules a retry.
func (s *OutboxStore) MarkFailed(ctx context.Context, eventID string, nextAttempt time.Time, cause string) error {
	now := s.now()
	return s.db.WriteTx(ctx, now.UnixMilli(), func(u *UnitOfWork) error {
		_, err := u.exec(
			`UPDATE outbox_events
			 SET attempts = attempts + 1, next_attempt_at = ?, last_error = ?
			 WHERE event_id = ? AND published_at IS NULL`,
			nextAttempt.UnixMilli(), truncate(cause, 1024), eventID)
		return err
	})
}

// PendingCount reports backlog depth. A growing backlog is a readiness signal,
// not a liveness one: the system remains correct while the bus is unavailable.
func (s *OutboxStore) PendingCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.Reader().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM outbox_events WHERE published_at IS NULL`).Scan(&n)
	return n, err
}

// PurgePublished removes published events older than the cutoff, keeping the
// table bounded on a long-running process. The audit trail is the durable
// record; the outbox is a queue.
func (s *OutboxStore) PurgePublished(ctx context.Context, olderThan time.Time) (int64, error) {
	var affected int64
	err := s.db.WriteTx(ctx, s.now().UnixMilli(), func(u *UnitOfWork) error {
		res, err := u.exec(
			`DELETE FROM outbox_events WHERE published_at IS NOT NULL AND published_at < ?`,
			olderThan.UnixMilli())
		if err != nil {
			return err
		}
		affected, err = res.RowsAffected()
		return err
	})
	return affected, err
}

func truncate(s string, n int) any {
	if s == "" {
		return nil
	}
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Pending implements messaging.OutboxReader.
//
// The messaging package declares its own view of an outbox row so that it does
// not depend on the domain packages that queue events into it. This adapter is
// the seam between the two, and it is deliberately the only place the two
// shapes are mapped.
type MessagingAdapter struct{ *OutboxStore }

// Pending returns unpublished events in the messaging package's shape.
func (a MessagingAdapter) Pending(ctx context.Context, now time.Time, limit int) ([]messaging.PendingEvent, error) {
	rows, err := a.OutboxStore.Pending(ctx, now, limit)
	if err != nil {
		return nil, err
	}
	out := make([]messaging.PendingEvent, len(rows))
	for i, r := range rows {
		out[i] = messaging.PendingEvent{
			Seq:           r.Seq,
			EventID:       r.EventID,
			Subject:       r.Subject,
			OperationID:   r.OperationID,
			CorrelationID: r.CorrelationID,
			Payload:       r.Payload,
			Attempts:      r.Attempts,
		}
	}
	return out, nil
}
