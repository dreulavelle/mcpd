package oauth

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
)

// dummyHash is compared against when a user does not exist, so that a missing
// account and a wrong password take comparable time. Without it, response
// latency enumerates valid usernames.
var dummyHash = []byte("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy")

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

// handleToken serves the token endpoint for both supported grants.
func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, errInvalidRequest("malformed request body"))
		return
	}
	switch r.FormValue("grant_type") {
	case "authorization_code":
		s.grantAuthorizationCode(w, r)
	case "refresh_token":
		s.grantRefreshToken(w, r)
	case "":
		writeOAuthError(w, errInvalidRequest("grant_type is required"))
	default:
		writeOAuthError(w, oauthErr(ErrCodeUnsupportedGrantType,
			"only authorization_code and refresh_token are supported",
			http.StatusBadRequest))
	}
}

// grantAuthorizationCode exchanges a code for tokens.
func (s *Server) grantAuthorizationCode(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	client, err := s.authenticateClient(ctx, r)
	if err != nil {
		writeOAuthError(w, errInvalidClient("client authentication failed"))
		return
	}

	rawCode := r.FormValue("code")
	verifier := r.FormValue("code_verifier")
	if rawCode == "" || verifier == "" {
		writeOAuthError(w, errInvalidRequest("code and code_verifier are required"))
		return
	}

	// Consuming first is what makes a code single-use under concurrency: the
	// guarded UPDATE leaves no window between checking and marking, so two
	// simultaneous exchanges produce one winner.
	code, err := s.store.ConsumeAuthCode(ctx, HashSecret(rawCode))
	if err != nil {
		writeOAuthError(w, errInvalidGrant())
		return
	}

	// Every remaining check compares against the code as stored, so a client
	// cannot substitute a different callback or redeem another client's code.
	if code.ClientID != client.ID {
		s.log.Warn("authorization code presented by the wrong client",
			"code_client", code.ClientID, "presenting_client", client.ID)
		writeOAuthError(w, errInvalidGrant())
		return
	}
	if code.RedirectURI != r.FormValue("redirect_uri") {
		writeOAuthError(w, errInvalidGrant())
		return
	}
	if err := VerifyPKCE(verifier, code.CodeChallenge, code.CodeChallengeMethod); err != nil {
		writeOAuthError(w, errInvalidGrant())
		return
	}

	user, err := s.store.UserByID(ctx, code.UserID)
	if err != nil || user.Disabled {
		writeOAuthError(w, errInvalidGrant())
		return
	}

	resp, err := s.issueTokenPair(ctx, client.ID, user.ID, code.Scope, "")
	if err != nil {
		s.log.Error("failed to issue tokens", "error", err)
		writeOAuthError(w, errServer("could not issue tokens"))
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// grantRefreshToken rotates a refresh token.
func (s *Server) grantRefreshToken(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	client, err := s.authenticateClient(ctx, r)
	if err != nil {
		writeOAuthError(w, errInvalidClient("client authentication failed"))
		return
	}

	raw := r.FormValue("refresh_token")
	if raw == "" {
		writeOAuthError(w, errInvalidRequest("refresh_token is required"))
		return
	}
	oldHash := HashSecret(raw)

	existing, err := s.store.TokenByHash(ctx, oldHash)
	if err != nil || existing.Kind != KindRefresh || existing.ClientID != client.ID {
		writeOAuthError(w, errInvalidGrant())
		return
	}

	// A refresh token narrowed by the client stays narrowed; it can never be
	// widened, or a compromised token could escalate its own scope.
	scope := existing.Scope
	if requested := r.FormValue("scope"); requested != "" {
		scope = intersectScopes(existing.Scope, requested)
	}

	resp, err := s.issueTokenPair(ctx, client.ID, existing.UserID, scope, oldHash)
	switch {
	case errors.Is(err, ErrTokenReuse):
		// The legitimate client and an attacker cannot both hold the current
		// token, and there is no way to tell which one is calling. The lineage
		// is already revoked by the store; all that remains is to say no.
		s.log.Warn("refresh token reuse detected; lineage revoked",
			"client", client.ID, "user", existing.UserID, "lineage", existing.LineageID)
		writeOAuthError(w, errInvalidGrant())
		return
	case err != nil:
		writeOAuthError(w, errInvalidGrant())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// issueTokenPair mints an access and refresh token. When rotating, oldRefresh
// names the token being replaced so the store can detect reuse.
func (s *Server) issueTokenPair(ctx context.Context, clientID, userID, scope, oldRefresh string) (*tokenResponse, error) {
	now := s.now()

	accessRaw, err := GenerateSecret()
	if err != nil {
		return nil, err
	}
	refreshRaw, err := GenerateSecret()
	if err != nil {
		return nil, err
	}

	lineage := ""
	if oldRefresh == "" {
		if lineage, err = NewID("lin_"); err != nil {
			return nil, err
		}
	}

	refresh := &Token{
		Hash:      HashSecret(refreshRaw),
		Kind:      KindRefresh,
		ClientID:  clientID,
		UserID:    userID,
		Scope:     scope,
		ExpiresAt: now.Add(s.cfg.RefreshTokenTTL),
		LineageID: lineage,
	}

	if oldRefresh != "" {
		// RotateRefreshToken sets LineageID from the token being replaced.
		if err := s.store.RotateRefreshToken(ctx, oldRefresh, refresh); err != nil {
			return nil, err
		}
	} else if err := s.store.SaveToken(ctx, refresh); err != nil {
		return nil, err
	}

	access := &Token{
		Hash:      HashSecret(accessRaw),
		Kind:      KindAccess,
		ClientID:  clientID,
		UserID:    userID,
		Scope:     scope,
		ExpiresAt: now.Add(s.cfg.AccessTokenTTL),
		LineageID: refresh.LineageID,
	}
	if err := s.store.SaveToken(ctx, access); err != nil {
		return nil, err
	}

	return &tokenResponse{
		AccessToken:  accessRaw,
		TokenType:    "Bearer",
		ExpiresIn:    int(s.cfg.AccessTokenTTL.Seconds()),
		RefreshToken: refreshRaw,
		Scope:        scope,
	}, nil
}

// intersectScopes returns the scopes present in both sets. It is how a
// narrowing request is honoured without ever widening a token.
func intersectScopes(have, want string) string {
	haveSet := make(map[string]bool)
	for _, s := range ParseScopes(have) {
		haveSet[s] = true
	}
	var out []string
	for _, s := range ParseScopes(want) {
		if haveSet[s] {
			out = append(out, s)
		}
	}
	return JoinScopes(out)
}

// authenticateClient identifies the caller at the token endpoint.
//
// Public clients present only a client_id and are authenticated by PKCE.
// Confidential clients present a secret, compared by digest.
func (s *Server) authenticateClient(ctx context.Context, r *http.Request) (*Client, error) {
	clientID, secret := clientCredentials(r)
	if clientID == "" {
		return nil, ErrClientNotFound
	}
	client, err := s.resolveClient(ctx, clientID)
	if err != nil {
		return nil, err
	}
	if client.IsPublic() {
		// A public client offering a secret is a misconfiguration, not an
		// upgrade: accept the registration as it stands and rely on PKCE.
		return client, nil
	}
	if secret == "" || HashSecret(secret) != client.SecretHash {
		return nil, ErrClientNotFound
	}
	return client, nil
}

// clientCredentials extracts client identity from either supported location.
func clientCredentials(r *http.Request) (id, secret string) {
	if u, p, ok := r.BasicAuth(); ok {
		// RFC 6749 requires form-encoding the components of Basic credentials.
		if du, err := url.QueryUnescape(u); err == nil {
			u = du
		}
		if dp, err := url.QueryUnescape(p); err == nil {
			p = dp
		}
		return u, p
	}
	return r.FormValue("client_id"), r.FormValue("client_secret")
}

// handleRevoke implements RFC 7009.
//
// The response is 200 whatever happens, including for an unknown token. The
// specification requires it, and it is also correct: an error would let a
// caller probe which tokens exist.
func (s *Server) handleRevoke(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	raw := r.FormValue("token")
	if raw == "" {
		w.WriteHeader(http.StatusOK)
		return
	}
	hash := HashSecret(raw)

	if token, err := s.store.TokenByHash(r.Context(), hash); err == nil {
		if client, cErr := s.authenticateClient(r.Context(), r); cErr != nil || client.ID != token.ClientID {
			// Revoking someone else's token is refused silently.
			w.WriteHeader(http.StatusOK)
			return
		}
		// Revoking a refresh token ends the whole session, which is what a
		// user signing out expects.
		if token.Kind == KindRefresh {
			_ = s.store.RevokeLineage(r.Context(), token.LineageID)
		} else {
			_ = s.store.RevokeToken(r.Context(), hash)
		}
	}
	w.WriteHeader(http.StatusOK)
}

// --- browser sessions ------------------------------------------------------

const sessionCookie = "mcpd_session"

// startSession issues a login cookie so consenting to a second client does not
// require re-entering a password.
func (s *Server) startSession(w http.ResponseWriter, ctx context.Context, user *User) {
	raw, err := GenerateSecret()
	if err != nil {
		return
	}
	expires := s.now().Add(s.cfg.SessionTTL)
	if err := s.store.SaveSession(ctx, HashSecret(raw), user.ID, expires); err != nil {
		s.log.Warn("failed to persist login session", "error", err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:  sessionCookie,
		Value: raw,
		Path:  "/oauth",
		// The consent flow only ever runs over the public HTTPS URL, so these
		// are unconditional rather than conditional on the scheme.
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  expires,
	})
}

// basicAuthValue is retained for symmetry with clientCredentials in tests.
func basicAuthValue(id, secret string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(id+":"+secret))
}
