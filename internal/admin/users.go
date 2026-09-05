package admin

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/auth/groups"
	"github.com/spoked/mcpd/internal/auth/sso"
	"github.com/spoked/mcpd/internal/auth/users"
)

// providerConfigured reports whether a provider is set up and ready here.
//
// The same list the sign-in page's buttons come from, so an administrator
// cannot invite somebody through a provider the page will not offer them.
func (s *Server) providerConfigured(r *http.Request, p users.Provider) bool {
	if s.opts.SSO == nil {
		return false
	}
	for _, d := range s.opts.SSO.Available(r.Context()) {
		if d.Provider == string(p) {
			return true
		}
	}
	return false
}

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
	ID          string `json:"id"`
	Email       string `json:"email"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	// Role is the account's own role, by id; RoleName is for reading.
	Role     string `json:"role"`
	RoleName string `json:"role_name"`
	// Grants is the account's own reach, exactly as stored, so an edit form
	// can round-trip it. Reaches is what the account actually reaches once
	// its groups are unioned in, and Permissions what it may do; both are
	// what a page should render -- the same distinction DisplayName and Name
	// draw, for the same reason.
	Grants      []auth.Grant   `json:"grants"`
	Reaches     []auth.Grant   `json:"reaches"`
	Permissions []string       `json:"permissions"`
	Groups      []groupRefView `json:"groups"`
	Disabled    bool           `json:"disabled"`
	// Status is "active" or "pending", and is a different fact from Disabled.
	// An administrator switched a disabled account off; a pending one is a
	// registration nobody has decided about.
	Status string `json:"status"`
	// HasPassword is false for an account that only signs in through a
	// provider, which the Users page shows so that "why can I not reset this
	// password" has an answer on the page.
	HasPassword bool `json:"has_password"`
	// InviteProvider names the provider an invited account is waiting for a
	// first sign-in through, and is empty for every other account.
	// InviteLabel is the same fact in the words the button uses.
	InviteProvider string `json:"invite_provider,omitempty"`
	InviteLabel    string `json:"invite_label,omitempty"`
	// InviteExpiresAt is when the invitation stops being claimable. A row that
	// has one, and an administrator wondering why somebody cannot get in,
	// meet here.
	InviteExpiresAt string `json:"invite_expires_at,omitempty"`
	CreatedAt       string `json:"created_at"`
	LastLoginAt     string `json:"last_login_at,omitempty"`
	// Self marks the account making the request, so the page can warn before
	// someone edits themselves out of their own access.
	Self bool `json:"self"`
}

func viewOfUser(u *users.User, self bool, access groups.Resolved, memberOf []groupRefView) userView {
	if memberOf == nil {
		memberOf = []groupRefView{}
	}
	v := userView{
		ID:          u.ID,
		Email:       u.Email,
		Name:        u.Name(),
		DisplayName: u.DisplayName,
		Role:        u.RoleID,
		RoleName:    access.RoleName,
		Grants:      nonNilGrants(u.Grants),
		Reaches:     nonNilGrants(access.Grants),
		Permissions: permissionNames(u.Principal("", access).PermissionList()),
		Groups:      memberOf,
		Disabled:    u.Disabled,
		Status:      statusOf(u),
		HasPassword: u.HasPassword(),
		CreatedAt:   u.CreatedAt.Format(time.RFC3339),
		Self:        self,
	}
	if u.LastLoginAt != nil {
		v.LastLoginAt = u.LastLoginAt.Format(time.RFC3339)
	}
	if u.Invited() {
		v.InviteProvider = string(u.InviteProvider)
		v.InviteLabel = sso.Label(u.InviteProvider)
		if u.InviteExpiresAt != nil {
			v.InviteExpiresAt = u.InviteExpiresAt.Format(time.RFC3339)
		}
	}
	return v
}

// viewUser renders an account with what it actually reaches.
//
// The groups are read per account rather than in one join with the list. A
// dashboard page shows tens of accounts against a local SQLite file, and the
// join that would save the queries would also mean two ways to ask what an
// account belongs to.
func (s *Server) viewUser(r *http.Request, u *users.User, self bool) userView {
	var memberOf []groupRefView
	if s.opts.Groups != nil {
		list, err := s.opts.Groups.Of(r.Context(), groups.User(u.ID))
		if err != nil {
			s.opts.Log.Error("could not read an account's groups",
				"account", u.ID, "error", err)
		} else {
			memberOf = viewOfGroupRefs(list)
		}
	}
	return viewOfUser(u, self, s.accessFor(r, u), memberOf)
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
		out[i] = s.viewUser(r, u, u.ID == self)
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"users": out, "count": len(out)})
}

type createUserRequest struct {
	Email string `json:"email"`
	// Password is what this account signs in with, and is required unless
	// InviteProvider names a provider instead.
	Password    string       `json:"password"`
	DisplayName string       `json:"display_name"`
	Role        string       `json:"role"`
	Grants      []auth.Grant `json:"grants"`
	// InviteProvider invites the person to sign in with that provider rather
	// than with a password an administrator has to invent and then hand over
	// by some channel neither of them chose.
	InviteProvider string `json:"invite_provider"`
	// Groups the account joins as it is created. An empty list is the default
	// and reaches nothing, which is what an account with no direct grants and
	// no group has always been.
	Groups []string `json:"groups"`
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
	invite := users.Provider(strings.TrimSpace(req.InviteProvider))
	if invite != "" && !s.providerConfigured(r, invite) {
		// Refused here rather than at the store, because "is this provider set
		// up on this host" is a question about settings the store cannot see.
		// An invitation naming a provider nobody configured is an account
		// nobody can ever sign in to.
		s.writeError(w, r, http.StatusBadRequest,
			"that sign-in provider is not set up on this host")
		return
	}
	user, err := s.opts.Accounts.Create(r.Context(), users.CreateRequest{
		Email:          req.Email,
		Password:       req.Password,
		DisplayName:    req.DisplayName,
		RoleID:         req.Role,
		Grants:         req.Grants,
		Groups:         req.Groups,
		InviteProvider: invite,
		Actor:          auth.FromContext(r.Context()).ID,
	})
	switch {
	case errors.Is(err, users.ErrDuplicateEmail):
		s.writeError(w, r, http.StatusConflict, "an account with that email already exists")
		return
	case errors.Is(err, users.ErrNameCollides):
		s.writeError(w, r, http.StatusConflict,
			"another account already uses that address as its display name")
		return
	case errors.Is(err, users.ErrNoSuchRole):
		s.writeError(w, r, http.StatusBadRequest, "that role does not exist")
		return
	case err != nil:
		// Create's failures are all statements about the request -- a
		// malformed address, an unknown role, a short password, an empty
		// plugin grant -- and each is actionable, so the text is passed
		// through rather than flattened.
		s.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	s.opts.Log.InfoContext(r.Context(), "account created", "email", user.Email,
		"role", user.RoleID, "invited_with", string(user.InviteProvider),
		"by", auth.FromContext(r.Context()).ID)
	s.writeJSON(w, r, http.StatusCreated, s.viewUser(r, user, false))
}

type updateUserRequest struct {
	DisplayName *string       `json:"display_name,omitempty"`
	Role        *string       `json:"role,omitempty"`
	Grants      *[]auth.Grant `json:"grants,omitempty"`
	Disabled    *bool         `json:"disabled,omitempty"`
	Password    *string       `json:"password,omitempty"`
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

	update := users.UpdateRequest{
		DisplayName: req.DisplayName,
		RoleID:      req.Role,
		Disabled:    req.Disabled,
	}
	if req.Grants != nil {
		gs := auth.Grants(*req.Grants)
		update.Grants = &gs
	}
	user, err := s.opts.Accounts.Update(r.Context(), id, update)
	switch {
	case errors.Is(err, users.ErrNotFound):
		s.writeError(w, r, http.StatusNotFound, "no such account")
		return
	case errors.Is(err, users.ErrNoSuchRole):
		s.writeError(w, r, http.StatusBadRequest, "that role does not exist")
		return
	case errors.Is(err, users.ErrLastAdmin):
		s.writeError(w, r, http.StatusConflict,
			"this is the last account that can manage access; give someone else that first")
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
	s.writeJSON(w, r, http.StatusOK, s.viewUser(r, user, user.ID == s.currentAccountID(r)))
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
			"this is the last account that can manage access; give someone else that first")
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
	s.writeJSON(w, r, http.StatusOK, s.viewUser(r, user, true))
}
