package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Config tunes the authorization server.
type Config struct {
	// Issuer is this server's base URL. It must match what clients reach,
	// because it is compared against the audience of every token.
	Issuer string

	// AccessTokenTTL bounds an access token's life. Kept short because
	// revocation of an in-flight token still requires it to expire from any
	// client-side cache.
	AccessTokenTTL time.Duration
	// RefreshTokenTTL bounds a refresh token's life.
	RefreshTokenTTL time.Duration
	// AuthCodeTTL bounds the window between authorization and exchange. RFC
	// 6749 recommends a maximum of ten minutes; one is ample for an
	// interactive redirect.
	AuthCodeTTL time.Duration
	// SessionTTL bounds a browser login session.
	SessionTTL time.Duration

	// AllowDynamicRegistration enables RFC 7591. ChatGPT uses it when Client
	// ID Metadata Documents are unavailable.
	AllowDynamicRegistration bool
	// AllowCIMD enables Client ID Metadata Documents, where the client_id is
	// an HTTPS URL serving the client's own metadata.
	AllowCIMD bool
}

func (c *Config) withDefaults() {
	if c.AccessTokenTTL <= 0 {
		c.AccessTokenTTL = time.Hour
	}
	if c.RefreshTokenTTL <= 0 {
		c.RefreshTokenTTL = 30 * 24 * time.Hour
	}
	if c.AuthCodeTTL <= 0 {
		c.AuthCodeTTL = time.Minute
	}
	if c.SessionTTL <= 0 {
		c.SessionTTL = 30 * time.Minute
	}
}

// Server implements the authorization-server endpoints.
type Server struct {
	cfg   Config
	store *Store
	log   *slog.Logger
	now   func() time.Time
	// cimd fetches Client ID Metadata Documents. Injectable for tests.
	cimd *cimdFetcher
}

// NewServer builds an authorization server.
func NewServer(cfg Config, store *Store, log *slog.Logger, now func() time.Time, httpClient *http.Client) (*Server, error) {
	cfg.withDefaults()
	if cfg.Issuer == "" {
		return nil, fmt.Errorf("oauth: issuer is required")
	}
	if now == nil {
		now = time.Now
	}
	return &Server{
		cfg:   cfg,
		store: store,
		log:   log,
		now:   now,
		cimd:  newCIMDFetcher(httpClient),
	}, nil
}

// Routes registers the authorization-server endpoints on a mux.
func (s *Server) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.handleMetadata)
	mux.HandleFunc("GET /oauth/authorize", s.handleAuthorize)
	mux.HandleFunc("POST /oauth/authorize", s.handleAuthorizeSubmit)
	mux.HandleFunc("POST /oauth/token", s.handleToken)
	mux.HandleFunc("POST /oauth/revoke", s.handleRevoke)
	if s.cfg.AllowDynamicRegistration {
		mux.HandleFunc("POST /oauth/register", s.handleRegister)
	}
}

// --- metadata --------------------------------------------------------------

type authServerMetadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	RevocationEndpoint                string   `json:"revocation_endpoint"`
	RegistrationEndpoint              string   `json:"registration_endpoint,omitempty"`
	ScopesSupported                   []string `json:"scopes_supported"`
	ResponseTypesSupported            []string `json:"response_types_supported"`
	GrantTypesSupported               []string `json:"grant_types_supported"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
	ClientIDMetadataDocumentSupported bool     `json:"client_id_metadata_document_supported,omitempty"`
}

// handleMetadata serves RFC 8414 authorization-server metadata. A client reads
// it to discover every other endpoint, so it is unauthenticated by necessity.
func (s *Server) handleMetadata(w http.ResponseWriter, _ *http.Request) {
	base := strings.TrimRight(s.cfg.Issuer, "/")
	meta := authServerMetadata{
		Issuer:                 base,
		AuthorizationEndpoint:  base + "/oauth/authorize",
		TokenEndpoint:          base + "/oauth/token",
		RevocationEndpoint:     base + "/oauth/revoke",
		ScopesSupported:        []string{ScopeRead, ScopePropose, ScopeApprove},
		ResponseTypesSupported: []string{"code"},
		GrantTypesSupported:    []string{"authorization_code", "refresh_token"},
		// S256 only. OAuth 2.1 removes "plain", and supporting it would mean
		// the weakest client sets the security level for everyone.
		CodeChallengeMethodsSupported: []string{"S256"},
		// Public clients authenticate with PKCE; confidential clients may use
		// a secret in the request body.
		TokenEndpointAuthMethodsSupported: []string{"none", "client_secret_post", "client_secret_basic"},
		ClientIDMetadataDocumentSupported: s.cfg.AllowCIMD,
	}
	if s.cfg.AllowDynamicRegistration {
		meta.RegistrationEndpoint = base + "/oauth/register"
	}
	writeJSON(w, http.StatusOK, meta)
}

// --- authorization ---------------------------------------------------------

// authorizeRequest is a validated /oauth/authorize query.
type authorizeRequest struct {
	ClientID            string
	RedirectURI         string
	State               string
	Scope               string
	CodeChallenge       string
	CodeChallengeMethod string
	client              *Client
}

// parseAuthorize validates an authorization request.
//
// The ordering matters for security. Client and redirect URI are validated
// first, and a failure in either is rendered on our own page rather than
// redirected -- redirecting an error to an unvalidated URI is what turns an
// authorization server into an open redirector. Only once the redirect target
// is known to be registered may later errors be sent back to it.
func (s *Server) parseAuthorize(ctx context.Context, q url.Values) (*authorizeRequest, error) {
	clientID := q.Get("client_id")
	if clientID == "" {
		return nil, errInvalidRequest("client_id is required")
	}
	client, err := s.resolveClient(ctx, clientID)
	if err != nil {
		return nil, errInvalidClient("unknown client")
	}

	redirectURI := q.Get("redirect_uri")
	if redirectURI == "" {
		return nil, errInvalidRequest("redirect_uri is required")
	}
	if !client.AllowsRedirect(redirectURI) {
		// Deliberately not redirected: the URI is not one we trust.
		return nil, errInvalidRequest("redirect_uri is not registered for this client")
	}

	req := &authorizeRequest{
		ClientID:    clientID,
		RedirectURI: redirectURI,
		State:       q.Get("state"),
		Scope:       q.Get("scope"),
		client:      client,
	}

	if rt := q.Get("response_type"); rt != "code" {
		return req, errInvalidRequest("response_type must be code")
	}

	// PKCE is mandatory for every client, public or confidential. OAuth 2.1
	// requires it, and making it conditional means the condition eventually
	// gets it wrong.
	req.CodeChallenge = q.Get("code_challenge")
	req.CodeChallengeMethod = q.Get("code_challenge_method")
	if req.CodeChallenge == "" {
		return req, errInvalidRequest("code_challenge is required")
	}
	if req.CodeChallengeMethod == "" {
		req.CodeChallengeMethod = "S256"
	}
	if req.CodeChallengeMethod != "S256" {
		return req, errInvalidRequest("code_challenge_method must be S256")
	}
	return req, nil
}

// resolveClient loads a registered client, falling back to a Client ID
// Metadata Document when the identifier is an HTTPS URL.
func (s *Server) resolveClient(ctx context.Context, clientID string) (*Client, error) {
	if c, err := s.store.ClientByID(ctx, clientID); err == nil {
		return c, nil
	}
	if !s.cfg.AllowCIMD || !strings.HasPrefix(clientID, "https://") {
		return nil, ErrClientNotFound
	}
	c, err := s.cimd.Fetch(ctx, clientID)
	if err != nil {
		s.log.Warn("client id metadata document could not be resolved",
			"client_id", clientID, "error", err)
		return nil, ErrClientNotFound
	}
	// Cache it so later requests do not depend on the client's site being up.
	if err := s.store.UpsertClient(ctx, c, ""); err != nil {
		s.log.Warn("failed to cache CIMD registration", "client_id", clientID, "error", err)
	}
	return c, nil
}

// handleAuthorize renders the login and consent page.
func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	req, err := s.parseAuthorize(r.Context(), r.URL.Query())
	if err != nil {
		s.renderAuthorizeError(w, r, req, err)
		return
	}

	// An existing browser session skips the password prompt but still shows
	// consent: authenticating is not the same act as granting an agent access.
	var user *User
	if c, cErr := r.Cookie(sessionCookie); cErr == nil {
		if u, uErr := s.store.SessionUser(r.Context(), HashSecret(c.Value)); uErr == nil {
			user = u
		}
	}
	s.renderConsent(w, req, user, "")
}

// handleAuthorizeSubmit processes the login and consent form.
func (s *Server) handleAuthorizeSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, errInvalidRequest("malformed form submission"))
		return
	}
	req, err := s.parseAuthorize(r.Context(), r.Form)
	if err != nil {
		s.renderAuthorizeError(w, r, req, err)
		return
	}

	user, err := s.authenticateForm(r)
	if err != nil {
		// One message for every failure mode, so the form cannot be used to
		// enumerate usernames.
		s.renderConsent(w, req, nil, "Incorrect username or password.")
		return
	}

	if r.FormValue("action") == "deny" {
		s.redirectError(w, r, req, ErrCodeAccessDenied, "the user denied the request")
		return
	}

	code, err := s.issueAuthCode(r.Context(), req, user)
	if err != nil {
		s.log.Error("failed to issue authorization code", "error", err)
		s.redirectError(w, r, req, ErrCodeServerError, "could not issue authorization code")
		return
	}

	// Refresh the browser session so consenting to a second client does not
	// require logging in again.
	s.startSession(w, r.Context(), user)

	redirect, _ := url.Parse(req.RedirectURI)
	q := redirect.Query()
	q.Set("code", code)
	if req.State != "" {
		q.Set("state", req.State)
	}
	redirect.RawQuery = q.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

// authenticateForm resolves the submitted credentials, or the existing session.
func (s *Server) authenticateForm(r *http.Request) (*User, error) {
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")

	if username == "" && password == "" {
		if c, err := r.Cookie(sessionCookie); err == nil {
			return s.store.SessionUser(r.Context(), HashSecret(c.Value))
		}
		return nil, ErrUserNotFound
	}

	user, err := s.store.UserByUsername(r.Context(), username)
	if err != nil {
		// Hash anyway so that a missing user and a wrong password take
		// comparable time and cannot be told apart by timing.
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return nil, ErrUserNotFound
	}
	if user.Disabled {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return nil, ErrUserNotFound
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return nil, ErrUserNotFound
	}
	_ = s.store.RecordLogin(r.Context(), user.ID)
	return user, nil
}

// issueAuthCode mints and stores a single-use authorization code.
func (s *Server) issueAuthCode(ctx context.Context, req *authorizeRequest, user *User) (string, error) {
	code, err := GenerateSecret()
	if err != nil {
		return "", err
	}
	granted := s.grantScope(req.Scope, user)
	now := s.now()
	if err := s.store.SaveAuthCode(ctx, &AuthCode{
		CodeHash:            HashSecret(code),
		ClientID:            req.ClientID,
		UserID:              user.ID,
		RedirectURI:         req.RedirectURI,
		Scope:               granted,
		CodeChallenge:       req.CodeChallenge,
		CodeChallengeMethod: req.CodeChallengeMethod,
		ExpiresAt:           now.Add(s.cfg.AuthCodeTTL),
	}); err != nil {
		return "", err
	}
	return code, nil
}

// grantScope intersects what a client asked for with what the user may
// delegate.
//
// A user can never grant more than they hold. An operator authorizing an agent
// cannot confer approval rights, and a user scoped to one plugin cannot hand
// over another -- which is what makes per-plugin scoping hold under OAuth as
// well as under static tokens.
func (s *Server) grantScope(requested string, user *User) string {
	userCaps := capabilitiesForRole(user.Role)
	wanted := ParseScopes(requested)
	if len(wanted) == 0 {
		// A client asking for nothing gets read on the user's plugins, which
		// is the least useful token that is still worth issuing.
		wanted = []string{ScopeRead}
		for _, p := range user.Plugins {
			wanted = append(wanted, PluginScope(p))
		}
	}

	var granted []string
	for _, want := range wanted {
		if name, isPlugin := strings.CutPrefix(want, PluginScopePrefix); isPlugin {
			if userGrantsPlugin(user, name) {
				granted = append(granted, want)
			}
			continue
		}
		if userCaps[want] {
			granted = append(granted, want)
		}
	}
	return JoinScopes(granted)
}

func userGrantsPlugin(u *User, name string) bool {
	for _, p := range u.Plugins {
		if p == "*" || p == name {
			return true
		}
	}
	return false
}

// capabilitiesForRole maps a role onto the scopes it may delegate.
func capabilitiesForRole(role string) map[string]bool {
	caps := map[string]bool{ScopeRead: true}
	switch role {
	case "operator":
		caps[ScopePropose] = true
	case "approver":
		caps[ScopePropose] = true
		caps[ScopeApprove] = true
	case "admin":
		caps[ScopePropose] = true
		caps[ScopeApprove] = true
	}
	return caps
}

// --- helpers ---------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeOAuthError(w http.ResponseWriter, err *Error) {
	writeJSON(w, err.Status(), err)
}

// redirectError returns a protocol error to the client's callback. It is only
// reached once the redirect URI has been validated against the registration.
func (s *Server) redirectError(w http.ResponseWriter, r *http.Request, req *authorizeRequest, code, desc string) {
	redirect, err := url.Parse(req.RedirectURI)
	if err != nil {
		writeOAuthError(w, errInvalidRequest("redirect_uri is malformed"))
		return
	}
	q := redirect.Query()
	q.Set("error", code)
	q.Set("error_description", desc)
	if req.State != "" {
		q.Set("state", req.State)
	}
	redirect.RawQuery = q.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

// renderAuthorizeError decides whether an error may be redirected.
//
// Errors discovered before the redirect URI is validated are rendered locally.
// Sending them onward would mean redirecting to a URI an attacker supplied,
// which is the open-redirect hole this split exists to close.
func (s *Server) renderAuthorizeError(w http.ResponseWriter, r *http.Request, req *authorizeRequest, err error) {
	oe, ok := err.(*Error)
	if !ok {
		oe = errServer("unexpected error")
	}
	if req == nil || req.client == nil || req.RedirectURI == "" ||
		!req.client.AllowsRedirect(req.RedirectURI) {
		s.renderErrorPage(w, oe)
		return
	}
	s.redirectError(w, r, req, oe.Code, oe.Description)
}

// GrantScopeForTest exposes scope intersection for host-level tests. It is not
// part of the server's runtime contract.
func (s *Server) GrantScopeForTest(requested string, user *User) string {
	return s.grantScope(requested, user)
}
