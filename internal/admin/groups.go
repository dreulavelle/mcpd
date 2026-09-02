package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/auth/groups"
)

// Groups is the slice of the group store the dashboard needs.
//
// An interface for the same reason Accounts is one: the dependency is narrow,
// and naming it as such keeps the handler tests from needing a database.
type Groups interface {
	List(ctx context.Context) ([]*groups.Group, error)
	ByID(ctx context.Context, id string) (*groups.Group, error)
	Create(ctx context.Context, actor string, req groups.CreateRequest) (*groups.Group, error)
	Update(ctx context.Context, actor, id string, req groups.UpdateRequest) (*groups.Group, error)
	Delete(ctx context.Context, actor, id string) error
	Members(ctx context.Context, groupID string) ([]groups.Member, error)
	Of(ctx context.Context, subject groups.Subject) ([]*groups.Group, error)
	AddMember(ctx context.Context, actor, groupID string, subject groups.Subject) error
	RemoveMember(ctx context.Context, actor, groupID string, subject groups.Subject) error
}

// groupView is what the dashboard sees.
type groupView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Plugins is the grant, exactly as stored. Empty is legitimate and means
	// the group hands out nothing.
	Plugins []string `json:"plugins"`
	// Capabilities is the ceiling this group imposes on what its members may
	// do. null means it imposes none and each member's role stands; an empty
	// array is a group that permits nothing, which suspends its members
	// without deleting them. The dashboard has to keep those apart, so this is
	// a nullable array rather than one that defaults to empty.
	Capabilities []string `json:"capabilities"`
	Members      int      `json:"members"`
	CreatedBy    string   `json:"created_by"`
	CreatedAt    string   `json:"created_at"`
}

func viewOfGroup(g *groups.Group) groupView {
	return groupView{
		ID:          g.ID,
		Name:        g.Name,
		Description: g.Description,
		Plugins:     nonNil(g.Plugins),
		// nil stays nil on the wire: a group imposing no ceiling and a group
		// permitting nothing are different, and nonNil would collapse them.
		Capabilities: capabilityNames(g.Capabilities),
		Members:      g.Members,
		CreatedBy:    g.CreatedBy,
		CreatedAt:    g.CreatedAt.Format(time.RFC3339),
	}
}

// groupRefView is a group as it appears beside a member, where the grant is
// worth showing and the rest is not.
type groupRefView struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Plugins []string `json:"plugins"`
}

func viewOfGroupRefs(list []*groups.Group) []groupRefView {
	out := make([]groupRefView, 0, len(list))
	for _, g := range list {
		out = append(out, groupRefView{ID: g.ID, Name: g.Name, Plugins: nonNil(g.Plugins)})
	}
	return out
}

type memberView struct {
	// Kind is "user" or "key".
	Kind string `json:"kind"`
	ID   string `json:"id"`
	// Label is an account's address or a key's name, for reading.
	Label   string `json:"label"`
	AddedBy string `json:"added_by"`
	AddedAt string `json:"added_at"`
}

func (s *Server) handleListGroups(w http.ResponseWriter, r *http.Request) {
	if s.opts.Groups == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "groups are not configured")
		return
	}
	list, err := s.opts.Groups.List(r.Context())
	if err != nil {
		s.opts.Log.Error("could not list groups", "error", err)
		s.writeError(w, r, http.StatusInternalServerError, "could not read groups")
		return
	}
	out := make([]groupView, len(list))
	for i, g := range list {
		out[i] = viewOfGroup(g)
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"groups": out, "count": len(out)})
}

func (s *Server) handleGetGroup(w http.ResponseWriter, r *http.Request) {
	if s.opts.Groups == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "groups are not configured")
		return
	}
	g, err := s.opts.Groups.ByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, groups.ErrNotFound) {
		s.writeError(w, r, http.StatusNotFound, "no such group")
		return
	}
	if err != nil {
		s.opts.Log.Error("could not read a group", "error", err)
		s.writeError(w, r, http.StatusInternalServerError, "could not read the group")
		return
	}
	members, err := s.opts.Groups.Members(r.Context(), g.ID)
	if err != nil {
		s.opts.Log.Error("could not list group members", "error", err)
		s.writeError(w, r, http.StatusInternalServerError, "could not read the members")
		return
	}
	rows := make([]memberView, len(members))
	for i, m := range members {
		rows[i] = memberView{
			Kind: string(m.Kind), ID: m.ID, Label: m.Label,
			AddedBy: m.AddedBy, AddedAt: m.AddedAt.Format(time.RFC3339),
		}
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"group": viewOfGroup(g), "members": rows,
	})
}

type groupRequest struct {
	Name        *string   `json:"name,omitempty"`
	Description *string   `json:"description,omitempty"`
	Plugins     *[]string `json:"plugins,omitempty"`
	// Capabilities sets the ceiling. Absent leaves it alone; null removes it;
	// an array sets it, including an empty one. Three distinct requests, which
	// is why this is a pointer to a pointer's worth of meaning rather than a
	// plain slice.
	Capabilities *[]string `json:"capabilities,omitempty"`
	// capabilitiesSet records whether the caller mentioned the field at all,
	// which a nil pointer alone cannot distinguish from an explicit null.
	capabilitiesSet bool `json:"-"`
}

// UnmarshalJSON records whether "capabilities" was present, so that omitting it
// and sending null mean different things -- leave the ceiling alone, and remove
// it. Without this a group could never have its ceiling cleared.
func (g *groupRequest) UnmarshalJSON(b []byte) error {
	type alias groupRequest
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(b, &probe); err != nil {
		return err
	}
	*g = groupRequest(a)
	_, g.capabilitiesSet = probe["capabilities"]
	return nil
}

// parseCapabilities turns the wire form into capabilities, refusing a name that
// is not one rather than dropping it.
//
// Dropping would be the wrong direction here, unlike when reading a stored
// ceiling: this is somebody typing, and a typo that silently widens a group is
// exactly the failure this whole mechanism exists to prevent.
func parseCapabilities(raw []string) ([]auth.Capability, error) {
	out := make([]auth.Capability, 0, len(raw))
	for _, name := range raw {
		c := auth.Capability(strings.TrimSpace(name))
		if !c.Valid() {
			return nil, fmt.Errorf("%q is not a capability; use read, propose, approve or admin", name)
		}
		out = append(out, c)
	}
	return out, nil
}

func (s *Server) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	if s.opts.Groups == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "groups are not configured")
		return
	}
	var req groupRequest
	if !s.decode(w, r, &req) {
		return
	}
	if req.Name == nil {
		s.writeError(w, r, http.StatusBadRequest, "a group needs a name")
		return
	}
	create := groups.CreateRequest{Name: *req.Name}
	if req.Description != nil {
		create.Description = *req.Description
	}
	if req.Plugins != nil {
		create.Plugins = *req.Plugins
	}
	if req.Capabilities != nil {
		caps, err := parseCapabilities(*req.Capabilities)
		if err != nil {
			s.writeError(w, r, http.StatusBadRequest, err.Error())
			return
		}
		create.Capabilities = caps
	}
	actor := auth.FromContext(r.Context()).ID
	g, err := s.opts.Groups.Create(r.Context(), actor, create)
	switch {
	case errors.Is(err, groups.ErrDuplicateName):
		s.writeError(w, r, http.StatusConflict, "a group with that name already exists")
		return
	case err != nil:
		// Every remaining refusal is a statement about the request, and each
		// is actionable, so the text is passed through rather than flattened.
		s.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	s.opts.Log.Info("group created", "group", g.ID, "name", g.Name, "by", actor)
	s.writeJSON(w, r, http.StatusCreated, viewOfGroup(g))
}

func (s *Server) handleUpdateGroup(w http.ResponseWriter, r *http.Request) {
	if s.opts.Groups == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "groups are not configured")
		return
	}
	var req groupRequest
	if !s.decode(w, r, &req) {
		return
	}
	update := groups.UpdateRequest{
		Name:        req.Name,
		Description: req.Description,
		Plugins:     req.Plugins,
	}
	// Three requests, kept apart: the field absent leaves the ceiling alone,
	// null removes it, and an array sets it -- including an empty array, which
	// is a group that permits nothing rather than one that restricts nothing.
	if req.capabilitiesSet {
		if req.Capabilities == nil {
			var none []auth.Capability
			update.Capabilities = &none
		} else {
			caps, err := parseCapabilities(*req.Capabilities)
			if err != nil {
				s.writeError(w, r, http.StatusBadRequest, err.Error())
				return
			}
			update.Capabilities = &caps
		}
	}
	actor := auth.FromContext(r.Context()).ID
	g, err := s.opts.Groups.Update(r.Context(), actor, r.PathValue("id"), update)
	switch {
	case errors.Is(err, groups.ErrNotFound):
		s.writeError(w, r, http.StatusNotFound, "no such group")
		return
	case errors.Is(err, groups.ErrDuplicateName):
		s.writeError(w, r, http.StatusConflict, "a group with that name already exists")
		return
	case errors.Is(err, groups.ErrLastAdmin):
		s.writeError(w, r, http.StatusConflict,
			"that would leave nobody able to administer this host; "+
				"give another administrator a group that permits admin first")
		return
	case err != nil:
		s.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	s.opts.Log.Info("group changed", "group", g.ID, "by", actor)
	s.writeJSON(w, r, http.StatusOK, viewOfGroup(g))
}

// handleDeleteGroup removes a group.
//
// Deleting takes the group's grant away from every member and gives nobody
// anything, so it is allowed while members remain -- narrowing is the safe
// direction, and a group that has to be emptied first is one an operator
// empties in a hurry with no record of what it held. The page confirms with
// the member count; the trail records it.
func (s *Server) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	if s.opts.Groups == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "groups are not configured")
		return
	}
	actor := auth.FromContext(r.Context()).ID
	err := s.opts.Groups.Delete(r.Context(), actor, r.PathValue("id"))
	switch {
	case errors.Is(err, groups.ErrNotFound):
		s.writeError(w, r, http.StatusNotFound, "no such group")
		return
	case err != nil:
		s.opts.Log.Error("could not delete a group", "error", err)
		s.writeError(w, r, http.StatusInternalServerError, "could not delete the group")
		return
	}
	s.opts.Log.Info("group deleted", "group", r.PathValue("id"), "by", actor)
	w.WriteHeader(http.StatusNoContent)
}

type memberRequest struct {
	// Kind is "user" or "key".
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

func (s *Server) handleAddGroupMember(w http.ResponseWriter, r *http.Request) {
	if s.opts.Groups == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "groups are not configured")
		return
	}
	var req memberRequest
	if !s.decode(w, r, &req) {
		return
	}
	subject := groups.Subject{Kind: groups.Kind(req.Kind), ID: req.ID}
	if !subject.Kind.Valid() {
		s.writeError(w, r, http.StatusBadRequest, `kind must be "user" or "key"`)
		return
	}
	actor := auth.FromContext(r.Context()).ID
	err := s.opts.Groups.AddMember(r.Context(), actor, r.PathValue("id"), subject)
	switch {
	case errors.Is(err, groups.ErrNotFound):
		s.writeError(w, r, http.StatusNotFound, "no such group")
		return
	case errors.Is(err, groups.ErrNoSuchMember):
		s.writeError(w, r, http.StatusNotFound, "no such account or key")
		return
	case errors.Is(err, groups.ErrLastAdmin):
		s.writeError(w, r, http.StatusConflict,
			"this group's restriction would take admin away from the last administrator")
		return
	case err != nil:
		s.opts.Log.Error("could not add a group member", "error", err)
		s.writeError(w, r, http.StatusInternalServerError, "could not add them to the group")
		return
	}
	s.opts.Log.Info("group member added",
		"group", r.PathValue("id"), "kind", req.Kind, "member", req.ID, "by", actor)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRemoveGroupMember(w http.ResponseWriter, r *http.Request) {
	if s.opts.Groups == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "groups are not configured")
		return
	}
	subject := groups.Subject{
		Kind: groups.Kind(r.PathValue("kind")),
		ID:   r.PathValue("member"),
	}
	if !subject.Kind.Valid() {
		s.writeError(w, r, http.StatusBadRequest, `kind must be "user" or "key"`)
		return
	}
	actor := auth.FromContext(r.Context()).ID
	err := s.opts.Groups.RemoveMember(r.Context(), actor, r.PathValue("id"), subject)
	switch {
	case errors.Is(err, groups.ErrNotFound):
		s.writeError(w, r, http.StatusNotFound, "no such group")
		return
	case errors.Is(err, groups.ErrNoSuchMember):
		s.writeError(w, r, http.StatusNotFound, "they are not in this group")
		return
	case err != nil:
		s.opts.Log.Error("could not remove a group member", "error", err)
		s.writeError(w, r, http.StatusInternalServerError, "could not take them out of the group")
		return
	}
	s.opts.Log.Info("group member removed",
		"group", r.PathValue("id"), "kind", subject.Kind, "member", subject.ID, "by", actor)
	w.WriteHeader(http.StatusNoContent)
}

// nonNil keeps an empty grant rendering as [] rather than null, so a page can
// treat "reaches nothing" as a list it can count.
func nonNil(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

// capabilityNames renders a ceiling for the wire, keeping nil as null.
func capabilityNames(caps []auth.Capability) []string {
	if caps == nil {
		return nil
	}
	out := make([]string, 0, len(caps))
	for _, c := range caps {
		out = append(out, string(c))
	}
	return out
}
