package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// UnitOfWork is the only way anything in mcpd writes to the database.
//
// The single most important invariant in the system is that a state change,
// its transition record, its audit entry, and its outbox event become durable
// together or not at all. Funnelling every write through one type makes that
// invariant structural rather than a rule reviewers have to remember.
//
// A UnitOfWork is valid only for the duration of the WriteTx callback that
// created it. Retaining one past that point will fail against a closed
// transaction.
type UnitOfWork struct {
	tx  *sql.Tx
	ctx context.Context
	// now is the single timestamp used for every row written in this
	// transaction, so that an operation, its transition, its audit entry and
	// its outbox event all agree on when the change happened.
	now int64
}

// Now returns the transaction's timestamp in Unix milliseconds.
func (u *UnitOfWork) Now() int64 { return u.now }

// exec runs a statement inside the transaction.
func (u *UnitOfWork) exec(query string, args ...any) (sql.Result, error) {
	return u.tx.ExecContext(u.ctx, query, args...)
}

// queryRow runs a single-row query inside the transaction. Reading through the
// transaction rather than the reader pool matters: a guarded update needs to
// see its own uncommitted writes.
func (u *UnitOfWork) queryRow(query string, args ...any) *sql.Row {
	return u.tx.QueryRowContext(u.ctx, query, args...)
}

// execGuarded runs an UPDATE whose WHERE clause encodes a precondition and
// requires that exactly one row matched.
//
// Zero rows means the precondition did not hold — another worker won the race,
// or the operation left the state the guard required. That is reported as
// ErrNoRowsAffected, which callers are expected to handle rather than treat as
// a failure.
func (u *UnitOfWork) execGuarded(query string, args ...any) error {
	res, err := u.exec(query, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	switch {
	case n == 1:
		return nil
	case n == 0:
		return ErrNoRowsAffected
	default:
		// A guarded update is always keyed by primary key. More than one row
		// means the WHERE clause is wrong, which is a bug worth failing on.
		return fmt.Errorf("sqlite: guarded update matched %d rows, expected 1", n)
	}
}

// WriteTx runs fn inside a single transaction on the writer pool.
//
// The writer pool holds exactly one connection and the DSN sets
// _txlock=immediate, so transactions serialise in Go rather than contending in
// SQLite. Callers never see SQLITE_BUSY on this path.
//
// If fn returns an error the transaction rolls back and the error is returned
// unwrapped, so callers can match their own sentinels with errors.Is.
func (d *DB) WriteTx(ctx context.Context, nowMillis int64, fn func(*UnitOfWork) error) error {
	tx, err := d.write.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			// Use a context detached from cancellation: if ctx is already
			// cancelled, the rollback still needs to run or the single writer
			// connection stays wedged.
			_ = tx.Rollback()
		}
	}()

	uow := &UnitOfWork{tx: tx, ctx: ctx, now: nowMillis}
	if err := fn(uow); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: commit: %w", err)
	}
	committed = true
	return nil
}

// ReadTx runs fn against a read-only snapshot. Under WAL this never blocks
// behind the writer, which is what keeps agent tool calls fast while an
// approval is being committed.
func (d *DB) ReadTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := d.read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("sqlite: begin read: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // read-only; nothing to lose
	return fn(tx)
}

// ErrNoRowsAffected reports a guarded UPDATE that matched nothing. Under this
// design that is an expected outcome, not necessarily a failure.
var ErrNoRowsAffected = errors.New("sqlite: guarded update matched no rows")

// isUniqueViolation reports whether err is a UNIQUE constraint failure over a
// particular set of columns. Identifying which constraint fired is what
// separates "this exact proposal already exists" from any other constraint the
// table enforces.
//
// The match is on the column list rather than the index name because
// modernc.org/sqlite reports the failure as
// "UNIQUE constraint failed: operations.plugin, operations.action, ..." and
// never names the index. Matching an index name here silently matched nothing,
// which sent every duplicate-proposal insert down the generic error path.
func isUniqueViolation(err error, columns string) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") && strings.Contains(msg, columns)
}

// IsImmutabilityViolation reports whether err came from one of the append-only
// or immutability triggers. These indicate a bug in calling code attempting a
// write the design forbids, and should never be retried.
func IsImmutabilityViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "is immutable") ||
		strings.Contains(msg, "append-only")
}

// PluginStatePut upserts a namespaced plugin value.
func (u *UnitOfWork) PluginStatePut(plugin, key, valueJSON string) error {
	_, err := u.exec(`
		INSERT INTO plugin_state (plugin, key, value_json, updated_at)
		VALUES (?,?,?,?)
		ON CONFLICT (plugin, key) DO UPDATE
		SET value_json = excluded.value_json, updated_at = excluded.updated_at`,
		plugin, key, valueJSON, u.now)
	return err
}

// PluginStateDelete removes a namespaced plugin value.
func (u *UnitOfWork) PluginStateDelete(plugin, key string) error {
	_, err := u.exec(`DELETE FROM plugin_state WHERE plugin = ? AND key = ?`, plugin, key)
	return err
}

// EnqueueEvent queues an outbox event outside an operation transaction. It is
// used by plugin publishers, whose domain events are not tied to a state
// change.
func (u *UnitOfWork) EnqueueEvent(subject, operationID, correlationID string, payload []byte) error {
	_, err := u.exec(`
		INSERT INTO outbox_events (event_id, subject, operation_id, correlation_id,
		                           payload_json, created_at, next_attempt_at)
		VALUES (?,?,?,?,?,?,0)`,
		newEventID(), subject, nullStr(operationID), correlationID,
		string(payload), u.now)
	return err
}

// Exec runs a statement inside the transaction, discarding the result. It is
// exported so packages outside this one can compose their own writes into a
// UnitOfWork without gaining access to the underlying connection.
func (u *UnitOfWork) Exec(query string, args ...any) error {
	_, err := u.exec(query, args...)
	return err
}

// ExecAffected runs a statement and reports how many rows it changed. Callers
// use it to implement guarded updates of their own.
func (u *UnitOfWork) ExecAffected(query string, args ...any) (int64, error) {
	res, err := u.exec(query, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// QueryRow runs a single-row query inside the transaction, so a caller can
// read its own uncommitted writes.
func (u *UnitOfWork) QueryRow(query string, args ...any) *sql.Row {
	return u.queryRow(query, args...)
}
