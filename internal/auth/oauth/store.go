package oauth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/spoked/mcpd/internal/storage/sqlite"
)

// Store persists OAuth state. Every credential is stored as a digest, so a
// database leak yields nothing usable.
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

// --- users -----------------------------------------------------------------

// CreateUser inserts a user. The caller supplies an already-hashed password.
func (s *Store) CreateUser(ctx context.Context, u *User) error {
	plugins, err := json.Marshal(u.Plugins)
	if err != nil {
		return fmt.Errorf("oauth: encode plugin grants: %w", err)
	}
	now := s.now().UnixMilli()
	return s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		return tx.Exec(`
			INSERT INTO users (id, username, password_hash, display_name, role,
			                   plugins_json, disabled, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?)`,
			u.ID, u.Username, u.PasswordHash, u.DisplayName, u.Role,
			string(plugins), boolInt(u.Disabled), now, now)
	})
}

// UserByUsername loads an enabled user by name.
func (s *Store) UserByUsername(ctx context.Context, username string) (*User, error) {
	return s.scanUser(s.db.Reader().QueryRowContext(ctx, `
		SELECT id, username, password_hash, display_name, role, plugins_json,
		       disabled, created_at, updated_at, last_login_at
		FROM users WHERE username = ?`, username))
}

// UserByID loads a user by identifier.
func (s *Store) UserByID(ctx context.Context, id string) (*User, error) {
	return s.scanUser(s.db.Reader().QueryRowContext(ctx, `
		SELECT id, username, password_hash, display_name, role, plugins_json,
		       disabled, created_at, updated_at, last_login_at
		FROM users WHERE id = ?`, id))
}

// ListUsers returns every user, for the admin interface.
func (s *Store) ListUsers(ctx context.Context) ([]*User, error) {
	rows, err := s.db.Reader().QueryContext(ctx, `
		SELECT id, username, password_hash, display_name, role, plugins_json,
		       disabled, created_at, updated_at, last_login_at
		FROM users ORDER BY username`)
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

// RecordLogin stamps a successful authentication.
func (s *Store) RecordLogin(ctx context.Context, userID string) error {
	now := s.now().UnixMilli()
	return s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		return tx.Exec(`UPDATE users SET last_login_at = ? WHERE id = ?`, now, userID)
	})
}

// CountUsers reports how many identities exist, so startup can tell whether
// bootstrapping is needed.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

type rowScanner interface{ Scan(...any) error }

func (s *Store) scanUser(row rowScanner) (*User, error) {
	var (
		u           User
		pluginsJSON string
		disabled    int
		created     int64
		updated     int64
		lastLogin   sql.NullInt64
	)
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.DisplayName, &u.Role,
		&pluginsJSON, &disabled, &created, &updated, &lastLogin)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(pluginsJSON), &u.Plugins); err != nil {
		return nil, fmt.Errorf("oauth: decode plugin grants for %s: %w", u.Username, err)
	}
	u.Disabled = disabled != 0
	u.CreatedAt = time.UnixMilli(created).UTC()
	u.UpdatedAt = time.UnixMilli(updated).UTC()
	if lastLogin.Valid {
		t := time.UnixMilli(lastLogin.Int64).UTC()
		u.LastLoginAt = &t
	}
	return &u, nil
}

// --- clients ---------------------------------------------------------------

// UpsertClient inserts or refreshes a client registration.
func (s *Store) UpsertClient(ctx context.Context, c *Client, registrationTokenHash string) error {
	uris, err := json.Marshal(c.RedirectURIs)
	if err != nil {
		return fmt.Errorf("oauth: encode redirect uris: %w", err)
	}
	now := s.now().UnixMilli()
	return s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		return tx.Exec(`
			INSERT INTO oauth_clients (client_id, client_secret_hash, client_name,
			                           redirect_uris_json, registration_type,
			                           registration_token_hash, created_at, updated_at, disabled)
			VALUES (?,?,?,?,?,?,?,?,0)
			ON CONFLICT (client_id) DO UPDATE SET
				client_name        = excluded.client_name,
				redirect_uris_json = excluded.redirect_uris_json,
				updated_at         = excluded.updated_at`,
			c.ID, nullIfEmpty(c.SecretHash), c.Name, string(uris), string(c.Type),
			nullIfEmpty(registrationTokenHash), now, now)
	})
}

// ClientByID loads an enabled client.
func (s *Store) ClientByID(ctx context.Context, id string) (*Client, error) {
	var (
		c        Client
		secret   sql.NullString
		urisJSON string
		regType  string
		created  int64
		updated  int64
		disabled int
	)
	err := s.db.Reader().QueryRowContext(ctx, `
		SELECT client_id, client_secret_hash, client_name, redirect_uris_json,
		       registration_type, created_at, updated_at, disabled
		FROM oauth_clients WHERE client_id = ?`, id).
		Scan(&c.ID, &secret, &c.Name, &urisJSON, &regType, &created, &updated, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrClientNotFound
	}
	if err != nil {
		return nil, err
	}
	if disabled != 0 {
		return nil, ErrClientNotFound
	}
	if err := json.Unmarshal([]byte(urisJSON), &c.RedirectURIs); err != nil {
		return nil, fmt.Errorf("oauth: decode redirect uris for %s: %w", id, err)
	}
	c.SecretHash = secret.String
	c.Type = RegistrationType(regType)
	c.CreatedAt = time.UnixMilli(created).UTC()
	c.UpdatedAt = time.UnixMilli(updated).UTC()
	return &c, nil
}

// --- authorization codes ---------------------------------------------------

// SaveAuthCode persists a pending authorization.
func (s *Store) SaveAuthCode(ctx context.Context, c *AuthCode) error {
	now := s.now().UnixMilli()
	return s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		return tx.Exec(`
			INSERT INTO oauth_auth_codes (code_hash, client_id, user_id, redirect_uri,
			                              scope, code_challenge, code_challenge_method,
			                              created_at, expires_at)
			VALUES (?,?,?,?,?,?,?,?,?)`,
			c.CodeHash, c.ClientID, c.UserID, c.RedirectURI, c.Scope,
			c.CodeChallenge, c.CodeChallengeMethod, now, c.ExpiresAt.UnixMilli())
	})
}

// ConsumeAuthCode atomically redeems an authorization code.
//
// The guarded UPDATE is what makes redemption single-use under concurrency:
// two simultaneous exchanges of the same code produce one winner and one
// ErrInvalidGrant, with no window between checking and marking.
func (s *Store) ConsumeAuthCode(ctx context.Context, codeHash string) (*AuthCode, error) {
	var code AuthCode
	now := s.now()

	err := s.db.WriteTx(ctx, now.UnixMilli(), func(tx *sqlite.UnitOfWork) error {
		affected, err := tx.ExecAffected(`
			UPDATE oauth_auth_codes SET consumed_at = ?
			WHERE code_hash = ? AND consumed_at IS NULL AND expires_at > ?`,
			now.UnixMilli(), codeHash, now.UnixMilli())
		if err != nil {
			return err
		}
		if affected != 1 {
			return ErrInvalidGrant
		}
		var created, expires int64
		return tx.QueryRow(`
			SELECT code_hash, client_id, user_id, redirect_uri, scope,
			       code_challenge, code_challenge_method, created_at, expires_at
			FROM oauth_auth_codes WHERE code_hash = ?`, codeHash).
			Scan(&code.CodeHash, &code.ClientID, &code.UserID, &code.RedirectURI,
				&code.Scope, &code.CodeChallenge, &code.CodeChallengeMethod,
				&created, &expires)
	})
	if err != nil {
		return nil, err
	}
	return &code, nil
}

// --- tokens ----------------------------------------------------------------

// SaveToken persists an issued credential.
func (s *Store) SaveToken(ctx context.Context, t *Token) error {
	now := s.now().UnixMilli()
	return s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		return tx.Exec(`
			INSERT INTO oauth_tokens (token_hash, kind, client_id, user_id, scope,
			                          created_at, expires_at, parent_hash, lineage_id)
			VALUES (?,?,?,?,?,?,?,?,?)`,
			t.Hash, string(t.Kind), t.ClientID, t.UserID, t.Scope,
			now, t.ExpiresAt.UnixMilli(), nullIfEmpty(t.ParentHash), t.LineageID)
	})
}

// TokenByHash loads a credential by digest.
func (s *Store) TokenByHash(ctx context.Context, hash string) (*Token, error) {
	var (
		t       Token
		kind    string
		created int64
		expires int64
		revoked sql.NullInt64
		parent  sql.NullString
	)
	err := s.db.Reader().QueryRowContext(ctx, `
		SELECT token_hash, kind, client_id, user_id, scope,
		       created_at, expires_at, revoked_at, parent_hash, lineage_id
		FROM oauth_tokens WHERE token_hash = ?`, hash).
		Scan(&t.Hash, &kind, &t.ClientID, &t.UserID, &t.Scope,
			&created, &expires, &revoked, &parent, &t.LineageID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalidGrant
	}
	if err != nil {
		return nil, err
	}
	t.Kind = TokenKind(kind)
	t.CreatedAt = time.UnixMilli(created).UTC()
	t.ExpiresAt = time.UnixMilli(expires).UTC()
	t.ParentHash = parent.String
	if revoked.Valid {
		r := time.UnixMilli(revoked.Int64).UTC()
		t.RevokedAt = &r
	}
	return &t, nil
}

// RotateRefreshToken redeems a refresh token and issues its successor
// atomically.
//
// Reuse detection is the point. A refresh token is single-use, so presenting
// one that has already been rotated means the credential was captured -- the
// legitimate client and the attacker cannot both hold the current one. The
// response is to revoke the entire lineage rather than the single token,
// because there is no way to tell which party is which.
func (s *Store) RotateRefreshToken(ctx context.Context, oldHash string, next *Token) error {
	now := s.now()

	// Reuse is signalled out of the transaction rather than by returning an
	// error from it. Returning an error rolls the transaction back, which
	// would undo the very revocation that detecting reuse exists to perform.
	reuseDetected := false

	err := s.db.WriteTx(ctx, now.UnixMilli(), func(tx *sqlite.UnitOfWork) error {
		var lineage string
		var revoked sql.NullInt64
		var expires int64
		err := tx.QueryRow(`
			SELECT lineage_id, revoked_at, expires_at FROM oauth_tokens
			WHERE token_hash = ? AND kind = 'refresh'`, oldHash).
			Scan(&lineage, &revoked, &expires)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInvalidGrant
		}
		if err != nil {
			return err
		}

		if revoked.Valid {
			// Already rotated or explicitly revoked, and being presented
			// again. The legitimate client and an attacker cannot both hold
			// the current token, and there is no way to tell which is
			// calling, so the whole lineage goes.
			//
			// This must COMMIT: the revocation is the entire point of
			// detection. The caller is told via reuseDetected once the
			// transaction has landed.
			reuseDetected = true
			return tx.Exec(`
				UPDATE oauth_tokens SET revoked_at = ?
				WHERE lineage_id = ? AND revoked_at IS NULL`,
				now.UnixMilli(), lineage)
		}
		if now.UnixMilli() >= expires {
			return ErrInvalidGrant
		}

		// Revoke the presented token and every access token issued alongside
		// it, so the old pair stops working the instant the new one exists.
		if err := tx.Exec(`
			UPDATE oauth_tokens SET revoked_at = ?
			WHERE lineage_id = ? AND revoked_at IS NULL AND
			      (token_hash = ? OR kind = 'access')`,
			now.UnixMilli(), lineage, oldHash); err != nil {
			return err
		}

		next.LineageID = lineage
		next.ParentHash = oldHash
		return tx.Exec(`
			INSERT INTO oauth_tokens (token_hash, kind, client_id, user_id, scope,
			                          created_at, expires_at, parent_hash, lineage_id)
			VALUES (?,?,?,?,?,?,?,?,?)`,
			next.Hash, string(next.Kind), next.ClientID, next.UserID, next.Scope,
			now.UnixMilli(), next.ExpiresAt.UnixMilli(), oldHash, lineage)
	})
	if err != nil {
		return err
	}
	if reuseDetected {
		return ErrTokenReuse
	}
	return nil
}

// RevokeLineage revokes every credential descended from one authorization.
func (s *Store) RevokeLineage(ctx context.Context, lineageID string) error {
	now := s.now().UnixMilli()
	return s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		return tx.Exec(`
			UPDATE oauth_tokens SET revoked_at = ?
			WHERE lineage_id = ? AND revoked_at IS NULL`, now, lineageID)
	})
}

// RevokeToken revokes a single credential, used by the revocation endpoint.
func (s *Store) RevokeToken(ctx context.Context, hash string) error {
	now := s.now().UnixMilli()
	return s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		return tx.Exec(`
			UPDATE oauth_tokens SET revoked_at = ?
			WHERE token_hash = ? AND revoked_at IS NULL`, now, hash)
	})
}

// PurgeExpired removes spent codes, sessions, and long-dead tokens.
//
// Revoked tokens are kept for a grace period rather than deleted immediately:
// reuse detection depends on recognising a token that has already been
// rotated, and a deleted row is indistinguishable from one that never existed.
func (s *Store) PurgeExpired(ctx context.Context, retain time.Duration) error {
	now := s.now()
	cutoff := now.Add(-retain).UnixMilli()
	return s.db.WriteTx(ctx, now.UnixMilli(), func(tx *sqlite.UnitOfWork) error {
		if err := tx.Exec(`DELETE FROM oauth_auth_codes WHERE expires_at < ?`, cutoff); err != nil {
			return err
		}
		if err := tx.Exec(`DELETE FROM oauth_sessions WHERE expires_at < ?`, now.UnixMilli()); err != nil {
			return err
		}
		return tx.Exec(`DELETE FROM oauth_tokens WHERE expires_at < ?`, cutoff)
	})
}

// --- login sessions --------------------------------------------------------

// SaveSession records a browser login session.
func (s *Store) SaveSession(ctx context.Context, hash, userID string, expiresAt time.Time) error {
	now := s.now().UnixMilli()
	return s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		return tx.Exec(`
			INSERT INTO oauth_sessions (session_hash, user_id, created_at, expires_at)
			VALUES (?,?,?,?)
			ON CONFLICT (session_hash) DO UPDATE SET expires_at = excluded.expires_at`,
			hash, userID, now, expiresAt.UnixMilli())
	})
}

// SessionUser resolves a live session to its user.
func (s *Store) SessionUser(ctx context.Context, hash string) (*User, error) {
	var userID string
	err := s.db.Reader().QueryRowContext(ctx, `
		SELECT user_id FROM oauth_sessions WHERE session_hash = ? AND expires_at > ?`,
		hash, s.now().UnixMilli()).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.UserByID(ctx, userID)
}

// DeleteSession ends a browser session.
func (s *Store) DeleteSession(ctx context.Context, hash string) error {
	now := s.now().UnixMilli()
	return s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		return tx.Exec(`DELETE FROM oauth_sessions WHERE session_hash = ?`, hash)
	})
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
