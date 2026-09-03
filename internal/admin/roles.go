package admin

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/auth/roles"
)

// Roles is the slice of the role store the dashboard needs.
type Roles interface {
	List(ctx context.Context) ([]*auth.Role, error)
	ByID(ctx context.Context, id string) (*auth.Role, error)
	Create(ctx context.Context, actor string, req roles.CreateRequest) (*auth.Role, error)
	Update(ctx context.Context, actor, id string, req roles.UpdateRequest) (*auth.Role, error)
	Delete(ctx context.Context, actor, id string) error
}

// roleView is what the dashboard sees.
type roleView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Builtin marks a role this build defines, which the page shows but
	// does not let anybody edit or delete.
	Builtin bool `json:"builtin"`
	// Permissions is the level held in each area, areas at nothing absent.
	Permissions map[string]string `json:"permissions"`
	// Assigned counts the users, keys, accounts and groups holding it.
	Assigned  int    `json:"assigned"`
	CreatedBy string `json:"created_by"`
	CreatedAt string `json:"created_at"`
}

func viewOfRole(r *auth.Role) roleView {
	perms := map[string]string{}
	for area, level := range r.Permissions.Normalize() {
		perms[string(area)] = string(level)
	}
	return roleView{
		ID:          r.ID,
		Name:        r.Name,
		Description: r.Description,
		Builtin:     r.Builtin,
		Permissions: perms,
		Assigned:    r.Assigned,
		CreatedBy:   r.CreatedBy,
		CreatedAt:   r.CreatedAt.Format(time.RFC3339),
	}
}

// areaView describes one row of the permission matrix: the levels an area
// can be held at, in order. Served with the roles so the editor draws the
// matrix from the server's vocabulary rather than a copy of it.
type areaView struct {
	Area   string   `json:"area"`
	Levels []string `json:"levels"`
}

func areaViews() []areaView {
	out := make([]areaView, 0, len(auth.Areas))
	for _, a := range auth.Areas {
		levels := make([]string, 0, 2)
		for _, l := range a.Levels() {
			levels = append(levels, string(l))
		}
		out = append(out, areaView{Area: string(a), Levels: levels})
	}
	return out
}

func (s *Server) handleListRoles(w http.ResponseWriter, r *http.Request) {
	if s.opts.Roles == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "roles are not configured")
		return
	}
	list, err := s.opts.Roles.List(r.Context())
	if err != nil {
		s.opts.Log.Error("could not list roles", "error", err)
		s.writeError(w, r, http.StatusInternalServerError, "could not read the roles")
		return
	}
	out := make([]roleView, len(list))
	for i, role := range list {
		out[i] = viewOfRole(role)
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"roles": out, "count": len(out), "areas": areaViews(),
	})
}

type roleRequest struct {
	Name        *string            `json:"name,omitempty"`
	Description *string            `json:"description,omitempty"`
	Permissions *map[string]string `json:"permissions,omitempty"`
}

func parsePermissions(raw map[string]string) auth.Permissions {
	out := auth.Permissions{}
	for area, level := range raw {
		out[auth.Area(area)] = auth.Level(level)
	}
	return out
}

func (s *Server) handleCreateRole(w http.ResponseWriter, r *http.Request) {
	if s.opts.Roles == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "roles are not configured")
		return
	}
	var req roleRequest
	if !s.decode(w, r, &req) {
		return
	}
	if req.Name == nil {
		s.writeError(w, r, http.StatusBadRequest, "a role needs a name")
		return
	}
	create := roles.CreateRequest{Name: *req.Name}
	if req.Description != nil {
		create.Description = *req.Description
	}
	if req.Permissions != nil {
		create.Permissions = parsePermissions(*req.Permissions)
	}
	actor := auth.FromContext(r.Context()).ID
	role, err := s.opts.Roles.Create(r.Context(), actor, create)
	switch {
	case errors.Is(err, roles.ErrDuplicateName):
		s.writeError(w, r, http.StatusConflict, "a role with that name already exists")
		return
	case err != nil:
		// Every remaining refusal is a statement about the request -- an
		// unusable name, a level an area cannot be held at -- and each is
		// actionable, so the text is passed through rather than flattened.
		s.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	s.opts.Log.Info("role created", "role", role.ID, "name", role.Name, "by", actor)
	s.writeJSON(w, r, http.StatusCreated, viewOfRole(role))
}

func (s *Server) handleUpdateRole(w http.ResponseWriter, r *http.Request) {
	if s.opts.Roles == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "roles are not configured")
		return
	}
	var req roleRequest
	if !s.decode(w, r, &req) {
		return
	}
	update := roles.UpdateRequest{Name: req.Name, Description: req.Description}
	if req.Permissions != nil {
		perms := parsePermissions(*req.Permissions)
		update.Permissions = &perms
	}
	actor := auth.FromContext(r.Context()).ID
	role, err := s.opts.Roles.Update(r.Context(), actor, r.PathValue("id"), update)
	switch {
	case errors.Is(err, roles.ErrNotFound):
		s.writeError(w, r, http.StatusNotFound, "no such role")
		return
	case errors.Is(err, roles.ErrBuiltin):
		s.writeError(w, r, http.StatusConflict,
			"a built-in role cannot be changed; make a copy and change that")
		return
	case errors.Is(err, roles.ErrDuplicateName):
		s.writeError(w, r, http.StatusConflict, "a role with that name already exists")
		return
	case errors.Is(err, roles.ErrLastAdmin):
		s.writeError(w, r, http.StatusConflict,
			"that would leave nobody able to manage access to this host; "+
				"give someone else that first")
		return
	case err != nil:
		s.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	s.opts.Log.Info("role changed", "role", role.ID, "by", actor)
	s.writeJSON(w, r, http.StatusOK, viewOfRole(role))
}

func (s *Server) handleDeleteRole(w http.ResponseWriter, r *http.Request) {
	if s.opts.Roles == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "roles are not configured")
		return
	}
	actor := auth.FromContext(r.Context()).ID
	err := s.opts.Roles.Delete(r.Context(), actor, r.PathValue("id"))
	switch {
	case errors.Is(err, roles.ErrNotFound):
		s.writeError(w, r, http.StatusNotFound, "no such role")
		return
	case errors.Is(err, roles.ErrBuiltin):
		s.writeError(w, r, http.StatusConflict, "a built-in role cannot be deleted")
		return
	case errors.Is(err, roles.ErrAssigned):
		s.writeError(w, r, http.StatusConflict,
			"that role is still held by a user, key, account or group; move them first")
		return
	case err != nil:
		s.opts.Log.Error("could not delete a role", "error", err)
		s.writeError(w, r, http.StatusInternalServerError, "could not delete the role")
		return
	}
	s.opts.Log.Info("role deleted", "role", r.PathValue("id"), "by", actor)
	w.WriteHeader(http.StatusNoContent)
}
