package users

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/storage/sqlite"
)

// Store persists accounts and sessions. Passwords are held as bcrypt hashes
// and session tokens as SHA-256 digests, so a database leak yields neither a
// usable password nor a usable session.
type Store struct {
	db  *sqlite.DB
	now func() time.Time
}

// NewStore returns a store backed by db.
func NewStore(db *sqlite.DB, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, now: now}
}

const userColumns = `id, email, password_hash, display_name, role, plugins_json,
	                 disabled, created_at, updated_at, last_login_at`

// --- accounts --------------------------------------------------------------

// CreateRequest describes a new account.
type CreateRequest struct {
	Email       string
	Password    string
	DisplayName string
	Role        auth.Role
	Plugins     []string
}

// Create provisions an account.
func (s *Store) Create(ctx context.Context, req CreateRequest) (*User, error) {
	email, err := NormalizeEmail(req.Email)
	if err != nil {
		return nil, err
	}
	if !req.Role.Valid() {
		return nil, fmt.Errorf("users: role %q is not one of viewer, operator, approver, admin", req.Role)
	}
	// An empty grant denies everything, which is the safe reading but almost
	// never what someone meant to type. Saying so beats creating an account
	// that silently reaches nothing.
	if len(req.Plugins) == 0 {
		return nil, fmt.Errorf(`users: account %s grants no plugin access; `+
			`list plugins explicitly or use ["*"]`, email)
	}
	hash, err := HashPassword(req.Password)
	if err != nil {
		return nil, err
	}
	id, err := newID("usr_")
	if err != nil {
		return nil, err
	}

	u := &User{
		ID:           id,
		Email:        email,
		PasswordHash: hash,
		DisplayName:  strings.TrimSpace(req.DisplayName),
		Role:         req.Role,
		Plugins:      req.Plugins,
	}
	plugins, err := json.Marshal(u.Plugins)
	if err != nil {
		return nil, fmt.Errorf("users: encode plugin grants: %w", err)
	}

	now := s.now().UnixMilli()
	err = s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		return tx.Exec(`
			INSERT INTO users (id, email, password_hash, display_name, role,
			                   plugins_json, disabled, created_at, updated_at)
			VALUES (?,?,?,?,?,?,0,?,?)`,
			u.ID, u.Email, u.PasswordHash, u.DisplayName, string(u.Role),
			string(plugins), now, now)
	})
	if isUniqueViolation(err) {
		return nil, ErrDuplicateEmail
	}
	if err != nil {
		return nil, err
	}
	u.CreatedAt = time.UnixMilli(now).UTC()
	u.UpdatedAt = u.CreatedAt
	return u, nil
}

// ByEmail loads an account by address. The address is normalised first, so a
// caller may pass whatever the sign-in form collected.
func (s *Store) ByEmail(ctx context.Context, email string) (*User, error) {
	normalized, err := NormalizeEmail(email)
	if err != nil {
		return nil, ErrNotFound
	}
	return s.scanUser(s.db.Reader().QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE email = ?`, normalized))
}

// ByID loads an account by identifier.
func (s *Store) ByID(ctx context.Context, id string) (*User, error) {
	return s.scanUser(s.db.Reader().QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE id = ?`, id))
}

// List returns every account, for the dashboard.
func (s *Store) List(ctx context.Context) ([]*User, error) {
	rows, err := s.db.Reader().QueryContext(ctx,
		`SELECT `+userColumns+` FROM users ORDER BY email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*User
	for rows.Next() {
		u, err := s.scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// Count reports how many accounts exist, so startup can tell whether
// bootstrapping is needed.
func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	err := s.db.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// UpdateRequest describes an edit. Nil fields are left alone, which is what
// lets the dashboard send only what changed.
type UpdateRequest struct {
	DisplayName *string
	Role        *auth.Role
	Plugins     *[]string
	Disabled    *bool
}

// Update edits an account.
//
// The last-administrator guard runs inside the write transaction. Checking
// first and writing after would let two concurrent edits each observe the
// other's administrator and both proceed, leaving a host no one can administer.
func (s *Store) Update(ctx context.Context, id string, req UpdateRequest) (*User, error) {
	if req.Role != nil && !req.Role.Valid() {
		return nil, fmt.Errorf("users: role %q is not one of viewer, operator, approver, admin", *req.Role)
	}
	if req.Plugins != nil && len(*req.Plugins) == 0 {
		return nil, fmt.Errorf(`users: an account granting no plugin access reaches nothing; ` +
			`list plugins explicitly or use ["*"]`)
	}

	now := s.now().UnixMilli()
	err := s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		var role string
		var disabled int
		if err := tx.QueryRow(
			`SELECT role, disabled FROM users WHERE id = ?`, id).
			Scan(&role, &disabled); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}

		// Losing administrator rights, whether by role change or by being
		// switched off, is the same event as far as the guard is concerned.
		wasAdmin := auth.Role(role) == auth.RoleAdmin && disabled == 0
		stillAdmin := wasAdmin
		if req.Role != nil {
			stillAdmin = *req.Role == auth.RoleAdmin && stillAdmin
		}
		if req.Disabled != nil && *req.Disabled {
			stillAdmin = false
		}
		if wasAdmin && !stillAdmin {
			if err := guardLastAdmin(tx, id); err != nil {
				return err
			}
		}

		sets := []string{"updated_at = ?"}
		args := []any{now}
		if req.DisplayName != nil {
			sets = append(sets, "display_name = ?")
			args = append(args, strings.TrimSpace(*req.DisplayName))
		}
		if req.Role != nil {
			sets = append(sets, "role = ?")
			args = append(args, string(*req.Role))
		}
		if req.Plugins != nil {
			encoded, err := json.Marshal(*req.Plugins)
			if err != nil {
				return fmt.Errorf("users: encode plugin grants: %w", err)
			}
			sets = append(sets, "plugins_json = ?")
			args = append(args, string(encoded))
		}
		if req.Disabled != nil {
			sets = append(sets, "disabled = ?")
			args = append(args, boolInt(*req.Disabled))
		}
		args = append(args, id)
		if err := tx.Exec(
			`UPDATE users SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...); err != nil {
			return err
		}

		// A disabled account must not keep browsing on a session it already
		// holds, and a demoted one must not keep the rights its old session
		// was resolved with. Sessions carry no capabilities themselves -- they
		// are re-resolved per request -- so only the disable case strictly
		// needs this, but dropping them on any privilege change keeps the rule
		// simple: an edit takes effect immediately, everywhere.
		if req.Disabled != nil || req.Role != nil || req.Plugins != nil {
			return tx.Exec(`DELETE FROM user_sessions WHERE user_id = ?`, id)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.ByID(ctx, id)
}

// SetPassword replaces an account's password and signs out its sessions.
//
// Ending the sessions is the point of a password change as often as not: the
// person changing it may be doing so because they believe the old one leaked,
// and leaving live sessions behind would defeat that.
func (s *Store) SetPassword(ctx context.Context, id, password string) error {
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	now := s.now().UnixMilli()
	return s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		affected, err := tx.ExecAffected(
			`UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`,
			hash, now, id)
		if err != nil {
			return err
		}
		if affected == 0 {
			return ErrNotFound
		}
		return tx.Exec(`DELETE FROM user_sessions WHERE user_id = ?`, id)
	})
}

// Delete removes an account and every session it holds.
func (s *Store) Delete(ctx context.Context, id string) error {
	now := s.now().UnixMilli()
	return s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		var role string
		var disabled int
		if err := tx.QueryRow(
			`SELECT role, disabled FROM users WHERE id = ?`, id).
			Scan(&role, &disabled); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if auth.Role(role) == auth.RoleAdmin && disabled == 0 {
			if err := guardLastAdmin(tx, id); err != nil {
				return err
			}
		}
		// Sessions cascade on the foreign key, but saying so here keeps the
		// behaviour true of a database restored without foreign keys on.
		if err := tx.Exec(`DELETE FROM user_sessions WHERE user_id = ?`, id); err != nil {
			return err
		}
		return tx.Exec(`DELETE FROM users WHERE id = ?`, id)
	})
}

// guardLastAdmin refuses an edit that would leave no enabled administrator.
func guardLastAdmin(tx *sqlite.UnitOfWork, excludingID string) error {
	var others int
	if err := tx.QueryRow(`
		SELECT COUNT(*) FROM users
		WHERE role = 'admin' AND disabled = 0 AND id <> ?`, excludingID).
		Scan(&others); err != nil {
		return err
	}
	if others == 0 {
		return ErrLastAdmin
	}
	return nil
}

// Authenticate verifies an email and password.
//
// A wrong address and a wrong password are indistinguishable to the caller and
// cost the same time, because an unmatched address is still compared against a
// decoy hash. Together those keep the sign-in form from answering "does this
// account exist?".
func (s *Store) Authenticate(ctx context.Context, email, password string) (*User, error) {
	u, err := s.ByEmail(ctx, email)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	hash := dummyHash
	if u != nil && !u.Disabled {
		hash = u.PasswordHash
	}
	if !comparePassword(hash, password) {
		return nil, ErrInvalidCredentials
	}
	// A disabled account matches the decoy above, never its own hash, so this
	// is unreachable by a correct password on a disabled account. It stands as
	// a second gate rather than a comment.
	if u == nil || u.Disabled {
		return nil, ErrInvalidCredentials
	}
	return u, nil
}

// RecordLogin stamps a successful sign-in.
func (s *Store) RecordLogin(ctx context.Context, userID string) error {
	now := s.now().UnixMilli()
	return s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		return tx.Exec(`UPDATE users SET last_login_at = ? WHERE id = ?`, now, userID)
	})
}

type rowScanner interface{ Scan(...any) error }

func (s *Store) scanUser(row rowScanner) (*User, error) {
	var (
		u           User
		role        string
		pluginsJSON string
		disabled    int
		created     int64
		updated     int64
		lastLogin   sql.NullInt64
	)
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.DisplayName, &role,
		&pluginsJSON, &disabled, &created, &updated, &lastLogin)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(pluginsJSON), &u.Plugins); err != nil {
		return nil, fmt.Errorf("users: decode plugin grants for %s: %w", u.Email, err)
	}
	u.Role = auth.Role(role)
	u.Disabled = disabled != 0
	u.CreatedAt = time.UnixMilli(created).UTC()
	u.UpdatedAt = time.UnixMilli(updated).UTC()
	if lastLogin.Valid {
		t := time.UnixMilli(lastLogin.Int64).UTC()
		u.LastLoginAt = &t
	}
	return &u, nil
}

// --- sessions --------------------------------------------------------------

// NewSession issues a browser session and returns the token to set as a
// cookie. The token is returned once and never stored in recoverable form.
func (s *Store) NewSession(ctx context.Context, userID string, ttl time.Duration) (token string, sess *Session, err error) {
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}
	token, err = generateSecret()
	if err != nil {
		return "", nil, err
	}
	csrf, err := generateSecret()
	if err != nil {
		return "", nil, err
	}
	id, err := newID("ses_")
	if err != nil {
		return "", nil, err
	}

	now := s.now()
	expires := now.Add(ttl)
	err = s.db.WriteTx(ctx, now.UnixMilli(), func(tx *sqlite.UnitOfWork) error {
		return tx.Exec(`
			INSERT INTO user_sessions (session_hash, id, user_id, csrf_token, created_at, expires_at)
			VALUES (?,?,?,?,?,?)`,
			hashSecret(token), id, userID, csrf, now.UnixMilli(), expires.UnixMilli())
	})
	if err != nil {
		return "", nil, err
	}
	return token, &Session{
		ID:        id,
		UserID:    userID,
		CSRFToken: csrf,
		CreatedAt: now.UTC(),
		ExpiresAt: expires.UTC(),
	}, nil
}

// ResolveSession turns a session token into its account.
//
// A session for a disabled account resolves to nothing: rights are re-read on
// every request rather than frozen at sign-in, so switching an account off
// takes effect on its next call.
func (s *Store) ResolveSession(ctx context.Context, token string) (*User, *Session, error) {
	if token == "" {
		return nil, nil, ErrNotFound
	}
	var (
		sess    Session
		created int64
		expires int64
	)
	err := s.db.Reader().QueryRowContext(ctx, `
		SELECT id, user_id, csrf_token, created_at, expires_at
		FROM user_sessions WHERE session_hash = ? AND expires_at > ?`,
		hashSecret(token), s.now().UnixMilli()).
		Scan(&sess.ID, &sess.UserID, &sess.CSRFToken, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	sess.CreatedAt = time.UnixMilli(created).UTC()
	sess.ExpiresAt = time.UnixMilli(expires).UTC()

	u, err := s.ByID(ctx, sess.UserID)
	if err != nil {
		return nil, nil, err
	}
	if u.Disabled {
		return nil, nil, ErrNotFound
	}
	return u, &sess, nil
}

// DeleteSession ends one browser session.
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	now := s.now().UnixMilli()
	return s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		return tx.Exec(`DELETE FROM user_sessions WHERE session_hash = ?`, hashSecret(token))
	})
}

// PurgeExpiredSessions removes sessions past their expiry.
func (s *Store) PurgeExpiredSessions(ctx context.Context) error {
	now := s.now().UnixMilli()
	return s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		return tx.Exec(`DELETE FROM user_sessions WHERE expires_at < ?`, now)
	})
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// isUniqueViolation reports whether err is a uniqueness failure.
//
// The driver's error is matched by text because the SQLite drivers in use
// disagree on their error types, and the constraint name is stable across all
// of them.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// Housekeeper removes expired sessions.
//
// Expiry is enforced on read, so this is hygiene rather than correctness: a
// row past its expires_at stops resolving whether or not it has been deleted.
// What it prevents is the table growing without bound in a deployment that
// signs in often and never restarts.
type Housekeeper struct {
	store    *Store
	interval time.Duration
}

// NewHousekeeper returns a background cleaner.
func NewHousekeeper(store *Store, interval time.Duration) *Housekeeper {
	if interval <= 0 {
		interval = time.Hour
	}
	return &Housekeeper{store: store, interval: interval}
}

// Run cleans until ctx is cancelled.
func (h *Housekeeper) Run(ctx context.Context) error {
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := h.store.PurgeExpiredSessions(ctx); err != nil {
				return fmt.Errorf("users: housekeeping: %w", err)
			}
		}
	}
}
