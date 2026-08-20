package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/spoked/mcpd/internal/operations"
	"time"
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
func (u *UnitOfWork) appendAudit(e operations.AuditEntry) error {
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
func chainHash(prev string, at int64, e operations.AuditEntry, detail []byte) string {
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
func (u *UnitOfWork) enqueue(e operations.OutboxEvent) error {
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

// AuditStore reads the append-only trail. There is no Append method by design:
// audit entries are written only inside the transaction that caused them, via
// UnitOfWork, so there is no path that records an event without the state
// change it describes.
type AuditStore struct {
	db *DB
}

// NewAuditStore returns a reader over the audit trail.
func NewAuditStore(db *DB) *AuditStore { return &AuditStore{db: db} }

const auditColumns = `seq, event_id, at, kind, COALESCE(operation_id,''),
	COALESCE(plugin,''), COALESCE(action,''), actor,
	COALESCE(from_state,''), COALESCE(to_state,''), COALESCE(risk,''),
	correlation_id, detail_json, prev_hash, entry_hash`

// ByOperation returns every audit entry for one operation, oldest first.
func (s *AuditStore) ByOperation(ctx context.Context, operationID string) ([]operations.AuditRecord, error) {
	rows, err := s.db.Reader().QueryContext(ctx,
		`SELECT `+auditColumns+` FROM audit_events WHERE operation_id = ? ORDER BY seq`,
		operationID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: read audit for %s: %w", operationID, err)
	}
	defer rows.Close()
	return scanAudit(rows)
}

// Recent returns the newest audit entries.
func (s *AuditStore) Recent(ctx context.Context, limit int) ([]operations.AuditRecord, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.Reader().QueryContext(ctx,
		`SELECT `+auditColumns+` FROM audit_events ORDER BY seq DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("sqlite: read recent audit: %w", err)
	}
	defer rows.Close()
	return scanAudit(rows)
}

// VerifyChain walks the hash chain and reports the first sequence number where
// it breaks, or zero if intact.
//
// This is what makes the trail evidence rather than a log. The triggers refuse
// UPDATE and DELETE, but a database file can be edited outside the process;
// recomputing each link detects that, because altering any row invalidates
// every entry_hash after it.
func (s *AuditStore) VerifyChain(ctx context.Context) (int64, error) {
	rows, err := s.db.Reader().QueryContext(ctx,
		`SELECT `+auditColumns+` FROM audit_events ORDER BY seq`)
	if err != nil {
		return 0, fmt.Errorf("sqlite: read audit chain: %w", err)
	}
	defer rows.Close()

	prev := genesisHash
	for rows.Next() {
		rec, err := scanAuditRow(rows)
		if err != nil {
			return 0, err
		}
		if rec.PrevHash != prev {
			return rec.Seq, nil
		}
		expected := chainHash(prev, rec.At.UnixMilli(), rec.Entry, rec.Entry.Detail)
		if expected != rec.EntryHash {
			return rec.Seq, nil
		}
		prev = rec.EntryHash
	}
	return 0, rows.Err()
}

func scanAudit(rows *sql.Rows) ([]operations.AuditRecord, error) {
	var out []operations.AuditRecord
	for rows.Next() {
		rec, err := scanAuditRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func scanAuditRow(row scannable) (operations.AuditRecord, error) {
	var (
		rec      operations.AuditRecord
		at       int64
		from, to string
		risk     string
		detail   string
	)
	err := row.Scan(&rec.Seq, &rec.EventID, &at, &rec.Entry.Kind,
		&rec.Entry.OperationID, &rec.Entry.Plugin, &rec.Entry.Action,
		&rec.Entry.Actor, &from, &to, &risk,
		&rec.Entry.CorrelationID, &detail, &rec.PrevHash, &rec.EntryHash)
	if err != nil {
		return rec, err
	}
	rec.At = time.UnixMilli(at).UTC()
	rec.Entry.EventID = rec.EventID
	rec.Entry.FromState = operations.OperationState(from)
	rec.Entry.ToState = operations.OperationState(to)
	rec.Entry.Risk = operations.RiskLevel(risk)
	rec.Entry.Detail = json.RawMessage(detail)
	return rec, nil
}
