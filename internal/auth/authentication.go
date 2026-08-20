package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Authentication failures. These are deliberately coarse: a caller learns that
// their credential was not accepted, never why. Distinguishing "unknown token"
// from "expired token" hands an attacker a probing oracle.
var (
	// ErrUnauthenticated reports a missing, malformed, or unrecognised
	// credential.
	ErrUnauthenticated = errors.New("auth: unauthenticated")
	// ErrForbidden reports a verified principal lacking the required
	// capability or plugin grant.
	ErrForbidden = errors.New("auth: forbidden")
)

// TokenVerifier turns a bearer credential into a Principal.
//
// Implementations must not distinguish failure modes in the returned error;
// return ErrUnauthenticated for anything that is not a successful
// verification.
type TokenVerifier interface {
	Verify(ctx context.Context, token string, r *http.Request) (*Principal, error)
	// Scheme names the mechanism for logs and health output, e.g. "static" or
	// "oauth". It must not reveal configuration details.
	Scheme() string
}

// --- static tokens ---------------------------------------------------------

// StaticToken is one configured credential. The plaintext token never reaches
// this struct: only its SHA-256 digest is retained, so a memory dump or a
// mistakenly logged config struct cannot yield a working credential.
type StaticToken struct {
	// ID names the credential for audit and revocation.
	ID string
	// digest is the SHA-256 of the token bytes.
	digest [sha256.Size]byte
	// Principal is issued on a successful match.
	Principal Principal
}

// NewStaticToken derives a credential entry from a plaintext token. The
// plaintext is not retained.
func NewStaticToken(id, plaintext string, p Principal) (*StaticToken, error) {
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("auth: static token requires an id")
	}
	// 32 bytes of entropy, base64-encoded, is 43 characters. Anything much
	// shorter is either a typo or a guessable secret.
	if len(plaintext) < 32 {
		return nil, fmt.Errorf("auth: token %s is too short; use at least 32 characters", id)
	}
	p.TokenID = id
	// A static token identifies a credential, not a human. Two callers sharing
	// it are indistinguishable, and separation of duties must know that.
	p.Distinguishable = false
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &StaticToken{ID: id, digest: sha256.Sum256([]byte(plaintext)), Principal: p}, nil
}

// StaticVerifier authenticates against a fixed set of tokens. It suits
// machine-to-machine callers and local development; ChatGPT connects over
// OAuth instead.
type StaticVerifier struct {
	tokens []*StaticToken
}

// NewStaticVerifier builds a verifier over the supplied credentials.
func NewStaticVerifier(tokens ...*StaticToken) (*StaticVerifier, error) {
	seen := make(map[string]bool, len(tokens))
	for _, t := range tokens {
		if t == nil {
			return nil, fmt.Errorf("auth: nil static token")
		}
		if seen[t.ID] {
			return nil, fmt.Errorf("auth: duplicate token id %q", t.ID)
		}
		seen[t.ID] = true
	}
	return &StaticVerifier{tokens: tokens}, nil
}

// Scheme implements TokenVerifier.
func (v *StaticVerifier) Scheme() string { return "static" }

// Verify implements TokenVerifier.
//
// Every configured token is compared even after a match is found, and the
// comparison itself is constant-time. This keeps the response time
// independent of both which token matched and how many are configured, so
// timing cannot be used to enumerate credentials.
func (v *StaticVerifier) Verify(_ context.Context, token string, _ *http.Request) (*Principal, error) {
	if token == "" {
		return nil, ErrUnauthenticated
	}
	presented := sha256.Sum256([]byte(token))

	var matched *StaticToken
	for _, t := range v.tokens {
		if subtle.ConstantTimeCompare(presented[:], t.digest[:]) == 1 {
			matched = t
		}
	}
	if matched == nil {
		return nil, ErrUnauthenticated
	}
	p := matched.Principal
	return &p, nil
}

// --- request plumbing ------------------------------------------------------

// BearerToken extracts a bearer credential from an Authorization header.
//
// The scheme match is case-insensitive per RFC 7235, but the token itself is
// returned verbatim.
func BearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", false
	}
	const prefix = "bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(h[len(prefix):])
	if token == "" {
		return "", false
	}
	return token, true
}

// contextKey is unexported so no other package can inject a principal into a
// context and bypass verification.
type contextKey struct{}

// WithPrincipal returns a context carrying a verified principal. Only the
// authentication middleware should call it.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, contextKey{}, p)
}

// FromContext returns the verified principal, or Anonymous if the request was
// never authenticated. It never returns nil, so callers cannot forget a nil
// check and accidentally skip an authorization test.
func FromContext(ctx context.Context) *Principal {
	if p, ok := ctx.Value(contextKey{}).(*Principal); ok && p != nil {
		return p
	}
	return Anonymous()
}

// Fingerprint returns a short, non-reversible identifier for a credential,
// suitable for correlating log lines about the same token without recording
// the token. It is truncated because it is an identifier, not a digest to be
// verified against.
func Fingerprint(token string) string {
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:6])
}

// GenerateToken produces a credential with 32 bytes of entropy, encoded
// URL-safely so it survives headers, environment variables and shell quoting.
func GenerateToken(random func([]byte) (int, error)) (string, error) {
	buf := make([]byte, 32)
	if _, err := random(buf); err != nil {
		return "", fmt.Errorf("auth: generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
