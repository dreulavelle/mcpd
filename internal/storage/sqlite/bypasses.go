package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/operations"
)

// BypassStore holds the windows in which this host stops asking.
type BypassStore struct {
	db  *DB
	now func() time.Time
}

func NewBypassStore(db *DB, now func() time.Time) *BypassStore {
	return &BypassStore{db: db, now: now}
}

// ErrBypassTooLong reports a window past the ceiling.
var ErrBypassTooLong = fmt.Errorf(
	"a bypass can last at most %d minutes; there is no way to open one that "+
		"does not end, because a window that never closes is the thing this "+
		"exists instead of", operations.MaxBypassMinutes)

// ErrBypassCritical reports an attempt to authorise critical changes.
var ErrBypassCritical = errors.New(
	"a bypass cannot authorise critical changes. The rule set refuses a " +
		"critical ceiling for the same reason: a level an operator can opt " +
		"out of is not a level")

// Open starts a window.
//
// Bounded here as well as at the API, because this is the last place before
// the row exists and a store that trusted its caller would make the ceiling a
// property of the handler rather than of the feature.
func (s *BypassStore) Open(ctx context.Context, actor string, minutes int, plugin string, ceiling operations.RiskLevel, reason string) (*operations.Bypass, error) {
	if minutes < operations.MinBypassMinutes || minutes > operations.MaxBypassMinutes {
		return nil, ErrBypassTooLong
	}
	if ceiling == operations.RiskCritical || !ceiling.Valid() {
		return nil, ErrBypassCritical
	}

	now := s.now()
	b := &operations.Bypass{
		ID:        newBypassID(),
		Plugin:    plugin,
		Ceiling:   ceiling,
		CreatedAt: now,
		CreatedBy: actor,
		Reason:    reason,
		ExpiresAt: now.Add(time.Duration(minutes) * time.Minute),
	}

	err := s.db.WriteTx(ctx, now.UnixMilli(), func(tx *UnitOfWork) error {
		if _, err := tx.exec(`
			INSERT INTO approval_bypasses (id, created_at, expires_at, created_by, reason, plugin, ceiling)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			b.ID, now.UnixMilli(), b.ExpiresAt.UnixMilli(), actor, reason, plugin, string(ceiling)); err != nil {
			return fmt.Errorf("sqlite: open bypass: %w", err)
		}
		// In the trail, because switching the asking off is an administrative
		// act with consequences somebody may have to account for later.
		return tx.AppendAudit(AdminAct{
			Kind: "approval.bypass.opened", Actor: actor, Subject: b.ID, Action: "open",
			Detail: map[string]any{
				"minutes": minutes, "plugin": plugin,
				"ceiling": string(ceiling), "reason": reason,
				"expires_at": b.ExpiresAt.UTC().Format(time.RFC3339),
			},
		})
	})
	if err != nil {
		return nil, err
	}
	return b, nil
}

// Active returns the window in force now, or nil.
//
// The most permissive one when several are open: two windows are two people
// saying "stop asking", and honouring only the narrower would make the second
// request appear to have done nothing.
func (s *BypassStore) Active(ctx context.Context) (*operations.Bypass, error) {
	now := s.now()
	rows, err := s.db.Reader().QueryContext(ctx, `
		SELECT id, created_at, expires_at, created_by, reason, plugin, ceiling
		  FROM approval_bypasses
		 WHERE revoked_at IS NULL AND expires_at > ?
		 ORDER BY expires_at DESC`, now.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("sqlite: read active bypasses: %w", err)
	}
	defer rows.Close()

	var best *operations.Bypass
	for rows.Next() {
		b, err := scanBypass(rows)
		if err != nil {
			return nil, err
		}
		if best == nil || broader(b, best) {
			best = b
		}
	}
	return best, rows.Err()
}

// broader reports whether a authorises more than b.
//
// One covering every plugin beats one scoped to a single instance; between two
// of the same scope, the higher ceiling wins.
func broader(a, b *operations.Bypass) bool {
	if (a.Plugin == "") != (b.Plugin == "") {
		return a.Plugin == ""
	}
	return a.Ceiling.AtLeast(b.Ceiling) && a.Ceiling != b.Ceiling
}

// List returns recent windows, open or not, newest first.
func (s *BypassStore) List(ctx context.Context, limit int) ([]*operations.Bypass, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Reader().QueryContext(ctx, `
		SELECT id, created_at, expires_at, created_by, reason, plugin, ceiling
		  FROM approval_bypasses
		 ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list bypasses: %w", err)
	}
	defer rows.Close()

	out := []*operations.Bypass{}
	for rows.Next() {
		b, err := scanBypass(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// RevokeAll closes every open window, returning how many it closed.
//
// All of them rather than one by id, because the question somebody asks in a
// hurry is "is anything unsupervised right now" and the answer they want is
// "no". Closing them individually leaves the possibility of missing one.
func (s *BypassStore) RevokeAll(ctx context.Context, actor string) (int64, error) {
	now := s.now()
	var closed int64

	err := s.db.WriteTx(ctx, now.UnixMilli(), func(tx *UnitOfWork) error {
		result, err := tx.exec(`
			UPDATE approval_bypasses
			   SET revoked_at = ?, revoked_by = ?
			 WHERE revoked_at IS NULL AND expires_at > ?`,
			now.UnixMilli(), actor, now.UnixMilli())
		if err != nil {
			return fmt.Errorf("sqlite: revoke bypasses: %w", err)
		}
		closed, _ = result.RowsAffected()
		if closed == 0 {
			return nil
		}
		return tx.AppendAudit(AdminAct{
			Kind: "approval.bypass.revoked", Actor: actor, Subject: "all", Action: "revoke",
			Detail: map[string]any{"closed": closed},
		})
	})
	return closed, err
}

// Approved counts what each window let through, keyed by bypass id.
//
// Read from the operations rather than from a counter beside the window. Every
// operation records the authority that approved it, so this counts rows that
// have to be right anyway -- and cannot drift from them, which a counter
// updated on a separate write could.
func (s *BypassStore) Approved(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.Reader().QueryContext(ctx, `
		SELECT authorized_by_rule, COUNT(*)
		  FROM operations
		 WHERE authorized_by_rule LIKE 'bypass:%'
		 GROUP BY authorized_by_rule`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: count bypassed approvals: %w", err)
	}
	defer rows.Close()

	out := map[string]int64{}
	for rows.Next() {
		var authority string
		var n int64
		if err := rows.Scan(&authority, &n); err != nil {
			return nil, fmt.Errorf("sqlite: scan a bypassed approval count: %w", err)
		}
		out[strings.TrimPrefix(authority, "bypass:")] = n
	}
	return out, rows.Err()
}

func scanBypass(row scanner) (*operations.Bypass, error) {
	var (
		b                operations.Bypass
		created, expires int64
		ceiling          string
		reason, plugin   sql.NullString
	)
	if err := row.Scan(&b.ID, &created, &expires, &b.CreatedBy,
		&reason, &plugin, &ceiling); err != nil {
		return nil, fmt.Errorf("sqlite: scan bypass: %w", err)
	}
	b.CreatedAt = time.UnixMilli(created)
	b.ExpiresAt = time.UnixMilli(expires)
	b.Reason = reason.String
	b.Plugin = plugin.String
	b.Ceiling = operations.RiskLevel(ceiling)
	return &b, nil
}
