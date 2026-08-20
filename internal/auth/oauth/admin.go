package oauth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// bcryptCost is deliberately above the library default of 10. Login happens
// once per browser session rather than per request, so a slower hash costs
// nothing noticeable and raises the price of an offline attack on a leaked
// database.
const bcryptCost = 12

// HashPassword derives the stored form of a password.
func HashPassword(plaintext string) (string, error) {
	if err := ValidatePassword(plaintext); err != nil {
		return "", err
	}
	h, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("oauth: hash password: %w", err)
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
		return fmt.Errorf("oauth: password must be at least %d characters", minLen)
	}
	// bcrypt silently truncates beyond 72 bytes, which would make two
	// different long passwords equivalent. Reject rather than truncate.
	if len(p) > 72 {
		return fmt.Errorf("oauth: password must be at most 72 bytes")
	}
	return nil
}

// CreateUserRequest describes a new identity.
type CreateUserRequest struct {
	Username    string
	Password    string
	DisplayName string
	Role        string
	Plugins     []string
}

// CreateUser provisions an identity.
func (s *Store) CreateUserWithPassword(ctx context.Context, req CreateUserRequest) (*User, error) {
	username := strings.ToLower(strings.TrimSpace(req.Username))
	if username == "" {
		return nil, fmt.Errorf("oauth: username is required")
	}
	if !validRole(req.Role) {
		return nil, fmt.Errorf("oauth: role %q is not one of viewer, operator, approver, admin", req.Role)
	}
	if len(req.Plugins) == 0 {
		return nil, fmt.Errorf("oauth: user %s grants no plugin access; "+
			`list plugins explicitly or use ["*"]`, username)
	}

	hash, err := HashPassword(req.Password)
	if err != nil {
		return nil, err
	}
	id, err := NewID("usr_")
	if err != nil {
		return nil, err
	}

	u := &User{
		ID:           id,
		Username:     username,
		PasswordHash: hash,
		DisplayName:  req.DisplayName,
		Role:         req.Role,
		Plugins:      req.Plugins,
	}
	if err := s.CreateUser(ctx, u); err != nil {
		return nil, err
	}
	return u, nil
}

func validRole(r string) bool {
	switch r {
	case "viewer", "operator", "approver", "admin":
		return true
	}
	return false
}

// Housekeeper periodically removes spent codes, sessions, and dead tokens.
type Housekeeper struct {
	store    *Store
	interval time.Duration
	// retain keeps revoked tokens around after expiry so that replaying a
	// rotated refresh token is still recognised as reuse rather than as an
	// unknown credential.
	retain time.Duration
}

// NewHousekeeper returns a background cleaner.
func NewHousekeeper(store *Store, interval, retain time.Duration) *Housekeeper {
	if interval <= 0 {
		interval = time.Hour
	}
	if retain <= 0 {
		retain = 7 * 24 * time.Hour
	}
	return &Housekeeper{store: store, interval: interval, retain: retain}
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
			if err := h.store.PurgeExpired(ctx, h.retain); err != nil {
				return fmt.Errorf("oauth: housekeeping: %w", err)
			}
		}
	}
}
