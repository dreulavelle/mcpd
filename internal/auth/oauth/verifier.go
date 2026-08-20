package oauth

import (
	"context"
	"net/http"
	"time"

	"github.com/spoked/mcpd/internal/auth"
)

// Verifier is the resource-server half: it turns a bearer access token into a
// Principal that the host's authorizer can act on.
type Verifier struct {
	store *Store
	now   func() time.Time
}

// NewVerifier returns a verifier backed by store.
func NewVerifier(store *Store, now func() time.Time) *Verifier {
	if now == nil {
		now = time.Now
	}
	return &Verifier{store: store, now: now}
}

// Scheme implements auth.TokenVerifier.
func (v *Verifier) Scheme() string { return "oauth" }

// Verify implements auth.TokenVerifier.
//
// Every failure returns auth.ErrUnauthenticated with no detail. Distinguishing
// an unknown token from an expired or revoked one would let a caller probe
// which tokens once existed.
func (v *Verifier) Verify(ctx context.Context, token string, _ *http.Request) (*auth.Principal, error) {
	if token == "" {
		return nil, auth.ErrUnauthenticated
	}

	// The lookup is by digest, so the plaintext token is never compared
	// against anything stored and never appears in a query log.
	stored, err := v.store.TokenByHash(ctx, HashSecret(token))
	if err != nil {
		return nil, auth.ErrUnauthenticated
	}
	if stored.Kind != KindAccess || !stored.Active(v.now()) {
		return nil, auth.ErrUnauthenticated
	}

	user, err := v.store.UserByID(ctx, stored.UserID)
	if err != nil || user.Disabled {
		// A disabled account stops working immediately, without waiting for
		// its outstanding tokens to expire.
		return nil, auth.ErrUnauthenticated
	}

	return &auth.Principal{
		ID:          user.ID,
		DisplayName: user.Username,
		// The role is derived from the token's scope intersected with the
		// user's role, not from the user's role alone. A user who delegated
		// only read access cannot have that token used to propose, even
		// though the user themselves could.
		Role:    roleFromScope(stored.Scope, user.Role),
		Plugins: PluginsFromScope(stored.Scope),
		// OAuth identities are per user, so separation of duties is
		// enforceable. This is the difference that makes high-risk approvals
		// possible at all.
		Distinguishable: true,
		TokenID:         stored.ClientID,
	}, nil
}

// roleFromScope derives the effective role for a token.
//
// The result can never exceed the user's own role: capabilitiesForRole gates
// what the authorization endpoint was willing to put in the scope in the first
// place, and this narrows it further to what the token actually carries.
func roleFromScope(scope, userRole string) auth.Role {
	caps := capabilitiesForRole(userRole)

	switch {
	case HasScope(scope, ScopeApprove) && caps[ScopeApprove]:
		return auth.RoleApprover
	case HasScope(scope, ScopePropose) && caps[ScopePropose]:
		return auth.RoleOperator
	case HasScope(scope, ScopeRead):
		return auth.RoleViewer
	default:
		// A token with no recognised scope holds nothing. Returning an empty
		// role means every capability check fails closed.
		return ""
	}
}

// MultiVerifier tries several verifiers in order, which is what "mixed" auth
// mode does: OAuth for interactive clients like ChatGPT, static tokens for
// machine callers that cannot complete a browser flow.
type MultiVerifier struct {
	verifiers []auth.TokenVerifier
}

// NewMultiVerifier composes verifiers. Order matters only for which scheme is
// reported in logs; a token either verifies against one of them or none.
func NewMultiVerifier(verifiers ...auth.TokenVerifier) *MultiVerifier {
	return &MultiVerifier{verifiers: verifiers}
}

// Scheme implements auth.TokenVerifier.
func (m *MultiVerifier) Scheme() string { return "mixed" }

// Verify implements auth.TokenVerifier.
func (m *MultiVerifier) Verify(ctx context.Context, token string, r *http.Request) (*auth.Principal, error) {
	for _, v := range m.verifiers {
		if p, err := v.Verify(ctx, token, r); err == nil {
			return p, nil
		}
	}
	return nil, auth.ErrUnauthenticated
}
