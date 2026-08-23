package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/auth/sso"
	"github.com/spoked/mcpd/internal/auth/users"
)

// Registrations is the slice of the account store this file needs.
//
// Separate from Accounts rather than added to it, because the two answer
// different questions and a handler that only ever links an identity has no
// business being handed a way to delete an account.
type Registrations interface {
	Register(ctx context.Context, req users.RegisterRequest) (*users.User, error)
	UserByIdentity(ctx context.Context, provider users.Provider, subject string) (*users.User, error)
	IdentitiesFor(ctx context.Context, userID string) ([]users.Identity, error)
	LinkIdentity(ctx context.Context, actor string, i users.Identity) error
	UnlinkIdentity(ctx context.Context, actor, userID string, provider users.Provider) error
	PendingRegistrations(ctx context.Context) ([]*users.User, error)
	ApproveRegistration(ctx context.Context, actor, id string) (*users.User, error)
	RejectRegistration(ctx context.Context, actor, id string) error
}

// ssoCookie carries the secret a provider round trip is bound to.
//
// Short-lived, HttpOnly, and scoped to the routes that use it. SameSite is Lax
// because the callback arrives as a top-level navigation from the provider's
// own site, which Strict would drop -- and dropping it presents as every
// sign-in being refused for a state this host did issue.
const ssoCookie = "mcpd_sso"

// ssoCookiePath scopes the binding to the flow. It is not the session cookie
// and has no business travelling with every request to the dashboard.
const ssoCookiePath = "/api/auth"

func (s *Server) setSSOCookie(w http.ResponseWriter, r *http.Request, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     ssoCookie,
		Value:    value,
		Path:     ssoCookiePath,
		MaxAge:   int(sso.StateTTL / time.Second),
		HttpOnly: true,
		Secure:   s.secureCookies(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearSSOCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     ssoCookie,
		Value:    "",
		Path:     ssoCookiePath,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.secureCookies(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func ssoBinding(r *http.Request) string {
	c, err := r.Cookie(ssoCookie)
	if err != nil {
		return ""
	}
	return c.Value
}

// --- what the signed-out page is told ---------------------------------------

// authOptionsResponse is what the sign-in screen needs before anybody has
// signed in.
//
// Unauthenticated, so it says as little as it can while still letting the page
// draw the right thing: which providers have buttons, and whether there is a
// sign-up form. Neither is a secret -- both are discoverable by pressing the
// button -- and withholding them would mean a page that offers a form nothing
// accepts, or hides one that works.
type authOptionsResponse struct {
	Providers []sso.Descriptor `json:"providers"`
	// Registration reports whether somebody without an account may ask for
	// one with an email and a password.
	Registration bool `json:"registration"`
	// Approval reports that a new account waits for an administrator, so the
	// form can say so before somebody fills it in rather than afterwards.
	Approval bool `json:"approval"`
}

func (s *Server) handleAuthOptions(w http.ResponseWriter, r *http.Request) {
	resp := authOptionsResponse{Providers: []sso.Descriptor{}}
	if s.opts.SSO != nil {
		resp.Providers = s.opts.SSO.Available(r.Context())
	}
	policy := s.registrationPolicy(r.Context())
	resp.Registration = policy.Enabled
	resp.Approval = policy.RequireApproval
	s.writeJSON(w, r, http.StatusOK, resp)
}

func (s *Server) registrationPolicy(ctx context.Context) users.RegistrationPolicy {
	if s.opts.RegistrationPolicy == nil {
		// The zero value accepts nothing, which is the right answer for a host
		// that was not told what it accepts.
		return users.RegistrationPolicy{}
	}
	return s.opts.RegistrationPolicy(ctx)
}

// redirectURIResponse is what the Authentication page needs in order to be
// followed.
//
// Setting a provider up means pasting an exact redirect URI into somebody
// else's console, and getting it wrong produces a failure at the provider that
// says nothing useful. So the page shows the URI this host will actually send
// -- derived from the configured public URL, the same value the flow uses --
// rather than asking an operator to assemble one.
//
// When there is no public URL there is no URI, and the page says so instead of
// showing one built from the browser's address bar. That URL works when an
// operator tests it from the same machine and fails for everybody else, which
// is the worst way for this to be wrong.
type redirectURIResponse struct {
	// Base is the configured public URL, empty when there is none.
	Base string `json:"base"`
	// URIs maps a provider to the exact address to register, and is empty
	// when Base is.
	URIs map[string]string `json:"redirect_uris"`
}

func (s *Server) handleAuthRedirectURIs(w http.ResponseWriter, r *http.Request) {
	resp := redirectURIResponse{
		Base: s.opts.FrontendPublicURL,
		URIs: map[string]string{},
	}
	for _, p := range []users.Provider{
		users.ProviderGoogle, users.ProviderGitHub, users.ProviderEntra,
	} {
		if uri, err := sso.RedirectURI(resp.Base, p); err == nil {
			resp.URIs[string(p)] = uri
		}
	}
	s.writeJSON(w, r, http.StatusOK, resp)
}

// --- starting a flow --------------------------------------------------------

// handleSSOStart begins a sign-in at a provider.
//
// Unauthenticated by necessity: this is how somebody who is not signed in
// signs in. What it hands back is a URL and a cookie, and neither is worth
// anything without the other -- the state in the URL is refused at the
// callback unless the cookie comes back with it.
func (s *Server) handleSSOStart(w http.ResponseWriter, r *http.Request) {
	s.startSSO(w, r, sso.PurposeSignIn, "")
}

// handleIdentityLinkStart begins attaching a provider to the signed-in
// account.
//
// This is the only way an account that already exists gains a provider, and
// that is the whole of this feature's answer to account takeover. Proving
// control of alice@corp.com at Google says nothing about who owns the mcpd
// account with that address -- the account may predate the Google one, the
// address may have been recycled, the two may simply be different people at
// different companies. Proving control of the mcpd account first, and then
// completing a flow at Google, says exactly the thing that needs saying.
//
// It takes CapRead because it edits only the account the request authenticated
// as, the same reasoning that puts PATCH /api/account at read.
func (s *Server) handleIdentityLinkStart(w http.ResponseWriter, r *http.Request) {
	id := s.currentAccountID(r)
	if id == "" {
		s.writeError(w, r, http.StatusForbidden,
			"this credential is not an account, so there is nothing to link to")
		return
	}
	s.startSSO(w, r, sso.PurposeLink, id)
}

func (s *Server) startSSO(w http.ResponseWriter, r *http.Request, purpose sso.Purpose, userID string) {
	if s.opts.SSO == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "single sign-on is not configured")
		return
	}
	provider := users.Provider(r.PathValue("provider"))
	if !provider.Valid() {
		s.writeError(w, r, http.StatusNotFound, "no such provider")
		return
	}

	var body struct {
		ReturnTo string `json:"return_to"`
	}
	// A body is optional here: the sign-in page sends none. A body that will
	// not decode is not worth refusing over, because the only field in it is a
	// hint about where to land afterwards and the state store bounds that to a
	// path on this dashboard regardless.
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body)

	started, err := s.opts.SSO.Start(r.Context(), sso.StartRequest{
		Provider: provider,
		Purpose:  purpose,
		UserID:   userID,
		ReturnTo: body.ReturnTo,
	})
	switch {
	case errors.Is(err, sso.ErrNoRedirectBase):
		s.writeError(w, r, http.StatusConflict,
			"mcpd does not know its own address, so it cannot be redirected back to. "+
				"Set the dashboard's public URL first.")
		return
	case errors.Is(err, sso.ErrNotConfigured):
		s.writeError(w, r, http.StatusConflict, "that provider is not set up")
		return
	case err != nil:
		s.opts.Log.Error("could not start a provider sign-in",
			"provider", provider, "purpose", purpose, "error", err)
		s.writeError(w, r, http.StatusBadGateway,
			"could not reach that provider; try again in a moment")
		return
	}

	s.setSSOCookie(w, r, started.Binding)
	s.writeJSON(w, r, http.StatusOK, map[string]string{
		"authorization_url": started.AuthorizationURL,
	})
}

// --- the callback -----------------------------------------------------------

// ssoOutcome is the reason a flow ended, as the dashboard's own query
// parameter.
//
// Deliberately a short code rather than the error text. What comes back from a
// provider is a third party's prose, and it ends up in a URL bar, a browser
// history and whatever is in front of this host -- so the browser gets a code
// this dashboard has a sentence for, and the operator's log gets the detail.
type ssoOutcome string

const (
	outcomeState        ssoOutcome = "state"
	outcomeProvider     ssoOutcome = "provider"
	outcomeNoEmail      ssoOutcome = "no_email"
	outcomeAddressTaken ssoOutcome = "address_taken"
	outcomeClosed       ssoOutcome = "registration_closed"
	outcomeDomain       ssoOutcome = "domain"
	outcomeUnclaimed    ssoOutcome = "unclaimed"
	outcomeLinked       ssoOutcome = "already_linked"
	outcomeDisabled     ssoOutcome = "disabled"
	outcomeWrongAccount ssoOutcome = "wrong_account"
)

// handleSSOCallback completes a flow.
//
// A browser redirect rather than a JSON call, so every refusal ends as a
// redirect too. Returning a 400 here would leave somebody looking at a bare
// error page on this host's own domain with no way back to the sign-in form.
func (s *Server) handleSSOCallback(w http.ResponseWriter, r *http.Request) {
	provider := users.Provider(r.PathValue("provider"))
	if s.opts.SSO == nil || !provider.Valid() {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		// The provider refused, usually because somebody pressed cancel.
		s.opts.Log.Info("a provider refused a sign-in",
			"provider", provider, "error", e)
		s.finish(w, r, "/", outcomeProvider)
		return
	}

	state, identity, err := s.opts.SSO.Complete(
		r.Context(), provider, q.Get("code"), q.Get("state"), ssoBinding(r))
	if err != nil {
		s.opts.Log.Warn("a provider sign-in did not complete",
			"provider", provider, "error", err, "remote", r.RemoteAddr)
		switch {
		case errors.Is(err, sso.ErrState):
			s.finish(w, r, "/", outcomeState)
		case errors.Is(err, sso.ErrNoVerifiedEmail):
			s.finish(w, r, "/", outcomeNoEmail)
		default:
			s.finish(w, r, "/", outcomeProvider)
		}
		return
	}

	if state.Purpose == sso.PurposeLink {
		s.completeLink(w, r, state, identity)
		return
	}
	s.completeSignIn(w, r, state, identity)
}

// completeLink attaches the provider to the account that started the flow.
func (s *Server) completeLink(w http.ResponseWriter, r *http.Request, state *sso.State, identity *sso.Identity) {
	// The session is checked again here, against the account the flow was
	// started for. A state is single-use and bound to the browser, so this is
	// a second gate rather than the only one -- but the first gate is about
	// the browser and this one is about the account, and somebody who signed
	// out and back in as somebody else in another tab is exactly the case
	// where those differ.
	if s.currentAccountID(r) != state.UserID {
		s.finish(w, r, state.ReturnTo, outcomeWrongAccount)
		return
	}
	actor := "user:" + identity.Email
	if u, _, err := s.opts.Accounts.ResolveSession(r.Context(), sessionToken(r)); err == nil {
		actor = "user:" + u.Email
	}

	err := s.opts.Identities.LinkIdentity(r.Context(), actor, users.Identity{
		Provider: identity.Provider,
		Subject:  identity.Subject,
		UserID:   state.UserID,
		Email:    identity.Email,
	})
	switch {
	case errors.Is(err, users.ErrIdentityLinked):
		s.finish(w, r, state.ReturnTo, outcomeLinked)
	case err != nil:
		s.opts.Log.Error("could not link a provider identity",
			"provider", identity.Provider, "account", state.UserID, "error", err)
		s.finish(w, r, state.ReturnTo, outcomeProvider)
	default:
		s.opts.Log.Info("provider linked to an account",
			"provider", identity.Provider, "account", state.UserID)
		s.finish(w, r, state.ReturnTo, "")
	}
}

// completeSignIn turns a provider identity into a session.
//
// The order is the whole of the security property. A linked identity signs in.
// An unlinked one does not sign in as anybody, whatever address it carries: it
// is offered to Register, which refuses if the address already belongs to an
// account, if registration is closed, if the domain is not allowed, or if
// nobody has claimed this instance yet.
func (s *Server) completeSignIn(w http.ResponseWriter, r *http.Request, state *sso.State, identity *sso.Identity) {
	ctx := r.Context()

	user, err := s.opts.Identities.UserByIdentity(ctx, identity.Provider, identity.Subject)
	switch {
	case err == nil:
		if user.Disabled {
			s.opts.Log.Warn("a disabled account signed in through a provider",
				"provider", identity.Provider, "account", user.Email)
			s.finish(w, r, "/", outcomeDisabled)
			return
		}
	case errors.Is(err, users.ErrNotFound):
		user, err = s.opts.Identities.Register(ctx, users.RegisterRequest{
			Email:       identity.Email,
			DisplayName: identity.Name,
			Identity: &users.Identity{
				Provider: identity.Provider,
				Subject:  identity.Subject,
				Email:    identity.Email,
			},
			Policy: s.registrationPolicy(ctx),
		})
		if err != nil {
			s.opts.Log.Warn("a provider sign-in was refused an account",
				"provider", identity.Provider, "error", err)
			s.finish(w, r, "/", registrationOutcome(err))
			return
		}
		s.opts.Log.Info("account registered through a provider",
			"provider", identity.Provider, "email", user.Email, "status", user.Status)
	default:
		s.opts.Log.Error("could not resolve a provider identity",
			"provider", identity.Provider, "error", err)
		s.finish(w, r, "/", outcomeProvider)
		return
	}

	token, sess, err := s.opts.Accounts.NewSession(ctx, user.ID, s.opts.SessionTTL)
	if err != nil {
		s.opts.Log.Error("could not start a session after a provider sign-in", "error", err)
		s.finish(w, r, "/", outcomeProvider)
		return
	}
	if err := s.opts.Accounts.RecordLogin(ctx, user.ID); err != nil {
		s.opts.Log.Warn("could not record a sign-in", "error", err)
	}
	s.opts.Log.Info("dashboard sign-in through a provider",
		"provider", identity.Provider, "user", user.Email, "session", sess.ID)

	s.setSessionCookie(w, r, token, sess.ExpiresAt)
	s.finish(w, r, state.ReturnTo, "")
}

// registrationOutcome maps a refusal to the code the page has words for.
func registrationOutcome(err error) ssoOutcome {
	switch {
	case errors.Is(err, users.ErrAddressTaken):
		return outcomeAddressTaken
	case errors.Is(err, users.ErrRegistrationClosed):
		return outcomeClosed
	case errors.Is(err, users.ErrDomainNotAllowed):
		return outcomeDomain
	case errors.Is(err, users.ErrUnclaimed):
		return outcomeUnclaimed
	case errors.Is(err, users.ErrIdentityLinked):
		return outcomeLinked
	}
	return outcomeProvider
}

// finish sends the browser back to the dashboard, saying how it went.
//
// A refusal lands on the page the flow was started from rather than on the
// root, because that is where somebody can read it and try again: a link that
// failed belongs on the profile page beside the button that started it, and a
// sign-in that failed belongs on the sign-in form. Dropping everybody on the
// root would leave half of them looking at a message about a page they are not
// on.
//
// The path is not taken from the request. It comes out of the state row, where
// it was bounded to a path on this dashboard before it was stored.
//
// The binding cookie is cleared here rather than from a defer in the handler,
// and that is not tidiness. http.Redirect writes the header, and a Set-Cookie
// added after the header is written is never sent -- so a deferred clear would
// leave the browser holding a binding for a state that has already been
// consumed. Every path out of the callback comes through here.
func (s *Server) finish(w http.ResponseWriter, r *http.Request, returnTo string, outcome ssoOutcome) {
	s.clearSSOCookie(w, r)

	target := &url.URL{Path: "/"}
	if strings.HasPrefix(returnTo, "/") && !strings.HasPrefix(returnTo, "//") {
		if parsed, err := url.Parse(returnTo); err == nil {
			target = &url.URL{Path: parsed.Path}
		}
	}
	if outcome != "" {
		target.RawQuery = url.Values{"sso_error": {string(outcome)}}.Encode()
	}
	http.Redirect(w, r, target.String(), http.StatusSeeOther)
}

// --- registering with a password --------------------------------------------

// handleRegister creates an account for somebody who asked for one.
//
// It runs the same policy the provider path does, through the same function.
// One door that checks whether registration is open and another that does not
// is how a host ends up refusing sign-ups on a form while accepting them
// through Google, and there would be nothing on either page to say so.
//
// Unauthenticated, and separate from POST /api/setup. That endpoint claims an
// unclaimed instance and makes an administrator; this one asks for an ordinary
// account on a host somebody already owns, and Register refuses outright if
// nobody does.
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if s.opts.Identities == nil {
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

	user, err := s.opts.Identities.Register(r.Context(), users.RegisterRequest{
		Email:       req.Email,
		Password:    req.Password,
		DisplayName: req.DisplayName,
		Policy:      s.registrationPolicy(r.Context()),
	})
	switch {
	case errors.Is(err, users.ErrRegistrationClosed), errors.Is(err, users.ErrUnclaimed):
		// One answer for both. A host that is not accepting registrations and
		// a host nobody has claimed are different facts, and neither is one an
		// anonymous caller has any business being told apart.
		s.writeError(w, r, http.StatusForbidden, "this host is not accepting new accounts")
		return
	case errors.Is(err, users.ErrDomainNotAllowed):
		s.writeError(w, r, http.StatusForbidden,
			"accounts here are limited to particular email domains, and that address is not one of them")
		return
	case errors.Is(err, users.ErrAddressTaken), errors.Is(err, users.ErrNameCollides):
		s.writeError(w, r, http.StatusConflict, "that email address cannot be used here")
		return
	case err != nil:
		// What is left is a statement about the request -- a malformed
		// address, a short password -- and each is actionable.
		s.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	s.opts.Log.Info("account registered", "email", user.Email, "status", user.Status)

	// Signed straight in, pending or not. A pending account holds no
	// capability, so the session it gets is worth exactly one screen saying it
	// is waiting -- which is a better answer than a form that succeeded and
	// then asked them to sign in to find out nothing works.
	token, sess, err := s.opts.Accounts.NewSession(r.Context(), user.ID, s.opts.SessionTTL)
	if err != nil {
		s.opts.Log.Error("could not start a session for a new account", "error", err)
		s.writeError(w, r, http.StatusInternalServerError,
			"account created, but signing in failed")
		return
	}
	s.setSessionCookie(w, r, token, sess.ExpiresAt)
	s.writeJSON(w, r, http.StatusCreated, sessionView(user, sess))
}

// --- the pending queue ------------------------------------------------------

func (s *Server) handleListRegistrations(w http.ResponseWriter, r *http.Request) {
	if s.opts.Identities == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "accounts are not configured")
		return
	}
	list, err := s.opts.Identities.PendingRegistrations(r.Context())
	if err != nil {
		s.opts.Log.Error("could not read pending registrations", "error", err)
		s.writeError(w, r, http.StatusInternalServerError, "could not read pending registrations")
		return
	}
	out := make([]userView, len(list))
	for i, u := range list {
		out[i] = viewOfUser(u, false)
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"registrations": out, "count": len(out)})
}

func (s *Server) handleApproveRegistration(w http.ResponseWriter, r *http.Request) {
	if s.opts.Identities == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "accounts are not configured")
		return
	}
	actor := auth.FromContext(r.Context()).ID
	user, err := s.opts.Identities.ApproveRegistration(r.Context(), actor, r.PathValue("id"))
	switch {
	case errors.Is(err, users.ErrNotFound):
		s.writeError(w, r, http.StatusNotFound, "no such account")
		return
	case errors.Is(err, users.ErrNotPending):
		s.writeError(w, r, http.StatusConflict, "that account is not waiting for approval")
		return
	case err != nil:
		s.opts.Log.Error("could not approve a registration", "error", err)
		s.writeError(w, r, http.StatusInternalServerError, "could not approve that account")
		return
	}
	s.opts.Log.Info("registration approved", "email", user.Email, "by", actor)
	s.writeJSON(w, r, http.StatusOK, viewOfUser(user, false))
}

func (s *Server) handleRejectRegistration(w http.ResponseWriter, r *http.Request) {
	if s.opts.Identities == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "accounts are not configured")
		return
	}
	actor := auth.FromContext(r.Context()).ID
	err := s.opts.Identities.RejectRegistration(r.Context(), actor, r.PathValue("id"))
	switch {
	case errors.Is(err, users.ErrNotFound):
		s.writeError(w, r, http.StatusNotFound, "no such account")
		return
	case errors.Is(err, users.ErrNotPending):
		s.writeError(w, r, http.StatusConflict, "that account is not waiting for approval")
		return
	case err != nil:
		s.opts.Log.Error("could not reject a registration", "error", err)
		s.writeError(w, r, http.StatusInternalServerError, "could not reject that account")
		return
	}
	s.opts.Log.Info("registration rejected", "account", r.PathValue("id"), "by", actor)
	w.WriteHeader(http.StatusNoContent)
}

// --- an account's own providers ---------------------------------------------

type identityView struct {
	Provider string `json:"provider"`
	Label    string `json:"label"`
	Email    string `json:"email"`
	LinkedAt string `json:"linked_at"`
}

// handleAccountIdentities lists the providers the signed-in account can use.
func (s *Server) handleAccountIdentities(w http.ResponseWriter, r *http.Request) {
	if s.opts.Identities == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "accounts are not configured")
		return
	}
	id := s.currentAccountID(r)
	if id == "" {
		s.writeError(w, r, http.StatusForbidden, "this credential is not an account")
		return
	}
	list, err := s.opts.Identities.IdentitiesFor(r.Context(), id)
	if err != nil {
		s.opts.Log.Error("could not read linked providers", "error", err)
		s.writeError(w, r, http.StatusInternalServerError, "could not read linked providers")
		return
	}
	out := make([]identityView, len(list))
	for i, l := range list {
		out[i] = identityView{
			Provider: string(l.Provider),
			Label:    sso.Label(l.Provider),
			Email:    l.Email,
			LinkedAt: l.LinkedAt.Format(time.RFC3339),
		}
	}
	available := []sso.Descriptor{}
	if s.opts.SSO != nil {
		available = s.opts.SSO.Available(r.Context())
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"identities": out,
		"available":  available,
	})
}

// handleUnlinkIdentity detaches a provider from the signed-in account.
func (s *Server) handleUnlinkIdentity(w http.ResponseWriter, r *http.Request) {
	if s.opts.Identities == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "accounts are not configured")
		return
	}
	id := s.currentAccountID(r)
	if id == "" {
		s.writeError(w, r, http.StatusForbidden, "this credential is not an account")
		return
	}
	provider := users.Provider(r.PathValue("provider"))
	if !provider.Valid() {
		s.writeError(w, r, http.StatusNotFound, "no such provider")
		return
	}
	actor := auth.FromContext(r.Context()).ID
	err := s.opts.Identities.UnlinkIdentity(r.Context(), actor, id, provider)
	switch {
	case errors.Is(err, users.ErrNotFound):
		s.writeError(w, r, http.StatusNotFound, "that provider is not linked to this account")
		return
	case errors.Is(err, users.ErrLastCredential):
		s.writeError(w, r, http.StatusConflict,
			"that is the only way this account can sign in; set a password first")
		return
	case err != nil:
		s.opts.Log.Error("could not unlink a provider", "error", err)
		s.writeError(w, r, http.StatusInternalServerError, "could not unlink that provider")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
