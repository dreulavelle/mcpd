// Package users holds the local identities that sign in to the dashboard, and
// the browser sessions those sign-ins produce.
//
// An account is identified by email address. There is no separate username:
// two names for one person is two things to keep in step, and the address is
// already the one an operator will recognise in an audit record.
//
// Sessions are opaque random strings stored as digests, not signed assertions.
// mcpd is the only party that validates them, so there is nothing for a
// signature to buy -- and a row in SQLite revokes immediately, which a JWT
// cannot.
package users

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/spoked/mcpd/internal/auth"
)

// Errors returned by this package. They are coarse on purpose: a caller learns
// that a sign-in did not work, never whether the address was the part that was
// wrong. Distinguishing the two turns the login form into an account
// enumerator.
var (
	// ErrNotFound reports an unknown or disabled account.
	ErrNotFound = errors.New("users: account not found")
	// ErrInvalidCredentials reports a failed sign-in, whatever the reason.
	ErrInvalidCredentials = errors.New("users: invalid credentials")
	// ErrDuplicateEmail reports an address already in use.
	ErrDuplicateEmail = errors.New("users: email already registered")
	// ErrLastAdmin reports an edit that would leave no one able to administer
	// the host.
	ErrLastAdmin = errors.New("users: cannot remove the last administrator")
)

// User is a local identity that can sign in to the dashboard.
type User struct {
	ID           string
	Email        string
	PasswordHash string
	DisplayName  string
	Role         auth.Role
	Plugins      []string
	Disabled     bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
	LastLoginAt  *time.Time
}

// Principal renders the account as a verified caller.
//
// The session identifier becomes the TokenID so that an audit record names the
// sign-in that performed an act, not merely the person: revoking one session
// leaves the others, and the trail can tell them apart.
func (u *User) Principal(sessionID string) *auth.Principal {
	name := u.DisplayName
	if name == "" {
		name = u.Email
	}
	return &auth.Principal{
		ID:          "user:" + u.Email,
		DisplayName: name,
		Role:        u.Role,
		Plugins:     append([]string(nil), u.Plugins...),
		TokenID:     sessionID,
	}
}

// Session is a signed-in browser.
type Session struct {
	ID        string
	UserID    string
	CSRFToken string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// --- passwords -------------------------------------------------------------

// bcryptCost is deliberately above the library default of 10. A password is
// verified once per sign-in rather than once per request, so a slower hash
// costs nothing noticeable and raises the price of an offline attack on a
// leaked database.
const bcryptCost = 12

// HashPassword derives the stored form of a password.
func HashPassword(plaintext string) (string, error) {
	if err := ValidatePassword(plaintext); err != nil {
		return "", err
	}
	h, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("users: hash password: %w", err)
	}
	return string(h), nil
}

// ValidatePassword enforces a minimum length.
//
// Length is the only rule. Composition requirements (a digit, a symbol, mixed
// case) measurably push people toward predictable substitutions without adding
// entropy, so a long passphrase is accepted as-is.
func ValidatePassword(p string) error {
	const minLen = 12
	if len(p) < minLen {
		return fmt.Errorf("users: password must be at least %d characters", minLen)
	}
	// bcrypt silently truncates beyond 72 bytes, which would make two
	// different long passwords equivalent. Reject rather than truncate.
	if len(p) > 72 {
		return fmt.Errorf("users: password must be at most 72 bytes")
	}
	return nil
}

// comparePassword reports whether plaintext matches a stored hash.
func comparePassword(hash, plaintext string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext)) == nil
}

// dummyHash is compared against when no account matches, so that a sign-in
// attempt for an unknown address costs the same as one for a known address
// with the wrong password. Without it, response time answers "does this
// account exist?" for anyone willing to measure.
//
// It is the bcrypt hash of a value no one can present, at the same cost as a
// real one.
var dummyHash = mustHash("a-password-no-account-has")

func mustHash(s string) string {
	h, err := bcrypt.GenerateFromPassword([]byte(s), bcryptCost)
	if err != nil {
		panic("users: cannot hash the timing-equalisation value: " + err.Error())
	}
	return string(h)
}

// --- addresses -------------------------------------------------------------

// NormalizeEmail lowercases and trims an address, and checks that it parses.
//
// Addresses are stored in exactly this form, which is what makes the unique
// index meaningful: without it "Alice@example.com" and "alice@example.com"
// would be two accounts for one person.
func NormalizeEmail(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("users: email is required")
	}
	addr, err := mail.ParseAddress(trimmed)
	if err != nil {
		return "", fmt.Errorf("users: %q is not a valid email address", trimmed)
	}
	// ParseAddress accepts a display name ("Alice <a@example.com>"); only the
	// address itself is the identity.
	return strings.ToLower(addr.Address), nil
}

// --- credential generation -------------------------------------------------

// secretBytes is the entropy behind a session token. 32 bytes puts guessing
// far beyond reach, which is what lets these be compared by digest rather than
// run through a slow KDF.
const secretBytes = 32

// generateSecret returns a URL-safe random credential.
func generateSecret() (string, error) {
	b := make([]byte, secretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("users: system entropy unavailable: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashSecret derives the stored form of a session token.
//
// SHA-256 without a salt is correct here and would be wrong for a password.
// These values carry 256 bits of entropy from a CSPRNG, so there is no
// dictionary to precompute and no work factor worth paying. Salting would only
// prevent the lookup by digest that session resolution depends on.
func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// newID returns a random identifier for an account or a session.
func newID(prefix string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("users: system entropy unavailable: %w", err)
	}
	return prefix + hex.EncodeToString(b), nil
}
