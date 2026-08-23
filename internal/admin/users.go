package admin

import (
	"errors"
	"net/http"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/auth/users"
)

// userView is what the dashboard sees. The password hash is never in it: the
// page has no use for it and every serialisation is a chance to leak it.
//
// Name and DisplayName are both here and they are not the same field.
// DisplayName is what is stored, which may be empty, and is what an edit form
// has to round-trip -- rendering the fallback into the input would make saving
// the form persist the address as a name. Name is what to render, and is never
// empty.
//
// A page must render Name. DisplayName is raw: on a database old enough it
// can be a value written before there were rules about what may go in it, so
// it belongs in an input where its owner can see it and replace it, and
// nowhere that treats it as text to display.
type userView struct {
	ID          string   `json:"id"`
	Email       string   `json:"email"`
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name"`
	Role        string   `json:"role"`
	Plugins     []string `json:"plugins"`
	Disabled    bool     `json:"disabled"`
	// Status is "active" or "pending", and is a different fact from Disabled.
	// An administrator switched a disabled account off; a pending one is a
	// registration nobody has decided about.
	Status string `json:"status"`
	// HasPassword is false for an account that only signs in through a
	// provider, which the Users page shows so that "why can I not reset this
	// password" has an answer on the page.
	HasPassword bool   `json:"has_password"`
	CreatedAt   string `json:"created_at"`
	LastLoginAt string `json:"last_login_at,omitempty"`
	// Self marks the account making the request, so the page can warn before
	// someone edits themselves out of their own access.
	Self bool `json:"self"`
}

func viewOfUser(u *users.User, self bool) userView {
	v := userView{
		ID:          u.ID,
		Email:       u.Email,
		Name:        u.Name(),
		DisplayName: u.DisplayName,
		Role:        string(u.Role),
		Plugins:     u.Plugins,
		Disabled:    u.Disabled,
		Status:      statusOf(u),
		HasPassword: u.HasPassword(),
		CreatedAt:   u.CreatedAt.Format(time.RFC3339),
		Self:        self,
	}
	if u.LastLoginAt != nil {
		v.LastLoginAt = u.LastLoginAt.Format(time.RFC3339)
	}
	return v
}

// currentAccountID returns the signed-in account's identifier.
//
// A principal's ID is "user:" + email for a session and something else for a
// static token, so this is deliberately a lookup by email rather than a string
// trim: a bearer token has no account and must not match one.
func (s *Server) currentAccountID(r *http.Request) string {
	if s.opts.Accounts == nil {
		return ""
	}
	user, _, err := s.opts.Accounts.ResolveSession(r.Context(), sessionToken(r))
	if err != nil {
		return ""
	}
	return user.ID
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	if s.opts.Accounts == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "accounts are not configured")
		return
	}
	list, err := s.opts.Accounts.List(r.Context())
	if err != nil {
		s.opts.Log.Error("could not list accounts", "error", err)
		s.writeError(w, r, http.StatusInternalServerError, "could not read accounts")
		return
	}
	self := s.currentAccountID(r)
	out := make([]userView, len(list))
	for i, u := range list {
		out[i] = viewOfUser(u, u.ID == self)
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"users": out, "count": len(out)})
}

type createUserRequest struct {
	Email       string   `json:"email"`
	Password    string   `json:"password"`
	DisplayName string   `json:"display_name"`
	Role        string   `json:"role"`
	Plugins     []string `json:"plugins"`
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if s.opts.Accounts == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "accounts are not configured")
		return
	}
	var req createUserRequest
	if !s.decode(w, r, &req) {
		return
	}
	user, err := s.opts.Accounts.Create(r.Context(), users.CreateRequest{
		Email:       req.Email,
		Password:    req.Password,
		DisplayName: req.DisplayName,
		Role:        auth.Role(req.Role),
		Plugins:     req.Plugins,
	})
	switch {
	case errors.Is(err, users.ErrDuplicateEmail):
		s.writeError(w, r, http.StatusConflict, "an account with that email already exists")
		return
	case errors.Is(err, users.ErrNameCollides):
		s.writeError(w, r, http.StatusConflict,
			"another account already uses that address as its display name")
		return
	case err != nil:
		// Create's failures are all statements about the request -- a
		// malformed address, an unknown role, a short password, an empty
		// plugin grant -- and each is actionable, so the text is passed
		// through rather than flattened.
		s.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	s.opts.Log.Info("account created", "email", user.Email,
		"role", user.Role, "by", auth.FromContext(r.Context()).ID)
	s.writeJSON(w, r, http.StatusCreated, viewOfUser(user, false))
}

type updateUserRequest struct {
	DisplayName *string   `json:"display_name,omitempty"`
	Role        *string   `json:"role,omitempty"`
	Plugins     *[]string `json:"plugins,omitempty"`
	Disabled    *bool     `json:"disabled,omitempty"`
	Password    *string   `json:"password,omitempty"`
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	if s.opts.Accounts == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "accounts are not configured")
		return
	}
	id := r.PathValue("id")
	var req updateUserRequest
	if !s.decode(w, r, &req) {
		return
	}

	// A password change is its own operation in the store, because it ends
	// every live session and the field edits do not.
	if req.Password != nil {
		if err := s.opts.Accounts.SetPassword(r.Context(), id, *req.Password); err != nil {
			if errors.Is(err, users.ErrNotFound) {
				s.writeError(w, r, http.StatusNotFound, "no such account")
				return
			}
			s.writeError(w, r, http.StatusBadRequest, err.Error())
			return
		}
		s.opts.Log.Info("account password changed",
			"account", id, "by", auth.FromContext(r.Context()).ID)
	}

	var role *auth.Role
	if req.Role != nil {
		r := auth.Role(*req.Role)
		role = &r
	}
	user, err := s.opts.Accounts.Update(r.Context(), id, users.UpdateRequest{
		DisplayName: req.DisplayName,
		Role:        role,
		Plugins:     req.Plugins,
		Disabled:    req.Disabled,
	})
	switch {
	case errors.Is(err, users.ErrNotFound):
		s.writeError(w, r, http.StatusNotFound, "no such account")
		return
	case errors.Is(err, users.ErrLastAdmin):
		s.writeError(w, r, http.StatusConflict,
			"this is the last administrator; promote someone else first")
		return
	case errors.Is(err, users.ErrNameCollides):
		s.writeError(w, r, http.StatusConflict,
			"that display name is another account's address")
		return
	case err != nil:
		s.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	s.opts.Log.Info("account updated", "email", user.Email,
		"by", auth.FromContext(r.Context()).ID)
	s.writeJSON(w, r, http.StatusOK, viewOfUser(user, user.ID == s.currentAccountID(r)))
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	if s.opts.Accounts == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "accounts are not configured")
		return
	}
	id := r.PathValue("id")

	// Deleting the account you are signed in as would end the session that is
	// doing it, which is confusing rather than dangerous -- but the confusion
	// is avoidable and the intent is almost never real.
	if id == s.currentAccountID(r) {
		s.writeError(w, r, http.StatusConflict,
			"you cannot delete the account you are signed in as")
		return
	}

	err := s.opts.Accounts.Delete(r.Context(), id)
	switch {
	case errors.Is(err, users.ErrNotFound):
		s.writeError(w, r, http.StatusNotFound, "no such account")
		return
	case errors.Is(err, users.ErrLastAdmin):
		s.writeError(w, r, http.StatusConflict,
			"this is the last administrator; promote someone else first")
		return
	case err != nil:
		s.opts.Log.Error("could not delete an account", "account", id, "error", err)
		s.writeError(w, r, http.StatusInternalServerError, "could not delete the account")
		return
	}
	s.opts.Log.Info("account deleted", "account", id,
		"by", auth.FromContext(r.Context()).ID)
	w.WriteHeader(http.StatusNoContent)
}

// accountRequest is what a person may change about their own account.
//
// One field, and that is the point. Everything else on an account -- its role,
// its plugin grants, whether it is switched off -- is a grant somebody else
// made, and an endpoint that let the holder edit those would be an endpoint
// that hands out administration.
type accountRequest struct {
	DisplayName *string `json:"display_name"`
}

// handleUpdateAccount lets a person set their own display name.
//
// It is not administration, so it does not require CapAdmin. Naming yourself
// is the one thing about an account its holder is the authority on, and the
// alternative -- the state before this existed -- was a dashboard telling
// every non-administrator to go and ask an administrator to type their name
// for them.
//
// The account edited is the one the request authenticated as, and there is no
// identifier in the request to say otherwise. That is what keeps this endpoint
// from becoming a way around PATCH /api/users/{id}: it cannot address another
// account, so there is no check to get wrong. Changing somebody else's name
// still goes through the administrator's route.
func (s *Server) handleUpdateAccount(w http.ResponseWriter, r *http.Request) {
	if s.opts.Accounts == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "accounts are not configured")
		return
	}
	id := s.currentAccountID(r)
	if id == "" {
		// A bearer token authenticates a script, which is a principal without
		// an account. There is no row for it to name.
		s.writeError(w, r, http.StatusForbidden,
			"this credential is not an account, so there is nothing to name")
		return
	}
	var req accountRequest
	if !s.decode(w, r, &req) {
		return
	}
	if req.DisplayName == nil {
		s.writeError(w, r, http.StatusBadRequest, "display_name is required")
		return
	}

	user, err := s.opts.Accounts.Update(r.Context(), id, users.UpdateRequest{
		DisplayName: req.DisplayName,
	})
	switch {
	case errors.Is(err, users.ErrNotFound):
		s.writeError(w, r, http.StatusNotFound, "no such account")
		return
	case errors.Is(err, users.ErrNameCollides):
		s.writeError(w, r, http.StatusConflict,
			"that display name is another account's address")
		return
	case err != nil:
		s.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	s.opts.Log.Info("account renamed itself", "account", id)
	s.writeJSON(w, r, http.StatusOK, viewOfUser(user, true))
}
