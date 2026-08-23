package sso

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/auth/users"
	"github.com/spoked/mcpd/internal/storage/sqlite"
)

// StateTTL bounds how long a half-finished sign-in stays open.
//
// Long enough to sign in at a provider that asks for a second factor, short
// enough that a state left in somebody's history is not a thing anybody can
// come back to. It is enforced in the WHERE clause of the claim, so a row that
// outlives it is untidy rather than usable.
const StateTTL = 10 * time.Minute

// State is one half-finished flow.
type State struct {
	Provider     users.Provider
	Purpose      Purpose
	UserID       string
	CodeVerifier string
	Nonce        string
	RedirectURI  string
	ReturnTo     string
}

// StateStore holds flows between the two halves of a redirect.
//
// A table rather than a signed cookie, because single use has to be enforced
// by something that can refuse the second attempt. A self-contained token
// cannot: it verifies just as well the tenth time it is replayed, and a
// replayed callback is one of the two things the state parameter exists to
// stop. The other -- a state this host never issued -- is answered by there
// being no row.
type StateStore struct {
	db  *sqlite.DB
	now func() time.Time
}

// NewStateStore returns a store backed by db.
func NewStateStore(db *sqlite.DB, now func() time.Time) *StateStore {
	if now == nil {
		now = time.Now
	}
	return &StateStore{db: db, now: now}
}

// Issue records a flow and returns the state parameter and the browser
// binding secret.
//
// Two secrets, and they travel by different routes on purpose. The state goes
// through the provider in a URL and comes back in one; the binding goes into a
// cookie this host sets and the browser returns directly. A callback has to
// present both, which is what makes a state useless to anybody but the browser
// that started the flow -- without it, an attacker who obtains a state (from a
// referer header, a shared screen, a proxy log) can complete the flow in
// somebody else's browser and sign them in as an account the attacker
// controls.
func (s *StateStore) Issue(ctx context.Context, st State) (state, binding string, err error) {
	if !st.Provider.Valid() || !st.Purpose.Valid() {
		return "", "", fmt.Errorf("sso: a state needs a known provider and purpose")
	}
	if st.Purpose == PurposeLink && st.UserID == "" {
		return "", "", fmt.Errorf("sso: a link flow needs the account it is linking to")
	}
	if state, err = secret(); err != nil {
		return "", "", err
	}
	if binding, err = secret(); err != nil {
		return "", "", err
	}

	now := s.now()
	err = s.db.WriteTx(ctx, now.UnixMilli(), func(tx *sqlite.UnitOfWork) error {
		return tx.Exec(`
			INSERT INTO sso_states (state_hash, provider, purpose, binding_hash,
			                        user_id, code_verifier, nonce, redirect_uri,
			                        return_to, created_at, expires_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			digest(state), string(st.Provider), string(st.Purpose), digest(binding),
			nullString(st.UserID), st.CodeVerifier, st.Nonce, st.RedirectURI,
			returnTo(st.ReturnTo), now.UnixMilli(), now.Add(StateTTL).UnixMilli())
	})
	if err != nil {
		return "", "", err
	}
	return state, binding, nil
}

// Claim consumes a state, once.
//
// Every condition is in the WHERE clause: the state exists, it has not been
// used, it has not expired, and the binding matches. A callback failing any of
// them matches zero rows and is refused with one error, because from the
// caller's side they are one sentence and telling them apart would say whether
// a state was ever issued.
//
// The provider is checked too. A flow started for Google and completed at the
// GitHub callback is not a mix-up worth being tolerant about: the two return
// different things and only the route knows which parsing to apply.
func (s *StateStore) Claim(ctx context.Context, provider users.Provider, state, binding string) (*State, error) {
	if state == "" || binding == "" {
		return nil, ErrState
	}
	now := s.now().UnixMilli()

	var out State
	err := s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		affected, err := tx.ExecAffected(`
			UPDATE sso_states SET consumed_at = ?
			 WHERE state_hash = ?
			   AND provider = ?
			   AND binding_hash = ?
			   AND consumed_at IS NULL
			   AND expires_at > ?`,
			now, digest(state), string(provider), digest(binding), now)
		if err != nil {
			return err
		}
		if affected == 0 {
			return ErrState
		}

		var purpose, userID sql.NullString
		var prov string
		if err := tx.QueryRow(`
			SELECT provider, purpose, user_id, code_verifier, nonce, redirect_uri, return_to
			  FROM sso_states WHERE state_hash = ?`, digest(state)).
			Scan(&prov, &purpose, &userID, &out.CodeVerifier, &out.Nonce,
				&out.RedirectURI, &out.ReturnTo); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrState
			}
			return err
		}
		out.Provider = users.Provider(prov)
		out.Purpose = Purpose(purpose.String)
		out.UserID = userID.String
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// Purge removes states past their expiry.
//
// Hygiene rather than correctness: expiry is a condition of the claim, so a
// row left behind stops being usable whether or not it is deleted. What this
// prevents is a table growing without bound on a host people sign in to often.
func (s *StateStore) Purge(ctx context.Context) error {
	now := s.now().UnixMilli()
	return s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		// A consumed state is kept until it expires rather than deleted on
		// use, so a replay inside the window is refused by the guard above
		// instead of looking like a state that was never issued. After expiry
		// the two are indistinguishable anyway.
		return tx.Exec(`DELETE FROM sso_states WHERE expires_at < ?`, now)
	})
}

// returnTo bounds where a finished flow may send the browser.
//
// A path on this dashboard, or "/". An absolute URL, a scheme-relative one, or
// anything carrying a backslash is discarded rather than corrected: a redirect
// target taken from a request is an open redirector, and this one is written
// into a row that a later request acts on without a person seeing it.
func returnTo(raw string) string {
	p := strings.TrimSpace(raw)
	if p == "" || !strings.HasPrefix(p, "/") ||
		strings.HasPrefix(p, "//") || strings.ContainsAny(p, "\\\r\n") {
		return "/"
	}
	return p
}

// secret returns a URL-safe random value with 256 bits behind it.
func secret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("sso: system entropy unavailable: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// digest is the stored form of a state or a binding. These carry 256 bits from
// a CSPRNG, so a plain hash is right and a slow KDF would buy nothing: there is
// no dictionary to precompute, and lookup by digest is what the claim needs.
func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
