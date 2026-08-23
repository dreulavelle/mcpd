package admin

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/auth/users"
)

// Accounts is the subset of the account store the dashboard needs.
//
// It is an interface here rather than a concrete store because the dashboard's
// dependency is narrow -- sign in, resolve, sign out, and administer -- and
// naming it as such keeps the handler tests from needing a database.
type Accounts interface {
	Authenticate(ctx context.Context, email, password string) (*users.User, error)
	RecordLogin(ctx context.Context, userID string) error
	NewSession(ctx context.Context, userID string, ttl time.Duration) (string, *users.Session, error)
	ResolveSession(ctx context.Context, token string) (*users.User, *users.Session, error)
	DeleteSession(ctx context.Context, token string) error

	Count(ctx context.Context) (int, error)
	CreateFirst(ctx context.Context, email, password, displayName string) (*users.User, error)
	Create(ctx context.Context, req users.CreateRequest) (*users.User, error)
	List(ctx context.Context) ([]*users.User, error)
	ByID(ctx context.Context, id string) (*users.User, error)
	Update(ctx context.Context, id string, req users.UpdateRequest) (*users.User, error)
	SetPassword(ctx context.Context, id, password string) error
	Delete(ctx context.Context, id string) error
}

// sessionCookie is the browser's half of a sign-in.
//
// It is HttpOnly so that a script injected into the dashboard cannot read it,
// which is the whole reason the credential is not handed to the page. SameSite
// is Lax rather than Strict so that following a link to the dashboard from
// elsewhere does not land on a sign-in form for a session the browser already
// holds; the CSRF token, not the cookie policy, is what guards mutations.
const sessionCookie = "mcpd_session"

// csrfHeader carries the token the page echoes back.
const csrfHeader = "X-CSRF-Token"

// secureCookies reports whether the session cookie must carry Secure.
//
// Two facts, and the second is the one that matters. Serving TLS directly
// settles it -- the connection is encrypted and the browser will honour the
// flag. But the ordinary production shape for this host is an FQDN with a
// reverse proxy terminating TLS and forwarding plain HTTP, and in that shape
// r.TLS is nil while every browser in the world is speaking https. Deciding
// from r.TLS alone issues the session cookie without Secure to exactly the
// deployments that did the right thing, and the browser will then send that
// cookie over plain http to the same host -- so a single downgraded request
// hands over the session.
//
// The configured public URL is what settles the second case. It is what an
// operator wrote down about how this host is reached, which makes it the only
// statement about the scheme that a client cannot make.
//
// Not X-Forwarded-Proto. A forwarded header is set by whoever is talking to
// this process, and nothing here can tell a proxy's header from a caller's:
// the MCP host refuses to trust X-Forwarded-For for the same reason. A header
// that a client can set is not a fact about the deployment.
//
// It only ever widens. A configured http URL cannot turn Secure off on a
// connection that really is TLS, and a public URL that does not parse falls
// back to the connection rather than to a guess.
func (s *Server) secureCookies(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	// FrontendPublicURL, not PublicURL. PublicURL is the MCP endpoint, and the
	// two are different listeners: the MCP endpoint commonly serves TLS from a
	// self-signed certificate while the dashboard is plain HTTP on the LAN.
	// Reading the wrong one marks this cookie Secure on a plain-HTTP origin,
	// the browser drops it, and signing in appears to do nothing.
	u, err := url.Parse(s.opts.FrontendPublicURL)
	return err == nil && u.Scheme == "https"
}

// setSessionCookie issues the cookie for a new sign-in.
//
// Secure is conditional because the dashboard is also routinely reached over
// plain HTTP on a loopback or LAN address, and a Secure cookie on such an
// origin is silently dropped -- which presents as "signing in does nothing".
// Browsers already treat localhost as a secure context, so nothing is lost
// there.
func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   s.secureCookies(r),
		SameSite: http.SameSiteLaxMode,
	})
}

// clearSessionCookie removes it. The attributes have to match the ones it was
// set with, Secure included: a clear that omits Secure on an https deployment
// does not replace the cookie the browser is holding, so the stale one stays
// and signing out appears not to work.
func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.secureCookies(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func sessionToken(r *http.Request) string {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	return c.Value
}

// --- sign in and out -------------------------------------------------------

type signInRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// sessionResponse describes the signed-in person to the page.
//
// The CSRF token is returned in the body rather than a cookie: a page that can
// read it is same-origin, which is exactly the property being tested.
type sessionResponse struct {
	Email string `json:"email"`
	// Name is what to render and is never empty; DisplayName is what is
	// stored and may be. An Account page needs both -- one for the heading,
	// one for the input the person edits.
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name"`
	Role        string   `json:"role"`
	Plugins     []string `json:"plugins"`
	CSRFToken   string   `json:"csrf_token"`
	ExpiresAt   string   `json:"expires_at"`
}

// handleSignIn exchanges an email and password for a session cookie.
func (s *Server) handleSignIn(w http.ResponseWriter, r *http.Request) {
	if s.opts.Accounts == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "accounts are not configured")
		return
	}
	var req signInRequest
	if !s.decode(w, r, &req) {
		return
	}

	user, err := s.opts.Accounts.Authenticate(r.Context(), req.Email, req.Password)
	if err != nil {
		// One message for every failure. Saying which half was wrong turns
		// this form into a way to discover which addresses have accounts.
		s.opts.Log.Warn("dashboard sign-in failed", "remote", r.RemoteAddr)
		s.writeError(w, r, http.StatusUnauthorized, "that email and password did not match")
		return
	}

	token, sess, err := s.opts.Accounts.NewSession(r.Context(), user.ID, s.opts.SessionTTL)
	if err != nil {
		s.opts.Log.Error("could not start a dashboard session", "error", err)
		s.writeError(w, r, http.StatusInternalServerError, "could not start a session")
		return
	}
	if err := s.opts.Accounts.RecordLogin(r.Context(), user.ID); err != nil {
		// Losing the timestamp is not worth refusing the sign-in over.
		s.opts.Log.Warn("could not record a sign-in", "error", err)
	}

	s.opts.Log.Info("dashboard sign-in", "user", user.Email, "session", sess.ID)
	s.setSessionCookie(w, r, token, sess.ExpiresAt)
	s.writeJSON(w, r, http.StatusOK, sessionView(user, sess))
}

// handleRegisterFirst claims an unclaimed instance.
//
// Unauthenticated, and it has to be: there is no account to authenticate as
// yet. What bounds it is the store refusing once any account exists, checked
// inside the write transaction rather than here, so two browsers racing for an
// unclaimed instance produce one administrator and one refusal.
//
// Signing the new administrator straight in is deliberate. Making an account
// and then presenting its own sign-in form asks someone to prove, one second
// later, something they just demonstrated.
func (s *Server) handleRegisterFirst(w http.ResponseWriter, r *http.Request) {
	if s.opts.Accounts == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "accounts are not configured")
		return
	}
	var req struct {
		Email       string `json:"email"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
	}
	if !s.decode(w, r, &req) {
		return
	}

	user, err := s.opts.Accounts.CreateFirst(r.Context(), req.Email, req.Password, req.DisplayName)
	switch {
	case errors.Is(err, users.ErrAlreadyClaimed):
		s.writeError(w, r, http.StatusConflict,
			"this instance already has an account; sign in instead")
		return
	case err != nil:
		// Every remaining failure is a statement about the request -- a
		// malformed address, a short password -- and each is actionable.
		s.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	token, sess, err := s.opts.Accounts.NewSession(r.Context(), user.ID, s.opts.SessionTTL)
	if err != nil {
		s.opts.Log.Error("could not start a session for the first account", "error", err)
		s.writeError(w, r, http.StatusInternalServerError, "account created, but signing in failed")
		return
	}
	s.opts.Log.Info("first account registered; this instance is now claimed",
		"email", user.Email, "id", user.ID)
	s.setSessionCookie(w, r, token, sess.ExpiresAt)
	s.writeJSON(w, r, http.StatusCreated, sessionView(user, sess))
}

// handleSignOut ends the current session.
func (s *Server) handleSignOut(w http.ResponseWriter, r *http.Request) {
	if token := sessionToken(r); token != "" && s.opts.Accounts != nil {
		if err := s.opts.Accounts.DeleteSession(r.Context(), token); err != nil {
			s.opts.Log.Warn("could not end a dashboard session", "error", err)
		}
	}
	// The cookie is cleared whether or not the row was found, so a stale
	// cookie cannot keep presenting itself.
	s.clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

// handleCurrentSession reports who is signed in, and reissues the CSRF token.
//
// The page calls this on load so that a session surviving a refresh does not
// require signing in again to obtain a token.
func (s *Server) handleCurrentSession(w http.ResponseWriter, r *http.Request) {
	if s.opts.Accounts == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "accounts are not configured")
		return
	}
	user, sess, err := s.opts.Accounts.ResolveSession(r.Context(), sessionToken(r))
	if err != nil {
		s.writeError(w, r, http.StatusUnauthorized, "not signed in")
		return
	}
	s.writeJSON(w, r, http.StatusOK, sessionView(user, sess))
}

func sessionView(u *users.User, sess *users.Session) sessionResponse {
	return sessionResponse{
		Email:       u.Email,
		Name:        u.Name(),
		DisplayName: u.DisplayName,
		Role:        string(u.Role),
		Plugins:     u.Plugins,
		CSRFToken:   sess.CSRFToken,
		ExpiresAt:   sess.ExpiresAt.Format(time.RFC3339),
	}
}

// --- request authentication ------------------------------------------------

// principalFor resolves whichever credential the request carries.
//
// Two are accepted and they are for different callers. A browser presents the
// session cookie; a script presents a bearer token. The cookie is tried first
// because it is the common case, and because a page that somehow holds both
// should be treated as the person it signed in as.
func (s *Server) principalFor(w http.ResponseWriter, r *http.Request) (*auth.Principal, bool) {
	if token := sessionToken(r); token != "" && s.opts.Accounts != nil {
		user, sess, err := s.opts.Accounts.ResolveSession(r.Context(), token)
		if err == nil {
			// A cookie is sent by the browser on any request to this origin,
			// including one a different site caused. The header cannot be set
			// cross-origin without CORS consent, so requiring it is what makes
			// the difference between the page acting and a page acting through
			// it.
			if !safeMethod(r.Method) && !csrfValid(r, sess) {
				s.writeError(w, r, http.StatusForbidden, "missing or invalid CSRF token")
				return nil, false
			}
			return user.Principal(sess.ID), true
		}
		if !errors.Is(err, users.ErrNotFound) {
			s.opts.Log.Error("could not resolve a dashboard session", "error", err)
		}
		// A cookie that no longer resolves is cleared, so the browser stops
		// presenting it and the page can tell it needs to sign in again.
		s.clearSessionCookie(w, r)
	}

	token, ok := auth.BearerToken(r)
	if !ok {
		s.writeError(w, r, http.StatusUnauthorized, "authentication required")
		return nil, false
	}
	principal, err := s.opts.Verifier.Verify(r.Context(), token, r)
	if err != nil {
		s.opts.Log.Warn("dashboard authentication failed",
			"path", r.URL.Path, "token_fingerprint", auth.Fingerprint(token))
		s.writeError(w, r, http.StatusUnauthorized, "authentication required")
		return nil, false
	}
	return principal, true
}

// safeMethod reports whether a method is defined as not changing state, and so
// does not need a CSRF token.
func safeMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}

// csrfValid compares the echoed token against the session's, in constant time.
func csrfValid(r *http.Request, sess *users.Session) bool {
	presented := r.Header.Get(csrfHeader)
	if presented == "" || sess.CSRFToken == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(sess.CSRFToken)) == 1
}
