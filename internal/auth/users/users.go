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
	"unicode"
	"unicode/utf8"

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
	// ErrAlreadyClaimed reports registration attempted on an instance that
	// already has an account. Registration exists to claim an unclaimed
	// instance; once claimed, accounts are made by an administrator.
	ErrAlreadyClaimed = errors.New("users: this instance already has an account")
	// ErrNameCollides reports a display name that is another account's
	// address. A name is for reading; letting one read as somebody else's
	// identity is how a list of who did what stops being one.
	ErrNameCollides = errors.New("users: that name is another account's address")
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

// Name is what to render wherever this account is shown.
//
// A display name is optional, and an account without one still has to appear
// somewhere in a heading, a list, or a line saying who approved something. The
// address is what that falls back to: technical, but it names a real person,
// which an empty string does not.
//
// It falls back for a stored value the rules now refuse, too, and that is the
// second job this does. The column predates every rule about what may go in
// it, so a database may hold a name written when nothing was checked -- one
// carrying a bidirectional override, or an invisible character, or a byte that
// is not UTF-8. The schema cannot help: a CHECK can express the length and
// nothing else, and enumerating the format characters in SQL would cover a
// score of the hundred and seventy in the category and drift from this
// package the first time either changed. Re-checking on the way out is the
// same rule, applied by the same code, to every row however it got there --
// and every render goes through here.
//
// The stored value is left alone rather than corrected. It stays visible as
// display_name so its owner can see what is there and replace it, which they
// can now do themselves; guessing what it was meant to say is not this
// function's business.
//
// This is never an identity. Two accounts may render the same, and the value
// changes whenever its owner decides to change it, so anything that has to
// name *which* account -- the audit trail above all -- uses the address.
func (u *User) Name() string {
	if name, err := ValidateDisplayName(u.DisplayName); err == nil && name != "" {
		return name
	}
	return u.Email
}

// Principal renders the account as a verified caller.
//
// The session identifier becomes the TokenID so that an audit record names the
// sign-in that performed an act, not merely the person: revoking one session
// leaves the others, and the trail can tell them apart.
//
// ID is built from the address rather than from the display name, and that is
// the whole of why a display name is safe to let people change: every audit
// record, every guard and every grant is keyed on the address, and the name is
// carried alongside for a human to read.
func (u *User) Principal(sessionID string) *auth.Principal {
	return &auth.Principal{
		ID:          "user:" + u.Email,
		DisplayName: u.Name(),
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

// --- display names ---------------------------------------------------------

// MaxDisplayNameRunes bounds a display name.
//
// Counted in runes rather than bytes so that a name in a script whose
// characters cost three bytes each is not a third as long as one in ASCII.
// The database enforces the same bound, in the same units: SQLite's length()
// counts characters of text.
const MaxDisplayNameRunes = 64

// ValidateDisplayName checks and normalises a display name, returning the form
// to store. An empty result is legitimate and means the account has no display
// name; it renders as its address.
//
// Three rules, and each exists for something that actually happens:
//
//   - Well-formed UTF-8. Ranging over a string yields U+FFFD for a malformed
//     byte rather than the byte itself, so the checks below would pass it and
//     the column would store it -- and the JSON encoder substitutes U+FFFD on
//     the way out, leaving the operator looking at a name they cannot correct
//     by retyping it, because what they typed was never what was stored.
//   - Length, because an unbounded name is a row somebody else's browser has
//     to render and a column this host has to hold.
//   - No control or format characters. A newline in a name breaks a log line
//     into two, and a bidirectional override renders "alice" as something
//     else entirely -- both are ways to make a name read as something it is
//     not, which is the one thing a name must not do.
//   - Not an address belonging to somebody else. That one cannot be decided
//     here, because it is a question about the other rows; it is a condition
//     in the WHERE clause of the write, where a race cannot slip past it.
func ValidateDisplayName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", nil
	}
	if !utf8.ValidString(name) {
		return "", fmt.Errorf("users: a display name must be valid UTF-8")
	}
	for _, r := range name {
		switch {
		case unicode.IsControl(r):
			return "", fmt.Errorf("users: a display name cannot contain control characters")
		case unicode.Is(unicode.Cf, r):
			// Cf is the invisible-formatting category: zero-width joiners,
			// bidirectional overrides, and the rest of the characters whose
			// entire purpose is to change how the text around them renders.
			return "", fmt.Errorf("users: a display name cannot contain invisible formatting characters")
		}
	}
	if utf8.RuneCountInString(name) > MaxDisplayNameRunes {
		return "", fmt.Errorf("users: a display name must be at most %d characters",
			MaxDisplayNameRunes)
	}
	return name, nil
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
