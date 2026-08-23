// Package sso signs somebody in through an identity provider they already
// have an account with.
//
// Three providers, and one flow. Google and Entra are OpenID Connect: a
// discovery document says where everything is, the token endpoint returns a
// signed ID token, and the claims in it are the answer. GitHub is not -- it is
// plain OAuth 2.0 with no ID token and no standard claims, so the same flow
// runs and only the last step differs, asking GitHub's own API who the access
// token belongs to. Adapting GitHub to the OIDC shape rather than growing a
// second flow beside it is deliberate: state, PKCE, single-use and the
// redirect pinning are the parts that must not vary by provider, and the way
// to make sure they do not is to have one copy of them.
//
// What this package does not do is decide who somebody is *here*. It returns a
// provider, a subject and an address, and stops. Turning that into an account
// -- which is where linking, registration and approval live -- is the users
// package's, because that is where the rule that an unlinked identity is not
// an account belongs.
package sso

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/spoked/mcpd/internal/auth/users"
)

// Errors this package returns. They are coarse where the difference would tell
// somebody something they should not learn, and precise where an operator has
// to be able to fix what is wrong.
var (
	// ErrNotConfigured reports a provider that is not switched on, or is
	// missing something it cannot run without.
	ErrNotConfigured = errors.New("sso: that provider is not configured")
	// ErrNoRedirectBase reports a host that does not know its own address, so
	// a redirect URI cannot be built. Generating one from the request would
	// produce a URL that works from inside and fails from a browser, which is
	// worse than saying so.
	ErrNoRedirectBase = errors.New("sso: this host does not know its own public address")
	// ErrState reports a callback carrying a state this host did not issue,
	// has already used, or that belongs to a different browser.
	ErrState = errors.New("sso: that sign-in link is not one this host is waiting for")
	// ErrNoVerifiedEmail reports a provider account with no address this host
	// is willing to believe.
	ErrNoVerifiedEmail = errors.New("sso: that account has no verified email address")
	// ErrProvider reports the provider failing or answering with something
	// unusable. The detail is for the operator's log, never the browser.
	ErrProvider = errors.New("sso: the identity provider did not complete the sign-in")
)

// Config is one configured provider.
type Config struct {
	Provider     users.Provider
	ClientID     string
	ClientSecret string
	// TenantID is the Entra directory this host trusts. Entra only, and
	// required there: the tenant is what makes an address in an ID token mean
	// somebody in this organisation rather than anybody with a Microsoft
	// account.
	TenantID string
}

// Ready reports whether a provider has everything it needs to run a flow.
func (c Config) Ready() bool {
	if strings.TrimSpace(c.ClientID) == "" || strings.TrimSpace(c.ClientSecret) == "" {
		return false
	}
	if c.Provider == users.ProviderEntra && !validTenant(c.TenantID) {
		return false
	}
	return c.Provider.Valid()
}

// validTenant reports whether a tenant names one directory.
//
// `common`, `organizations` and `consumers` are refused rather than accepted,
// and this is the one place Entra is materially stricter than Google. Those
// values make the token endpoint issue tokens for *any* directory, and the
// discovery document they publish carries a templated issuer
// ("{tenantid}") which no token's `iss` can ever equal -- so an issuer check
// against it either fails every sign-in or has to be skipped, and skipping it
// is how a tenant nobody in this organisation belongs to gets to mint an
// identity for it. A directory id, or a verified domain, names one directory.
func validTenant(tenant string) bool {
	t := strings.ToLower(strings.TrimSpace(tenant))
	switch t {
	case "", "common", "organizations", "consumers":
		return false
	}
	// Anything else is passed through to discovery, which refuses a templated
	// issuer on its own. Bounded here only so a tenant cannot smuggle a path.
	return !strings.ContainsAny(t, "/?#\\ ")
}

// Descriptor is what the sign-in page is told about a provider.
//
// Deliberately thin, and deliberately safe to serve unauthenticated: a name, a
// label, and whether it will work. Not the client id -- which is not secret
// but is nobody's business before they have signed in -- and certainly not the
// secret.
type Descriptor struct {
	Provider string `json:"provider"`
	Label    string `json:"label"`
}

// Label is what a button says.
func Label(p users.Provider) string {
	switch p {
	case users.ProviderGoogle:
		return "Google"
	case users.ProviderGitHub:
		return "GitHub"
	case users.ProviderEntra:
		return "Microsoft"
	}
	return string(p)
}

// Identity is what a completed flow establishes.
//
// Subject is the provider's own immutable identifier and is what an account is
// keyed on. Email is carried for display and for deciding whether a stranger
// may register, and is never used to find an existing account.
type Identity struct {
	Provider users.Provider
	Subject  string
	Email    string
	Name     string
}

// RedirectURI is the address a provider sends the browser back to.
//
// Derived from the dashboard's configured public URL rather than from the
// request, because it has to match what was registered at the provider exactly
// and because a Host header is set by whoever is talking to this process. An
// empty base is an error rather than a guess: a redirect URI assembled from a
// request works when an operator tests it from the same machine and fails for
// everybody else, which is the worst way for this to be wrong.
func RedirectURI(base string, p users.Provider) (string, error) {
	trimmed := strings.TrimSpace(base)
	if trimmed == "" {
		return "", ErrNoRedirectBase
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("%w: %q is not an absolute URL", ErrNoRedirectBase, base)
	}
	u.RawQuery, u.Fragment = "", ""
	u.Path = strings.TrimSuffix(u.Path, "/") + "/api/auth/sso/" + string(p) + "/callback"
	return u.String(), nil
}

// Purpose is what a flow was started for.
type Purpose string

const (
	// PurposeSignIn begins a session, and may create an account when the host
	// accepts registrations.
	PurposeSignIn Purpose = "signin"
	// PurposeLink attaches a provider to the account that started the flow.
	// It is the only way an account that already exists gains a provider.
	PurposeLink Purpose = "link"
)

// Valid reports whether p is a recognised purpose.
func (p Purpose) Valid() bool { return p == PurposeSignIn || p == PurposeLink }

// providerSet indexes configured providers by name.
type providerSet map[users.Provider]Config

func indexed(configs []Config) providerSet {
	out := make(providerSet, len(configs))
	for _, c := range configs {
		out[c.Provider] = c
	}
	return out
}

// configFor returns a ready provider, or ErrNotConfigured.
func (s *Service) configFor(ctx context.Context, p users.Provider) (Config, error) {
	set := indexed(s.providers(ctx))
	c, ok := set[p]
	if !ok || !c.Ready() {
		return Config{}, fmt.Errorf("%w: %s", ErrNotConfigured, p)
	}
	return c, nil
}
