package sqlite

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/spoked/mcpd/internal/storage"
)

// genesisHash seeds the audit chain. Its only requirement is that it is a
// constant no real entry hash can collide with.
const genesisHash = "0000000000000000000000000000000000000000000000000000000000000000"

// appendAudit writes one audit entry, linking it to its predecessor.
//
// The chain means tampering is detectable rather than merely discouraged:
// altering any historical row invalidates every entry_hash after it, and the
// triggers on the table already refuse UPDATE and DELETE outright. Together
// they make the trail evidence rather than a log.
//
// This is unexported and takes a UnitOfWork because an audit entry must never
// be written outside the transaction that caused it.
func (u *UnitOfWork) appendAudit(e storage.AuditEntry) error {
	var prev string
	err := u.queryRow(`SELECT entry_hash FROM audit_events ORDER BY seq DESC LIMIT 1`).Scan(&prev)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		prev = genesisHash
	case err != nil:
		return fmt.Errorf("sqlite: read audit head: %w", err)
	}

	detail := e.Detail
	if len(detail) == 0 {
		detail = []byte("{}")
	}

	entryHash := chainHash(prev, u.now, e, detail)

	_, err = u.exec(`
		INSERT INTO audit_events (
			event_id, at, kind, operation_id, plugin, action, actor,
			from_state, to_state, risk, correlation_id, detail_json,
			prev_hash, entry_hash
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		e.EventID, u.now, e.Kind, nullStr(e.OperationID), nullStr(e.Plugin),
		nullStr(e.Action), e.Actor, nullStr(e.FromState.String()),
		nullStr(e.ToState.String()), nullStr(e.Risk.String()),
		e.CorrelationID, string(detail), prev, entryHash)
	if err != nil {
		return fmt.Errorf("sqlite: append audit: %w", err)
	}
	return nil
}

// chainHash derives an entry hash from its predecessor and its own content.
// Fields are length-prefixed so that boundaries cannot be shifted: without
// them, actor "ab" with kind "c" would hash identically to actor "a" with
// kind "bc".
func chainHash(prev string, at int64, e storage.AuditEntry, detail []byte) string {
	h := sha256.New()
	parts := [][]byte{
		[]byte(prev),
		[]byte(fmt.Sprint(at)),
		[]byte(e.EventID),
		[]byte(e.Kind),
		[]byte(e.OperationID),
		[]byte(e.Plugin),
		[]byte(e.Action),
		[]byte(e.Actor),
		[]byte(e.FromState.String()),
		[]byte(e.ToState.String()),
		[]byte(e.Risk.String()),
		[]byte(e.CorrelationID),
		detail,
	}
	for _, p := range parts {
		fmt.Fprintf(h, "%d:", len(p))
		h.Write(p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// enqueue writes an outbox row inside the same transaction as the state change
// that produced it. This is the entire defence against a dual-write
// inconsistency between the database and the event bus: there is no code path
// that changes state without queueing an event, because both happen here.
func (u *UnitOfWork) enqueue(e storage.Event) error {
	if e.ID == "" || e.Subject == "" {
		return fmt.Errorf("sqlite: outbox event requires an id and subject")
	}
	payload := e.Payload
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	_, err := u.exec(`
		INSERT INTO outbox_events (
			event_id, subject, operation_id, correlation_id,
			payload_json, created_at, next_attempt_at
		) VALUES (?,?,?,?,?,?,0)`,
		e.ID, e.Subject, nullStr(e.OperationID), e.CorrelationID,
		string(payload), u.now)
	if err != nil {
		return fmt.Errorf("sqlite: enqueue outbox event: %w", err)
	}
	return nil
}

// recordTransition appends the state-change history row.
func (u *UnitOfWork) recordTransition(opID, from, to, actor, reason, correlationID string) error {
	_, err := u.exec(`
		INSERT INTO operation_transitions (
			operation_id, from_state, to_state, actor, reason, at, correlation_id
		) VALUES (?,?,?,?,?,?,?)`,
		opID, nullStr(from), to, actor, nullStr(reason), u.now, correlationID)
	if err != nil {
		return fmt.Errorf("sqlite: record transition: %w", err)
	}
	return nil
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
