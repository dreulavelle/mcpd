// Package oauth implements the authorization-server and resource-server halves
// of the OAuth 2.1 flow that ChatGPT and other MCP clients use to reach mcpd.
//
// mcpd is both server roles at once. That makes opaque tokens the right
// choice: there is no third party needing to validate a token offline, so a
// random string looked up in SQLite beats a signed assertion on revocation
// latency, operational burden, and dependency surface.
package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// TokenKind distinguishes access tokens from refresh tokens.
type TokenKind string

const (
	KindAccess  TokenKind = "access"
	KindRefresh TokenKind = "refresh"
)

// RegistrationType records how a client came to exist.
type RegistrationType string

const (
	// RegDynamic is RFC 7591 dynamic client registration.
	RegDynamic RegistrationType = "dcr"
	// RegCIMD is a Client ID Metadata Document: the client_id is an HTTPS URL
	// serving the client's own metadata. This supersedes DCR in the
	// 2026-07-28 MCP revision.
	RegCIMD RegistrationType = "cimd"
	// RegStatic is a client provisioned by an administrator.
	RegStatic RegistrationType = "static"
)

// Client is a registered OAuth client.
type Client struct {
	ID           string
	SecretHash   string // empty for public clients
	Name         string
	RedirectURIs []string
	Type         RegistrationType
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Disabled     bool
}

// IsPublic reports whether the client authenticates with PKCE alone. ChatGPT
// registers as a public client.
func (c *Client) IsPublic() bool { return c.SecretHash == "" }

// AllowsRedirect reports whether uri is a registered callback.
//
// Matching is exact. Prefix or wildcard matching on redirect URIs is the
// single most reliable way to turn an authorization server into an open
// redirector, and every shortcut here has a published attack.
func (c *Client) AllowsRedirect(uri string) bool {
	for _, u := range c.RedirectURIs {
		if u == uri {
			return true
		}
	}
	return false
}

// User is a local identity that can log in and authorize a client.
type User struct {
	ID           string
	Username     string
	PasswordHash string
	DisplayName  string
	Role         string
	Plugins      []string
	Disabled     bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
	LastLoginAt  *time.Time
}

// AuthCode is a pending authorization awaiting exchange.
type AuthCode struct {
	CodeHash            string
	ClientID            string
	UserID              string
	RedirectURI         string
	Scope               string
	CodeChallenge       string
	CodeChallengeMethod string
	CreatedAt           time.Time
	ExpiresAt           time.Time
	ConsumedAt          *time.Time
}

// Token is an issued credential.
type Token struct {
	Hash       string
	Kind       TokenKind
	ClientID   string
	UserID     string
	Scope      string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	RevokedAt  *time.Time
	ParentHash string
	LineageID  string
}

// Active reports whether a token may still be used.
func (t *Token) Active(now time.Time) bool {
	return t.RevokedAt == nil && now.Before(t.ExpiresAt)
}

// --- credential generation and hashing -------------------------------------

// tokenBytes is the entropy behind every generated credential. 32 bytes puts
// guessing far beyond reach, which is what lets these be compared by digest
// rather than run through a slow KDF.
const tokenBytes = 32

// GenerateSecret returns a URL-safe random credential.
func GenerateSecret() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("oauth: system entropy unavailable: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashSecret derives the stored form of a credential.
//
// SHA-256 without a salt is correct here and would be wrong for a password.
// These values carry 256 bits of entropy from a CSPRNG, so there is no
// dictionary to precompute and no work factor worth paying. Salting would only
// prevent the cross-record comparison that lookup by digest depends on.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// NewID returns a random identifier for a user, client, or lineage.
func NewID(prefix string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("oauth: system entropy unavailable: %w", err)
	}
	return prefix + hex.EncodeToString(b), nil
}

// --- PKCE ------------------------------------------------------------------

// VerifyPKCE checks a code verifier against the stored challenge.
//
// Only S256 is accepted. The "plain" method offers no protection against an
// attacker who can observe the authorization request, and OAuth 2.1 removes
// it; accepting it for compatibility would mean the strongest client and the
// weakest get the same guarantee.
func VerifyPKCE(verifier, challenge, method string) error {
	if method != "S256" {
		return fmt.Errorf("oauth: unsupported code_challenge_method %q; only S256 is accepted", method)
	}
	// RFC 7636 section 4.1.
	if n := len(verifier); n < 43 || n > 128 {
		return fmt.Errorf("oauth: code_verifier must be 43-128 characters, got %d", n)
	}
	if !isUnreservedPKCE(verifier) {
		return fmt.Errorf("oauth: code_verifier contains characters outside the permitted set")
	}
	sum := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(sum[:])
	if computed != challenge {
		return ErrInvalidGrant
	}
	return nil
}

// isUnreservedPKCE reports whether s uses only the characters RFC 7636 permits
// in a code verifier.
func isUnreservedPKCE(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-', r == '.', r == '_', r == '~':
		default:
			return false
		}
	}
	return true
}

// --- scopes ----------------------------------------------------------------

// Scope names a permission requested by a client. Scopes are how a user grants
// an agent access to some plugins and not others.
const (
	// ScopeRead permits read tools.
	ScopeRead = "mcp:read"
	// ScopePropose permits proposing mutations. It never permits executing one.
	ScopePropose = "mcp:propose"
	// ScopeApprove permits approving a proposed mutation.
	ScopeApprove = "mcp:approve"
)

// PluginScopePrefix builds the scope that grants access to one plugin, e.g.
// "mcp:plugin:cnmaestro". A token carrying no plugin scope reaches no plugin.
const PluginScopePrefix = "mcp:plugin:"

// PluginScope returns the scope string granting access to a named plugin.
func PluginScope(name string) string { return PluginScopePrefix + name }

// ParseScopes splits a space-delimited scope string.
func ParseScopes(s string) []string {
	return strings.Fields(s)
}

// JoinScopes renders scopes back into their wire form.
func JoinScopes(scopes []string) string { return strings.Join(scopes, " ") }

// HasScope reports whether a scope string contains a scope.
func HasScope(scopeStr, want string) bool {
	for _, s := range strings.Fields(scopeStr) {
		if s == want {
			return true
		}
	}
	return false
}

// PluginsFromScope extracts the plugin names a scope string grants.
func PluginsFromScope(scopeStr string) []string {
	var out []string
	for _, s := range strings.Fields(scopeStr) {
		if name, ok := strings.CutPrefix(s, PluginScopePrefix); ok && name != "" {
			out = append(out, name)
		}
	}
	return out
}
