package bookstack

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/plugins"
)

// The tools for "who can see this, and why can they not".
//
// These reads are gated harder than the rest of the package. A user listing is
// every colleague's email address, and a role's permission set is the map of
// what the knowledge base protects -- both are reads where seeing the answer
// is itself the privilege, which is what ToolSpec.Capability exists for.
// Nothing here changes anything; the gate is about disclosure, not risk.

func (p *Plugin) registerPeopleTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_users",
		Title: "List users",
		Description: "The people with accounts on the knowledge base, with when " +
			"each was last active. Requires the token's user to manage users.",
		Idempotent: true,
		Capability: auth.CapAdmin,
	}, p.listUsers)

	plugins.Tool(r, plugins.ToolSpec{
		Name:        "get_user",
		Title:       "Get one user",
		Description: "One person's account, with the roles they hold.",
		Idempotent:  true,
		Capability:  auth.CapAdmin,
	}, p.getUser)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_roles",
		Title: "List roles",
		Description: "The roles in the system, with how many people hold each " +
			"and how many permissions it grants.",
		Idempotent: true,
		Capability: auth.CapAdmin,
	}, p.listRoles)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_role",
		Title: "Get one role",
		Description: "One role in full: every permission it grants and the " +
			"people who hold it.",
		Idempotent: true,
		Capability: auth.CapAdmin,
	}, p.getRole)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_content_permissions",
		Title: "Get an item's permissions",
		Description: "Who can see and change one shelf, book, chapter or page, " +
			"and whether it inherits from what contains it. This is the answer " +
			"to \"why can this person not open that page\".",
		Idempotent: true,
		Capability: auth.CapAdmin,
	}, p.getContentPermissions)
}

type userRow struct {
	ID             int    `json:"id"`
	Name           string `json:"name"`
	Email          string `json:"email"`
	Slug           string `json:"slug"`
	ExternalAuthID string `json:"external_auth_id"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
	LastActivityAt string `json:"last_activity_at"`
	ProfileURL     string `json:"profile_url"`
}

// UsersResult is the user list.
type UsersResult struct {
	Users []userRow `json:"users"`
	Count int       `json:"count"`
	truncation
}

func (p *Plugin) listUsers(ctx context.Context, args listArgs) (UsersResult, error) {
	if err := p.ready(); err != nil {
		return UsersResult{}, err
	}
	pg, err := p.client.list(ctx, "/api/users", filterByName(args.Query), args.Limit)
	p.noted(err)
	if err != nil {
		return UsersResult{}, explainPeopleFailure(err, "users")
	}
	rows, err := decodeRows[userRow](pg)
	if err != nil {
		return UsersResult{}, err
	}
	rows, cut := bound(rows, pg)
	return UsersResult{Users: rows, Count: len(rows), truncation: cut}, nil
}

// UserDetail is one person's account.
type UserDetail struct {
	ID             int       `json:"id"`
	Name           string    `json:"name"`
	Email          string    `json:"email"`
	Slug           string    `json:"slug,omitempty"`
	ExternalAuthID string    `json:"external_auth_id,omitempty"`
	CreatedAt      string    `json:"created_at,omitempty"`
	UpdatedAt      string    `json:"updated_at,omitempty"`
	LastActivityAt string    `json:"last_activity_at,omitempty"`
	Roles          []ItemRef `json:"roles"`
}

func (p *Plugin) getUser(ctx context.Context, args idArgs) (UserDetail, error) {
	if err := p.ready(); err != nil {
		return UserDetail{}, err
	}
	if args.ID <= 0 {
		return UserDetail{}, fmt.Errorf("bookstack: a user id is required")
	}
	raw, err := p.client.get(ctx, "/api/users/"+strconv.Itoa(args.ID), nil)
	p.noted(err)
	if err != nil {
		return UserDetail{}, describeMissing(explainPeopleFailure(err, "users"), "user", args.ID)
	}
	var d struct {
		userRow
		Roles []struct {
			ID          int    `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"roles"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return UserDetail{}, fmt.Errorf("bookstack: could not read the user: %w", err)
	}
	out := UserDetail{
		ID: d.ID, Name: d.Name, Email: d.Email, Slug: d.Slug,
		ExternalAuthID: d.ExternalAuthID, CreatedAt: d.CreatedAt,
		UpdatedAt: d.UpdatedAt, LastActivityAt: d.LastActivityAt,
		Roles: make([]ItemRef, 0, len(d.Roles)),
	}
	for _, r := range d.Roles {
		out.Roles = append(out.Roles, ItemRef{ID: r.ID, Name: r.DisplayName, Type: "role"})
	}
	return out, nil
}

type roleRow struct {
	ID               int    `json:"id"`
	DisplayName      string `json:"display_name"`
	Description      string `json:"description"`
	SystemName       string `json:"system_name"`
	ExternalAuthID   string `json:"external_auth_id"`
	MFAEnforced      bool   `json:"mfa_enforced"`
	UsersCount       int    `json:"users_count"`
	PermissionsCount int    `json:"permissions_count"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

// RolesResult is the role list.
type RolesResult struct {
	Roles []roleRow `json:"roles"`
	Count int       `json:"count"`
	truncation
}

func (p *Plugin) listRoles(ctx context.Context, args limitArgs) (RolesResult, error) {
	if err := p.ready(); err != nil {
		return RolesResult{}, err
	}
	pg, err := p.client.list(ctx, "/api/roles", nil, args.Limit)
	p.noted(err)
	if err != nil {
		return RolesResult{}, explainPeopleFailure(err, "roles")
	}
	rows, err := decodeRows[roleRow](pg)
	if err != nil {
		return RolesResult{}, err
	}
	rows, cut := bound(rows, pg)
	return RolesResult{Roles: rows, Count: len(rows), truncation: cut}, nil
}

// RoleDetail is one role in full.
type RoleDetail struct {
	ID          int    `json:"id"`
	DisplayName string `json:"display_name"`
	Description string `json:"description,omitempty"`
	SystemName  string `json:"system_name,omitempty"`
	MFAEnforced bool   `json:"mfa_enforced,omitempty"`
	// Permissions is every permission this role grants, by name.
	Permissions []string  `json:"permissions"`
	Users       []ItemRef `json:"users"`
	CreatedAt   string    `json:"created_at,omitempty"`
	UpdatedAt   string    `json:"updated_at,omitempty"`
}

func (p *Plugin) getRole(ctx context.Context, args idArgs) (RoleDetail, error) {
	if err := p.ready(); err != nil {
		return RoleDetail{}, err
	}
	if args.ID <= 0 {
		return RoleDetail{}, fmt.Errorf("bookstack: a role id is required")
	}
	raw, err := p.client.get(ctx, "/api/roles/"+strconv.Itoa(args.ID), nil)
	p.noted(err)
	if err != nil {
		return RoleDetail{}, describeMissing(explainPeopleFailure(err, "roles"), "role", args.ID)
	}
	var d struct {
		roleRow
		Permissions []string `json:"permissions"`
		Users       []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
			Slug string `json:"slug"`
		} `json:"users"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return RoleDetail{}, fmt.Errorf("bookstack: could not read the role: %w", err)
	}
	out := RoleDetail{
		ID: d.ID, DisplayName: d.DisplayName, Description: d.Description,
		SystemName: d.SystemName, MFAEnforced: d.MFAEnforced,
		Permissions: d.Permissions, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
		Users: make([]ItemRef, 0, len(d.Users)),
	}
	if out.Permissions == nil {
		out.Permissions = []string{}
	}
	for _, u := range d.Users {
		out.Users = append(out.Users, ItemRef{ID: u.ID, Name: u.Name, Slug: u.Slug, Type: "user"})
	}
	return out, nil
}

// --- content permissions ----------------------------------------------------

type contentPermArgs struct {
	Type string `json:"type" jsonschema:"what kind of item: bookshelf, book, chapter or page"`
	ID   int    `json:"id" jsonschema:"that item's numeric id"`
}

// RolePermission is what one role may do with one item.
type RolePermission struct {
	RoleID   int    `json:"role_id"`
	RoleName string `json:"role_name,omitempty"`
	View     bool   `json:"view"`
	Create   bool   `json:"create"`
	Update   bool   `json:"update"`
	Delete   bool   `json:"delete"`
}

// ContentPermissions is who can do what with one item.
type ContentPermissions struct {
	Type  string   `json:"type"`
	ID    int      `json:"id"`
	Owner *userRef `json:"owner,omitempty"`
	// Inheriting reports whether the item takes its permissions from whatever
	// contains it. When true the overrides below are not in force, which is
	// the distinction somebody debugging access needs first.
	Inheriting      bool             `json:"inheriting"`
	Fallback        RolePermission   `json:"fallback"`
	RolePermissions []RolePermission `json:"role_permissions"`
}

func (p *Plugin) getContentPermissions(ctx context.Context, args contentPermArgs) (ContentPermissions, error) {
	if err := p.ready(); err != nil {
		return ContentPermissions{}, err
	}
	kind, err := contentType(args.Type)
	if err != nil {
		return ContentPermissions{}, err
	}
	if args.ID <= 0 {
		return ContentPermissions{}, fmt.Errorf("bookstack: an item id is required")
	}
	raw, err := p.client.get(ctx,
		"/api/content-permissions/"+kind+"/"+strconv.Itoa(args.ID), nil)
	p.noted(err)
	if err != nil {
		return ContentPermissions{}, describeMissing(err, kind, args.ID)
	}
	var d struct {
		Owner           *userRef `json:"owner"`
		RolePermissions []struct {
			RoleID int  `json:"role_id"`
			View   bool `json:"view"`
			Create bool `json:"create"`
			Update bool `json:"update"`
			Delete bool `json:"delete"`
			Role   *struct {
				ID          int    `json:"id"`
				DisplayName string `json:"display_name"`
			} `json:"role"`
		} `json:"role_permissions"`
		Fallback struct {
			Inheriting bool `json:"inheriting"`
			View       bool `json:"view"`
			Create     bool `json:"create"`
			Update     bool `json:"update"`
			Delete     bool `json:"delete"`
		} `json:"fallback_permissions"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return ContentPermissions{}, fmt.Errorf("bookstack: could not read the permissions: %w", err)
	}
	out := ContentPermissions{
		Type: kind, ID: args.ID, Owner: d.Owner,
		Inheriting: d.Fallback.Inheriting,
		Fallback: RolePermission{
			View: d.Fallback.View, Create: d.Fallback.Create,
			Update: d.Fallback.Update, Delete: d.Fallback.Delete,
		},
		RolePermissions: make([]RolePermission, 0, len(d.RolePermissions)),
	}
	for _, r := range d.RolePermissions {
		rp := RolePermission{
			RoleID: r.RoleID, View: r.View, Create: r.Create,
			Update: r.Update, Delete: r.Delete,
		}
		if r.Role != nil {
			rp.RoleName = r.Role.DisplayName
		}
		out.RolePermissions = append(out.RolePermissions, rp)
	}
	return out, nil
}

// contentType checks the item kind against what the API serves.
//
// BookStack calls a shelf "bookshelf" on this endpoint and "shelf" everywhere
// else, so both are accepted and the API's spelling is what goes on the wire.
func contentType(in string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(in)) {
	case "bookshelf", "shelf":
		return "bookshelf", nil
	case "book":
		return "book", nil
	case "chapter":
		return "chapter", nil
	case "page":
		return "page", nil
	}
	return "", fmt.Errorf("bookstack: %q is not a kind of item that carries "+
		"permissions; use bookshelf, book, chapter or page", in)
}

// explainPeopleFailure adds the reason a 403 happens here specifically.
func explainPeopleFailure(err error, what string) error {
	if err == nil || !strings.Contains(err.Error(), ErrForbidden.Error()) {
		return err
	}
	return fmt.Errorf("%w. Reading %s needs the \"manage %s\" permission on the "+
		"token owner's role in BookStack", err, what, what)
}
