package sso

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/auth/users"
	"github.com/spoked/mcpd/internal/cachestore"
)

// Service runs the provider flows.
//
// Providers are read per call rather than captured at construction, because
// they are settings: an operator who has just pasted a client secret should be
// able to sign in with it without restarting the host, which is the same
// reason the tunnel directory is a function here.
type Service struct {
	providers    func(ctx context.Context) []Config
	redirectBase func() string
	states       *StateStore
	log          *slog.Logger
	now          func() time.Time
	http         *http.Client
	cache        *cachestore.Store
	group        *cachestore.Group
}

// Options configures a Service.
type Options struct {
	// Providers returns the configured providers. Called per flow.
	Providers func(ctx context.Context) []Config
	// RedirectBase is how a browser reaches this dashboard. Empty means the
	// host does not know, and every flow is refused with a reason rather than
	// guessed at.
	RedirectBase func() string
	States       *StateStore
	Log          *slog.Logger
	Now          func() time.Time
	// Cache holds discovery documents and signing keys. One store, shared,
	// because what is being bounded is this process's memory.
	Cache *cachestore.Store
}

// NewService builds the flow runner.
func NewService(opts Options) *Service {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Providers == nil {
		opts.Providers = func(context.Context) []Config { return nil }
	}
	if opts.RedirectBase == nil {
		opts.RedirectBase = func() string { return "" }
	}
	if opts.Cache == nil {
		opts.Cache = cachestore.New(32)
	}
	return &Service{
		providers:    opts.Providers,
		redirectBase: opts.RedirectBase,
		states:       opts.States,
		log:          opts.Log,
		now:          opts.Now,
		http:         newHTTPClient(),
		cache:        opts.Cache,
		group:        &cachestore.Group{},
	}
}

// Available lists the providers a sign-in page should offer.
//
// A provider missing its client id, its secret, or Entra's tenant is not
// offered. A button that leads to a refusal is worse than no button: it looks
// like this host is broken rather than like it has not been set up.
func (s *Service) Available(ctx context.Context) []Descriptor {
	if s.redirectBase() == "" {
		// Without an address to come back to there is no flow to start, and
		// the Authentication page says so in as many words.
		return nil
	}
	out := []Descriptor{}
	for _, c := range s.providers(ctx) {
		if c.Ready() {
			out = append(out, Descriptor{Provider: string(c.Provider), Label: Label(c.Provider)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out
}

// StartRequest is one flow about to begin.
type StartRequest struct {
	Provider users.Provider
	Purpose  Purpose
	// UserID is the account a link attaches to. Ignored for a sign-in.
	UserID string
	// ReturnTo is where to send the browser afterwards. Bounded to a path on
	// this dashboard.
	ReturnTo string
}

// Started is what a caller needs to send a browser to a provider.
type Started struct {
	// AuthorizationURL is where the browser goes.
	AuthorizationURL string
	// Binding is the secret to put in the browser's own cookie. It never
	// leaves this host by any other route, which is what makes the state
	// useless to a different browser.
	Binding string
}

// Start issues a state and builds the authorization URL.
func (s *Service) Start(ctx context.Context, req StartRequest) (*Started, error) {
	c, err := s.configFor(ctx, req.Provider)
	if err != nil {
		return nil, err
	}
	if !req.Purpose.Valid() {
		return nil, fmt.Errorf("sso: %q is not a purpose", req.Purpose)
	}
	redirect, err := RedirectURI(s.redirectBase(), req.Provider)
	if err != nil {
		return nil, err
	}

	st := State{
		Provider:    req.Provider,
		Purpose:     req.Purpose,
		UserID:      req.UserID,
		RedirectURI: redirect,
		ReturnTo:    req.ReturnTo,
	}

	params := url.Values{}
	params.Set("client_id", c.ClientID)
	params.Set("redirect_uri", redirect)
	params.Set("response_type", "code")

	var authorize string
	switch req.Provider {
	case users.ProviderGitHub:
		// GitHub is not OpenID Connect: no id token, so no nonce, and its
		// OAuth apps do not implement PKCE. Sending a challenge it ignores
		// would look like protection this flow does not have. What stands in
		// its place is what PKCE protects against being already absent here:
		// the code is exchanged by this process with a client secret over a
		// server-to-server request, never by a public client.
		authorize = "https://github.com/login/oauth/authorize"
		params.Set("scope", "read:user user:email")
		// GitHub reuses an existing authorization silently, which makes a
		// second sign-in indistinguishable from a first. Nothing here depends
		// on that, and asking again on every sign-in would be noise.
	default:
		d, err := s.discover(ctx, c)
		if err != nil {
			return nil, err
		}
		authorize = d.AuthorizationEndpoint
		params.Set("scope", "openid email profile")

		if st.Nonce, err = secret(); err != nil {
			return nil, err
		}
		params.Set("nonce", st.Nonce)

		if st.CodeVerifier, err = secret(); err != nil {
			return nil, err
		}
		challenge := sha256.Sum256([]byte(st.CodeVerifier))
		params.Set("code_challenge", base64.RawURLEncoding.EncodeToString(challenge[:]))
		params.Set("code_challenge_method", "S256")
	}

	state, binding, err := s.states.Issue(ctx, st)
	if err != nil {
		return nil, err
	}
	params.Set("state", state)

	return &Started{
		AuthorizationURL: authorize + "?" + params.Encode(),
		Binding:          binding,
	}, nil
}

// Complete claims the state and exchanges the code for an identity.
//
// The state is claimed first, before anything is sent to the provider. A
// callback this host is not waiting for should cost nothing: exchanging the
// code first would let anybody with a URL make this process call out, and
// would spend a code the real browser is about to present.
func (s *Service) Complete(ctx context.Context, provider users.Provider, code, state, binding string) (*State, *Identity, error) {
	if code == "" {
		return nil, nil, ErrState
	}
	st, err := s.states.Claim(ctx, provider, state, binding)
	if err != nil {
		return nil, nil, err
	}
	c, err := s.configFor(ctx, provider)
	if err != nil {
		return nil, nil, err
	}
	// The redirect the flow began with, not the one the current configuration
	// would build. Several providers require the exchange to repeat it
	// verbatim, and a public URL edited mid-flow should fail here with
	// something an operator can read rather than at the provider.
	if want, err := RedirectURI(s.redirectBase(), provider); err != nil || want != st.RedirectURI {
		return nil, nil, fmt.Errorf(
			"%w: this host's address changed while the sign-in was in progress", ErrProvider)
	}

	var identity *Identity
	if provider == users.ProviderGitHub {
		identity, err = s.completeGitHub(ctx, c, st, code)
	} else {
		identity, err = s.completeOIDC(ctx, c, st, code)
	}
	if err != nil {
		return nil, nil, err
	}
	return st, identity, nil
}

// tokenResponse is the part of a token endpoint's answer this host reads.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	IDToken     string `json:"id_token"`
	// Error and its description are what a provider sends instead of a token.
	// They arrive with a 200 from at least one of the three, so they are read
	// rather than left to the status code.
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func (t tokenResponse) err(host string) error {
	if t.Error == "" {
		return nil
	}
	return fmt.Errorf("%w: %s refused the exchange: %s (%s)",
		ErrProvider, host, t.Error, snippet([]byte(t.ErrorDescription)))
}

func (s *Service) completeOIDC(ctx context.Context, c Config, st *State, code string) (*Identity, error) {
	d, err := s.discover(ctx, c)
	if err != nil {
		return nil, err
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {st.RedirectURI},
		"client_id":     {c.ClientID},
		"client_secret": {c.ClientSecret},
	}
	if st.CodeVerifier != "" {
		form.Set("code_verifier", st.CodeVerifier)
	}
	var tok tokenResponse
	if err := postForm(ctx, s.http, d.TokenEndpoint, form, &tok); err != nil {
		return nil, err
	}
	if err := tok.err(d.Issuer); err != nil {
		return nil, err
	}
	if tok.IDToken == "" {
		return nil, fmt.Errorf("%w: %s returned no id token", ErrProvider, d.Issuer)
	}

	claims, err := s.verifyIDToken(ctx, c, d, tok.IDToken, st.Nonce)
	if err != nil {
		return nil, err
	}
	email, err := addressFrom(c, claims)
	if err != nil {
		return nil, err
	}
	return &Identity{
		Provider: c.Provider,
		Subject:  claims.Subject,
		Email:    email,
		Name:     strings.TrimSpace(claims.Name),
	}, nil
}

// addressFrom decides which address a set of claims establishes.
//
// Google states `email` and `email_verified`, and an unverified address is
// refused: Google will hand one out for an account that merely typed it in,
// and this host uses the address to decide whether a stranger may register
// under an allowed domain.
//
// Entra is where the two providers genuinely differ, and it is worth being
// precise rather than pretending they are the same. Entra does not issue
// `email_verified` at all, and its `email` claim is optional -- a work account
// commonly carries the address in `preferred_username` instead. What stands in
// place of the missing claim is the tenant: this host refuses to run Entra
// without a directory id, so every token it accepts was minted by one
// directory for one of its own members, and the address in it was assigned by
// that directory's administrator rather than asserted by its holder. That is a
// stronger guarantee than `email_verified`, not a weaker one -- but it is a
// different guarantee, so it is written down here rather than left to be
// inferred from the absence of a check.
func addressFrom(c Config, claims *idClaims) (string, error) {
	candidate := strings.TrimSpace(claims.Email)
	if c.Provider == users.ProviderEntra {
		if candidate == "" {
			candidate = strings.TrimSpace(claims.PreferredUsername)
		}
		if candidate == "" {
			return "", ErrNoVerifiedEmail
		}
		// preferred_username is not required to be an address; it is only
		// usable here when it parses as one.
		email, err := users.NormalizeEmail(candidate)
		if err != nil {
			return "", ErrNoVerifiedEmail
		}
		return email, nil
	}
	if candidate == "" || !bool(claims.Verified) {
		return "", ErrNoVerifiedEmail
	}
	email, err := users.NormalizeEmail(candidate)
	if err != nil {
		return "", ErrNoVerifiedEmail
	}
	return email, nil
}
