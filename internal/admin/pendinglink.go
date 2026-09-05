package admin

import (
	"errors"
	"net/http"

	"github.com/spoked/mcpd/internal/auth/sso"
	"github.com/spoked/mcpd/internal/auth/users"
)

// The offer to connect a provider to an account that already holds the
// address, and the one place it is taken up.
//
// Unauthenticated by necessity, exactly as signing in is: the person is not
// signed in, and the whole point of these routes is that they are about to be.
// What bounds them is the offer -- a row this host wrote at the callback, held
// against a cookie set on that browser, single use, expiring, and retired
// after three wrong passwords.
//
// They sit under /api/auth so the cookie's path covers them and nothing else.

// pendingLinkResponse is what the screen needs in order to draw itself.
//
// The address is in it because the screen has to name the account being
// connected, and whoever is holding this cookie has just signed in at the
// provider with that address. Nothing else about the account is here: what it
// may reach, what role it holds and whether it is waiting for approval are
// facts for after the password.
type pendingLinkResponse struct {
	Provider string `json:"provider"`
	Label    string `json:"label"`
	Email    string `json:"email"`
}

// handlePendingLink says whether this browser is holding an offer.
//
// The screen confirms with this before rendering rather than trusting the code
// in the address bar. A parameter somebody typed is not an offer, and drawing
// a password field on the strength of one would ask for a password against
// nothing.
func (s *Server) handlePendingLink(w http.ResponseWriter, r *http.Request) {
	if s.opts.Identities == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "accounts are not configured")
		return
	}
	link, err := s.opts.Identities.PendingLinkFor(r.Context(), linkToken(r), ssoBinding(r))
	if err != nil {
		// One answer for no offer, an expired one, and one belonging to a
		// different browser. They are one sentence and one thing to do.
		s.writeError(w, r, http.StatusNotFound, "there is nothing waiting to be connected")
		return
	}
	s.writeJSON(w, r, http.StatusOK, pendingLinkResponse{
		Provider: string(link.Provider),
		Label:    sso.Label(link.Provider),
		Email:    link.Email,
	})
}

// handleCompletePendingLink connects the provider once the account's own
// password has been given.
//
// The password is the proof the provider cannot give. Everything about which
// account, which provider and which subject comes out of the row rather than
// out of the request, so there is nothing here for a caller to aim somewhere
// else.
func (s *Server) handleCompletePendingLink(w http.ResponseWriter, r *http.Request) {
	if s.opts.Identities == nil || s.opts.Accounts == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "accounts are not configured")
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	token, binding := linkToken(r), ssoBinding(r)

	user, err := s.opts.Identities.ClaimPendingLink(ctx, token, binding, req.Password)
	switch {
	case errors.Is(err, users.ErrInvalidCredentials):
		// One sentence for every wrong password, and the count that retires
		// the offer is kept in the row rather than reported here. Saying how
		// many attempts are left would only be useful to somebody who is not
		// the account's owner.
		s.opts.Log.WarnContext(ctx, "a wrong password was given for an offered provider link",
			"remote", r.RemoteAddr)
		s.writeError(w, r, http.StatusUnauthorized, "that password did not match")
		return
	case errors.Is(err, users.ErrIdentityLinked):
		s.clearLinkCookie(w, r)
		s.writeError(w, r, http.StatusConflict,
			"that provider account is already connected to an account here")
		return
	case errors.Is(err, users.ErrNotFound):
		s.clearLinkCookie(w, r)
		s.writeError(w, r, http.StatusNotFound, "there is nothing waiting to be connected")
		return
	case err != nil:
		s.opts.Log.ErrorContext(ctx, "could not connect an offered provider link", "error", err)
		s.writeError(w, r, http.StatusInternalServerError, "could not connect that provider")
		return
	}

	// Whatever session this browser was already holding goes first. Somebody
	// who signed in as another account in another tab must not end up with two
	// live sessions and a cookie naming whichever one was written last. Best
	// effort: there is usually no session here at all.
	if existing := sessionToken(r); existing != "" {
		if err := s.opts.Accounts.DeleteSession(ctx, existing); err != nil {
			s.opts.Log.WarnContext(ctx, "could not end an existing session", "error", err)
		}
	}

	token2, sess, err := s.opts.Accounts.NewSession(ctx, user.ID, s.sessionTTL(ctx))
	if err != nil {
		s.opts.Log.ErrorContext(ctx, "could not start a session after connecting a provider",
			"error", err)
		s.writeError(w, r, http.StatusInternalServerError,
			"that provider is connected, but signing in failed")
		return
	}
	if err := s.opts.Accounts.RecordLogin(ctx, user.ID); err != nil {
		s.opts.Log.WarnContext(ctx, "could not record a sign-in", "error", err)
	}
	s.opts.Log.InfoContext(ctx, "provider connected at sign-in and signed in",
		"user", user.Email, "session", sess.ID)

	s.clearLinkCookie(w, r)
	s.setSessionCookie(w, r, token2, sess.ExpiresAt)
	s.writeJSON(w, r, http.StatusOK, s.sessionView(r, user, sess))
}

// handleDiscardPendingLink is "Not now".
//
// It retires the row rather than merely sending the person back to the form.
// An offer left live is one the next person at that browser is holding, and
// the screen it draws asks for a password.
func (s *Server) handleDiscardPendingLink(w http.ResponseWriter, r *http.Request) {
	if s.opts.Identities == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "accounts are not configured")
		return
	}
	if err := s.opts.Identities.DiscardPendingLink(
		r.Context(), linkToken(r), ssoBinding(r)); err != nil {
		s.opts.Log.ErrorContext(r.Context(), "could not discard an offered provider link",
			"error", err)
		s.writeError(w, r, http.StatusInternalServerError, "could not put that away")
		return
	}
	s.clearLinkCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}
