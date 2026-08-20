package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/operations"
	"github.com/spoked/mcpd/internal/storage"
)

// OperationStore implements storage.OperationRepository.
type OperationStore struct {
	db  *DB
	now func() time.Time
}

// NewOperationStore returns a repository backed by db. now is injectable so
// tests can drive expiry deterministically.
func NewOperationStore(db *DB, now func() time.Time) *OperationStore {
	if now == nil {
		now = time.Now
	}
	return &OperationStore{db: db, now: now}
}

// opColumns is the canonical projection. Every scan path uses it so that a
// column added to one query cannot be forgotten in another.
const opColumns = `
	id, plugin, action, state, risk,
	target_json, params_json, payload_hash,
	before_json, desired_json, precondition_json, rollback_json, changes_json,
	impact, requested_by, requested_at, expires_at,
	approved_by, approved_at, approval_expires_at,
	attempt_count, lease_owner, lease_expires_at,
	terminal_at, outcome_verified, observed_json, error_code, error_detail,
	correlation_id, idempotency_key`

// Propose implements TX-1: insert the operation, its idempotency record, its
// first transition, its audit entry and its outbox event, atomically.
func (s *OperationStore) Propose(ctx context.Context, req storage.ProposeRequest) (*operations.Operation, error) {
	op := req.Operation
	if op == nil {
		return nil, fmt.Errorf("sqlite: propose requires an operation")
	}

	// An idempotency key that was already used must either return the original
	// operation or be refused. Checking before the insert keeps the common
	// replay case out of the constraint-violation path.
	if existing, err := s.byIdempotencyKey(ctx, op.Plugin, op.IdempotencyKey, req.RequestHash); err != nil {
		return nil, err
	} else if existing != nil {
		return existing, nil
	}

	now := s.now().UnixMilli()
	err := s.db.WriteTx(ctx, now, func(u *UnitOfWork) error {
		_, err := u.exec(`
			INSERT INTO operations (
				id, plugin, action, state, risk,
				target_json, params_json, payload_hash,
				before_json, desired_json, precondition_json, rollback_json, changes_json,
				impact, requested_by, requested_at, expires_at,
				correlation_id, idempotency_key
			) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			op.ID, op.Plugin, op.Action, string(op.State), string(op.Risk),
			string(op.Target), string(op.Params), op.PayloadHash,
			nullJSON(op.Before), nullJSON(op.Desired), nullJSON(op.Preconditions),
			nullJSON(op.Rollback), nullJSON(marshalChanges(op.Changes)),
			op.Impact, op.RequestedBy, op.RequestedAt.UnixMilli(), op.ExpiresAt.UnixMilli(),
			op.CorrelationID, op.IdempotencyKey)
		if err != nil {
			if IsConstraint(err) {
				return storage.ErrIdempotencyConflict
			}
			return fmt.Errorf("sqlite: insert operation: %w", err)
		}

		ttl := req.IdempotencyTTL
		if ttl <= 0 {
			ttl = 24 * time.Hour
		}
		if _, err := u.exec(`
			INSERT INTO idempotency_records (scope, key, request_hash, operation_id, created_at, expires_at)
			VALUES (?,?,?,?,?,?)`,
			op.Plugin, op.IdempotencyKey, req.RequestHash, op.ID,
			now, s.now().Add(ttl).UnixMilli()); err != nil {
			return fmt.Errorf("sqlite: insert idempotency record: %w", err)
		}

		if err := u.recordTransition(op.ID, "", string(op.State), op.RequestedBy, "proposed", op.CorrelationID); err != nil {
			return err
		}
		if err := u.appendAudit(req.Audit); err != nil {
			return err
		}
		return u.enqueue(req.Event)
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, op.ID)
}

// Transition implements TX-2 and TX-6: a guarded state change.
//
// The expected source state is part of the UPDATE's WHERE clause, so the check
// and the write are one atomic operation. Two approvers racing cannot both
// succeed: the second finds zero matching rows.
func (s *OperationStore) Transition(ctx context.Context, req storage.TransitionRequest) (*operations.Operation, error) {
	now := s.now().UnixMilli()

	set := []string{"state = ?"}
	args := []any{string(req.To)}

	if req.Approval != nil {
		set = append(set, "approved_by = ?", "approved_at = ?", "approval_expires_at = ?")
		args = append(args,
			req.Approval.ApprovedBy,
			req.Approval.ApprovedAt.UnixMilli(),
			req.Approval.ApprovalExpiresAt.UnixMilli())
	}
	if req.Terminal {
		set = append(set, "terminal_at = ?")
		args = append(args, now)
	}
	if req.ErrorCode != "" {
		set = append(set, "error_code = ?", "error_detail = ?")
		args = append(args, req.ErrorCode, nullStr(req.ErrorDetail))
	}
	args = append(args, req.OperationID, string(req.From))

	query := fmt.Sprintf(
		`UPDATE operations SET %s WHERE id = ? AND state = ?`,
		strings.Join(set, ", "))

	err := s.db.WriteTx(ctx, now, func(u *UnitOfWork) error {
		if err := u.execGuarded(query, args...); err != nil {
			if errors.Is(err, ErrNoRowsAffected) {
				return storage.ErrStateConflict
			}
			return err
		}
		if err := u.recordTransition(req.OperationID, string(req.From), string(req.To),
			req.Actor, req.Reason, req.Audit.CorrelationID); err != nil {
			return err
		}
		if err := u.appendAudit(req.Audit); err != nil {
			return err
		}
		return u.enqueue(req.Event)
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, req.OperationID)
}

// Claim implements TX-3: the guarded transition into execution.
//
// This is the transaction that guarantees at-most-once execution. Three
// conditions live in the WHERE clause rather than in application code:
// the operation must still be approved, its stored payload hash must match
// what the caller verified, and the approval must not have expired. Anything
// else matches zero rows and the caller learns it lost the race.
func (s *OperationStore) Claim(ctx context.Context, req storage.ClaimRequest) (*operations.Operation, error) {
	now := s.now().UnixMilli()
	var attemptNo int

	err := s.db.WriteTx(ctx, now, func(u *UnitOfWork) error {
		err := u.execGuarded(`
			UPDATE operations
			SET state            = 'executing',
			    lease_owner      = ?,
			    lease_expires_at = ?,
			    attempt_count    = attempt_count + 1
			WHERE id = ?
			  AND state = 'approved'
			  AND payload_hash = ?
			  AND approval_expires_at > ?`,
			req.InstanceID, req.LeaseExpiresAt.UnixMilli(),
			req.OperationID, req.ExpectedHash, now)
		if err != nil {
			if errors.Is(err, ErrNoRowsAffected) {
				return operations.ErrClaimLost
			}
			return err
		}

		// Read the incremented count back inside the same transaction so the
		// attempt number cannot be inflated by a lost claim.
		if err := u.queryRow(
			`SELECT attempt_count FROM operations WHERE id = ?`,
			req.OperationID).Scan(&attemptNo); err != nil {
			return fmt.Errorf("sqlite: read attempt count: %w", err)
		}

		if _, err := u.exec(`
			INSERT INTO execution_attempts (id, operation_id, attempt_no, instance_id, started_at)
			VALUES (?,?,?,?,?)`,
			req.AttemptID, req.OperationID, attemptNo, req.InstanceID, now); err != nil {
			return fmt.Errorf("sqlite: insert execution attempt: %w", err)
		}

		if err := u.recordTransition(req.OperationID, string(operations.StateApproved),
			string(operations.StateExecuting), req.InstanceID, "claimed", req.Audit.CorrelationID); err != nil {
			return err
		}
		if err := u.appendAudit(req.Audit); err != nil {
			return err
		}
		return u.enqueue(req.Event)
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, req.OperationID)
}

// Settle implements TX-4: record the outcome of an execution attempt.
func (s *OperationStore) Settle(ctx context.Context, req storage.SettleRequest) (*operations.Operation, error) {
	now := s.now().UnixMilli()

	// Indeterminate is not terminal: it awaits reconciliation.
	terminal := req.To.IsTerminal()

	err := s.db.WriteTx(ctx, now, func(u *UnitOfWork) error {
		var from operations.OperationState
		var raw string
		if err := u.queryRow(`SELECT state FROM operations WHERE id = ?`, req.OperationID).Scan(&raw); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return storage.ErrNotFound
			}
			return err
		}
		from = operations.OperationState(raw)

		set := []string{
			"state = ?", "lease_owner = NULL", "lease_expires_at = NULL",
			"outcome_verified = ?", "observed_json = ?",
			"error_code = ?", "error_detail = ?",
		}
		args := []any{
			string(req.To), nullBool(req.Verified), nullJSON(req.Observed),
			nullStr(req.ErrorCode), nullStr(req.ErrorDetail),
		}
		if terminal {
			set = append(set, "terminal_at = ?")
			args = append(args, now)
		}
		args = append(args, req.OperationID, string(from))

		if err := u.execGuarded(fmt.Sprintf(
			`UPDATE operations SET %s WHERE id = ? AND state = ?`,
			strings.Join(set, ", ")), args...); err != nil {
			if errors.Is(err, ErrNoRowsAffected) {
				return storage.ErrStateConflict
			}
			return err
		}

		if req.AttemptID != "" {
			if _, err := u.exec(`
				UPDATE execution_attempts
				SET finished_at = ?, outcome = ?, upstream_ref = ?,
				    verified = ?, observed_json = ?, error_code = ?, error_detail = ?
				WHERE id = ?`,
				now, string(req.To), nullStr(req.UpstreamRef),
				nullBool(req.Verified), nullJSON(req.Observed),
				nullStr(req.ErrorCode), nullStr(req.ErrorDetail),
				req.AttemptID); err != nil {
				return fmt.Errorf("sqlite: update execution attempt: %w", err)
			}
		}

		if err := u.recordTransition(req.OperationID, string(from), string(req.To),
			req.Actor, req.Reason, req.Audit.CorrelationID); err != nil {
			return err
		}
		if err := u.appendAudit(req.Audit); err != nil {
			return err
		}
		return u.enqueue(req.Event)
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, req.OperationID)
}

// Get loads one operation through the reader pool.
func (s *OperationStore) Get(ctx context.Context, id string) (*operations.Operation, error) {
	row := s.db.Reader().QueryRowContext(ctx,
		`SELECT `+opColumns+` FROM operations WHERE id = ?`, id)
	op, err := scanOperation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	return op, err
}

// List returns operations matching f, newest first.
func (s *OperationStore) List(ctx context.Context, f storage.ListFilter) ([]*operations.Operation, error) {
	var where []string
	var args []any
	if f.Plugin != "" {
		where = append(where, "plugin = ?")
		args = append(args, f.Plugin)
	}
	if len(f.States) > 0 {
		ph := make([]string, len(f.States))
		for i, st := range f.States {
			ph[i] = "?"
			args = append(args, string(st))
		}
		where = append(where, "state IN ("+strings.Join(ph, ",")+")")
	}
	q := `SELECT ` + opColumns + ` FROM operations`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY requested_at DESC, id DESC LIMIT ? OFFSET ?"
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	args = append(args, limit, max(0, f.Offset))

	rows, err := s.db.Reader().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list operations: %w", err)
	}
	defer rows.Close()
	return scanOperations(rows)
}

// Claimable returns approved operations awaiting execution.
func (s *OperationStore) Claimable(ctx context.Context, limit int) ([]*operations.Operation, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Reader().QueryContext(ctx,
		`SELECT `+opColumns+` FROM operations
		 WHERE state = 'approved' AND approval_expires_at > ?
		 ORDER BY requested_at ASC LIMIT ?`,
		s.now().UnixMilli(), limit)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list claimable: %w", err)
	}
	defer rows.Close()
	return scanOperations(rows)
}

// DueForExpiry returns operations the reaper must act on: expired proposals,
// expired approvals, and executing operations whose lease has lapsed.
func (s *OperationStore) DueForExpiry(ctx context.Context, now time.Time, limit int) ([]*operations.Operation, error) {
	if limit <= 0 {
		limit = 100
	}
	ms := now.UnixMilli()
	rows, err := s.db.Reader().QueryContext(ctx,
		`SELECT `+opColumns+` FROM operations
		 WHERE (state = 'pending_approval' AND expires_at <= ?)
		    OR (state = 'approved'         AND approval_expires_at <= ?)
		    OR (state = 'executing'        AND lease_expires_at IS NOT NULL AND lease_expires_at <= ?)
		 ORDER BY requested_at ASC LIMIT ?`,
		ms, ms, ms, limit)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list due for expiry: %w", err)
	}
	defer rows.Close()
	return scanOperations(rows)
}

// byIdempotencyKey resolves a replayed proposal. A matching request hash
// returns the original operation; a differing one is refused, because
// returning the first operation would execute something the caller did not ask
// for.
func (s *OperationStore) byIdempotencyKey(ctx context.Context, scope, key, requestHash string) (*operations.Operation, error) {
	if key == "" {
		return nil, nil
	}
	var opID, storedHash string
	var expiresAt int64
	err := s.db.Reader().QueryRowContext(ctx,
		`SELECT operation_id, request_hash, expires_at FROM idempotency_records WHERE scope = ? AND key = ?`,
		scope, key).Scan(&opID, &storedHash, &expiresAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("sqlite: read idempotency record: %w", err)
	}
	if s.now().UnixMilli() > expiresAt {
		return nil, nil
	}
	if storedHash != requestHash {
		return nil, storage.ErrIdempotencyConflict
	}
	return s.Get(ctx, opID)
}

// --- scanning -------------------------------------------------------------

type scannable interface {
	Scan(dest ...any) error
}

func scanOperation(row scannable) (*operations.Operation, error) {
	var (
		op                                                    operations.Operation
		state, risk                                           string
		target, params                                        string
		before, desired, precond, rollback, changes, observed sql.NullString
		approvedBy, leaseOwner, errCode, errDetail            sql.NullString
		requestedAt, expiresAt                                int64
		approvedAt, approvalExpiresAt, leaseExpiresAt, termAt sql.NullInt64
		verified                                              sql.NullBool
	)
	err := row.Scan(
		&op.ID, &op.Plugin, &op.Action, &state, &risk,
		&target, &params, &op.PayloadHash,
		&before, &desired, &precond, &rollback, &changes,
		&op.Impact, &op.RequestedBy, &requestedAt, &expiresAt,
		&approvedBy, &approvedAt, &approvalExpiresAt,
		&op.AttemptCount, &leaseOwner, &leaseExpiresAt,
		&termAt, &verified, &observed, &errCode, &errDetail,
		&op.CorrelationID, &op.IdempotencyKey)
	if err != nil {
		return nil, err
	}

	op.State = operations.OperationState(state)
	op.Risk = operations.RiskLevel(risk)
	op.Target = json.RawMessage(target)
	op.Params = json.RawMessage(params)
	op.Before = rawOrNil(before)
	op.Desired = rawOrNil(desired)
	op.Preconditions = rawOrNil(precond)
	op.Rollback = rawOrNil(rollback)
	op.Observed = rawOrNil(observed)
	op.RequestedAt = time.UnixMilli(requestedAt).UTC()
	op.ExpiresAt = time.UnixMilli(expiresAt).UTC()
	op.ApprovedBy = approvedBy.String
	op.LeaseOwner = leaseOwner.String
	op.ErrorCode = errCode.String
	op.ErrorDetail = errDetail.String
	op.ApprovedAt = timeOrNil(approvedAt)
	op.ApprovalExpiresAt = timeOrNil(approvalExpiresAt)
	op.LeaseExpiresAt = timeOrNil(leaseExpiresAt)
	op.TerminalAt = timeOrNil(termAt)
	if verified.Valid {
		v := verified.Bool
		op.OutcomeVerified = &v
	}
	if changes.Valid && changes.String != "" {
		if err := json.Unmarshal([]byte(changes.String), &op.Changes); err != nil {
			return nil, fmt.Errorf("sqlite: decode changes for %s: %w", op.ID, err)
		}
	}
	return &op, nil
}

func scanOperations(rows *sql.Rows) ([]*operations.Operation, error) {
	var out []*operations.Operation
	for rows.Next() {
		op, err := scanOperation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

func marshalChanges(c []operations.Change) json.RawMessage {
	if len(c) == 0 {
		return nil
	}
	b, err := json.Marshal(c)
	if err != nil {
		return nil
	}
	return b
}

func rawOrNil(s sql.NullString) json.RawMessage {
	if !s.Valid || s.String == "" {
		return nil
	}
	return json.RawMessage(s.String)
}

func timeOrNil(i sql.NullInt64) *time.Time {
	if !i.Valid {
		return nil
	}
	t := time.UnixMilli(i.Int64).UTC()
	return &t
}

func nullJSON(r json.RawMessage) any {
	if len(r) == 0 {
		return nil
	}
	return string(r)
}

func nullBool(b *bool) any {
	if b == nil {
		return nil
	}
	return *b
}
