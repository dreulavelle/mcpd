package bookstack

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spoked/mcpd/internal/operations"
	"github.com/spoked/mcpd/internal/plugins"
)

// Writing people, roles and who can see what.
//
// These are the changes that decide access rather than content, and they are
// declared at the top of the risk scale for a reason that is not squeamishness:
// a page written badly is noticed and corrected, while a permission granted
// wrongly is silent, and stays silent until somebody reads what they should
// not have. So nothing here is reversible in the sense the content mutations
// are -- BookStack keeps no recycle bin for a deleted user or role, and a
// permission overwritten is simply gone -- which also means no standing rule
// can ever approve one on its own.

func (p *Plugin) peopleMutations() []mutationEntry {
	return []mutationEntry{
		entry(plugins.MutationSpec{
			Action: "user.create",
			Title:  "Create a user",
			Description: "Proposes a new BookStack account, with the roles it should " +
				"hold. Optionally sends the person an invitation to set a password.",
			Risk: operations.RiskHigh,
			// Deleting the user is the way back, and that delete is offered here.
			Reversible: true,
			Verifiable: true,
		}, &userCreate{p: p}),

		entry(plugins.MutationSpec{
			Action: "user.update",
			Title:  "Update a user",
			Description: "Proposes changing a person's name, email, or the roles " +
				"they hold. Changing roles changes what they can see.",
			Risk:       operations.RiskHigh,
			Reversible: true,
			Verifiable: true,
		}, &userUpdate{p: p}),

		entry(plugins.MutationSpec{
			Action: "user.delete",
			Title:  "Delete a user",
			Description: "Proposes removing a person's account. Their content can be " +
				"reassigned to somebody else; without that it is left ownerless. " +
				"There is no recycle bin for a user.",
			Risk:       operations.RiskCritical,
			Reversible: false,
			Verifiable: true,
		}, &userDelete{p: p}),

		entry(plugins.MutationSpec{
			Action: "role.create",
			Title:  "Create a role",
			Description: "Proposes a new role with a set of permissions. " +
				"get_role on an existing role shows what the permission names look like.",
			Risk:       operations.RiskHigh,
			Reversible: true,
			Verifiable: true,
		}, &roleCreate{p: p}),

		entry(plugins.MutationSpec{
			Action: "role.update",
			Title:  "Update a role",
			Description: "Proposes changing a role's name, description or " +
				"permissions. Setting permissions replaces the whole set, and every " +
				"person holding the role is affected at once.",
			Risk:       operations.RiskCritical,
			Reversible: true,
			Verifiable: true,
		}, &roleUpdate{p: p}),

		entry(plugins.MutationSpec{
			Action: "role.delete",
			Title:  "Delete a role",
			Description: "Proposes removing a role. Everybody holding it loses " +
				"whatever it granted, and there is no recycle bin for a role.",
			Risk:       operations.RiskCritical,
			Reversible: false,
			Verifiable: true,
		}, &roleDelete{p: p}),
	}
}

func (p *Plugin) permissionMutations() []mutationEntry {
	return []mutationEntry{
		entry(plugins.MutationSpec{
			Action: "content_permissions.update",
			Title:  "Change who can see an item",
			Description: "Proposes changing the permission overrides on one shelf, " +
				"book, chapter or page — including whether it inherits from what " +
				"contains it. Setting role overrides replaces the whole set.",
			Risk: operations.RiskHigh,
			// The previous overrides are captured in the plan, and the rollback
			// carries them, so there is a way back.
			Reversible: true,
			Verifiable: true,
		}, &permissionsUpdate{p: p}),
	}
}

// --- users ------------------------------------------------------------------

// personState is what the user mutations observe.
type personState struct {
	Exists  bool     `json:"exists"`
	ID      int      `json:"id,omitempty"`
	Name    string   `json:"name,omitempty"`
	Email   string   `json:"email,omitempty"`
	Roles   []string `json:"roles,omitempty"`
	Updated string   `json:"updated_at,omitempty"`
}

// UserCreateParams is a new account.
type UserCreateParams struct {
	Name  string `json:"name" jsonschema:"the person's name"`
	Email string `json:"email" jsonschema:"their email address; it has to be unique"`
	Roles []int  `json:"roles,omitempty" jsonschema:"the ids of the roles they should hold; list_roles reports them"`
	// A password set through the API is one nobody has agreed to, so the
	// invitation is the better path and the default.
	SendInvite bool   `json:"send_invite,omitempty" jsonschema:"email them an invitation to set their own password"`
	Password   string `json:"password,omitempty" jsonschema:"set a password directly instead of inviting them; at least eight characters"`
}

type userCreate struct{ p *Plugin }

func (h *userCreate) Plan(ctx context.Context, params UserCreateParams) (plugins.Plan[personState], error) {
	var plan plugins.Plan[personState]
	if err := h.p.mutationReady(); err != nil {
		return plan, err
	}
	name := strings.TrimSpace(params.Name)
	email := strings.TrimSpace(params.Email)
	if name == "" || email == "" {
		return plan, fmt.Errorf("bookstack: a user needs a name and an email address")
	}
	roles, err := h.p.describeRoles(ctx, params.Roles)
	if err != nil {
		return plan, err
	}
	changes := []operations.Change{
		{Field: "user", From: nil, To: name},
		{Field: "email", From: nil, To: email},
		{Field: "roles", From: nil, To: roles},
	}
	impact := fmt.Sprintf("Creates an account for %s with the role(s): %s. That "+
		"decides what they can read and change.", name, roles)
	if params.Password != "" {
		// Worth naming in the impact rather than leaving in the parameters:
		// the password is not shown in the diff, and somebody approving should
		// know one was set rather than an invitation sent.
		impact += " A password is being set directly rather than an invitation sent."
	}
	return plugins.Plan[personState]{
		Before:  personState{Exists: false},
		Desired: personState{Exists: true, Name: name, Email: email},
		// The email is the unique key, so its absence is the precondition
		// that matters: two proposals for the same person must not both run.
		Preconditions: map[string]any{"exists": false, "email": email},
		Changes:       changes,
		Impact:        impact,
	}, nil
}

func (h *userCreate) Apply(ctx context.Context, params UserCreateParams, _ plugins.Plan[personState]) (plugins.ApplyResult, error) {
	payload := map[string]any{
		"name": strings.TrimSpace(params.Name), "email": strings.TrimSpace(params.Email),
	}
	if len(params.Roles) > 0 {
		payload["roles"] = params.Roles
	}
	if params.Password != "" {
		payload["password"] = params.Password
	} else if params.SendInvite {
		payload["send_invite"] = true
	}
	raw, err := h.p.client.send(ctx, "POST", "/api/users", payload)
	h.p.noted(err)
	if err != nil {
		return plugins.ApplyResult{}, wrapIndeterminate(err)
	}
	return applied(raw)
}

func (h *userCreate) Observe(ctx context.Context, params UserCreateParams) (personState, error) {
	id, err := h.p.findUserByEmail(ctx, strings.TrimSpace(params.Email))
	if err != nil || id == 0 {
		return personState{Exists: false}, err
	}
	return h.p.readUser(ctx, id)
}

// UserUpdateParams changes an account.
type UserUpdateParams struct {
	ID    int    `json:"id" jsonschema:"the user's numeric id"`
	Name  string `json:"name,omitempty" jsonschema:"a new name"`
	Email string `json:"email,omitempty" jsonschema:"a new email address"`
	Roles []int  `json:"roles,omitempty" jsonschema:"replace the roles they hold with these ids"`
}

type userUpdate struct{ p *Plugin }

func (h *userUpdate) Plan(ctx context.Context, params UserUpdateParams) (plugins.Plan[personState], error) {
	var plan plugins.Plan[personState]
	if err := h.p.mutationReady(); err != nil {
		return plan, err
	}
	before, err := h.p.readUser(ctx, params.ID)
	if err != nil {
		return plan, err
	}
	desired := before
	changes := []operations.Change{}
	if n := strings.TrimSpace(params.Name); n != "" && n != before.Name {
		desired.Name = n
		changes = diffField(changes, "name", before.Name, n)
	}
	if e := strings.TrimSpace(params.Email); e != "" && e != before.Email {
		desired.Email = e
		changes = diffField(changes, "email", before.Email, e)
	}
	if params.Roles != nil {
		to, err := h.p.describeRoles(ctx, params.Roles)
		if err != nil {
			return plan, err
		}
		from := strings.Join(before.Roles, ", ")
		changes = diffField(changes, "roles", from, to)
	}
	if len(changes) == 0 {
		return plan, fmt.Errorf("bookstack: nothing to change on user %d", params.ID)
	}
	impact := fmt.Sprintf("Changes %s's account.", before.Name)
	if params.Roles != nil {
		impact += " Replacing their roles changes what they can read and change, " +
			"immediately and everywhere."
	}
	return plugins.Plan[personState]{
		Before:        before,
		Desired:       desired,
		Preconditions: map[string]any{"exists": true, "updated_at": before.Updated},
		Changes:       changes,
		Impact:        impact,
	}, nil
}

func (h *userUpdate) Apply(ctx context.Context, params UserUpdateParams, _ plugins.Plan[personState]) (plugins.ApplyResult, error) {
	payload := map[string]any{}
	if n := strings.TrimSpace(params.Name); n != "" {
		payload["name"] = n
	}
	if e := strings.TrimSpace(params.Email); e != "" {
		payload["email"] = e
	}
	if params.Roles != nil {
		payload["roles"] = params.Roles
	}
	raw, err := h.p.client.send(ctx, "PUT", "/api/users/"+strconv.Itoa(params.ID), payload)
	h.p.noted(err)
	if err != nil {
		return plugins.ApplyResult{}, wrapIndeterminate(err)
	}
	return applied(raw)
}

func (h *userUpdate) Observe(ctx context.Context, params UserUpdateParams) (personState, error) {
	return h.p.readUser(ctx, params.ID)
}

// UserDeleteParams names an account to remove.
type UserDeleteParams struct {
	ID int `json:"id" jsonschema:"the user's numeric id"`
	// Without this the person's pages are left with no owner, which is
	// tidier to decide now than to discover later.
	MigrateOwnershipID int `json:"migrate_ownership_id,omitempty" jsonschema:"give everything they own to this user id; without it their content is left ownerless"`
}

type userDelete struct{ p *Plugin }

func (h *userDelete) Plan(ctx context.Context, params UserDeleteParams) (plugins.Plan[personState], error) {
	var plan plugins.Plan[personState]
	if err := h.p.mutationReady(); err != nil {
		return plan, err
	}
	before, err := h.p.readUser(ctx, params.ID)
	if err != nil {
		return plan, err
	}
	impact := fmt.Sprintf("Deletes %s's account permanently. BookStack keeps no "+
		"copy and there is no recycle bin for a user.", before.Name)
	changes := []operations.Change{
		{Field: "user", From: before.Name, To: nil},
		{Field: "email", From: before.Email, To: nil},
		{Field: "recoverable", From: false, To: false},
	}
	if params.MigrateOwnershipID > 0 {
		to, err := h.p.readUser(ctx, params.MigrateOwnershipID)
		if err != nil {
			return plan, err
		}
		changes = append(changes, operations.Change{
			Field: "their content goes to", From: before.Name, To: to.Name,
		})
		impact += fmt.Sprintf(" Everything they own is reassigned to %s.", to.Name)
	} else {
		impact += " Everything they own is left without an owner; set " +
			"migrate_ownership_id to give it to somebody instead."
	}
	return plugins.Plan[personState]{
		Before:        before,
		Desired:       personState{Exists: false, ID: params.ID},
		Preconditions: map[string]any{"exists": true, "updated_at": before.Updated},
		Changes:       changes,
		Impact:        impact,
	}, nil
}

func (h *userDelete) Apply(ctx context.Context, params UserDeleteParams, _ plugins.Plan[personState]) (plugins.ApplyResult, error) {
	var payload any
	if params.MigrateOwnershipID > 0 {
		payload = map[string]any{"migrate_ownership_id": params.MigrateOwnershipID}
	}
	_, err := h.p.client.send(ctx, "DELETE", "/api/users/"+strconv.Itoa(params.ID), payload)
	h.p.noted(err)
	if err != nil {
		return plugins.ApplyResult{}, wrapIndeterminate(err)
	}
	return plugins.ApplyResult{UpstreamRef: strconv.Itoa(params.ID)}, nil
}

func (h *userDelete) Observe(ctx context.Context, params UserDeleteParams) (personState, error) {
	got, err := h.p.readUser(ctx, params.ID)
	if isNotFound(err) {
		return personState{Exists: false, ID: params.ID}, nil
	}
	return got, err
}

// --- roles ------------------------------------------------------------------

// roleState is what the role mutations observe.
type roleState struct {
	Exists      bool     `json:"exists"`
	ID          int      `json:"id,omitempty"`
	DisplayName string   `json:"display_name,omitempty"`
	Description string   `json:"description,omitempty"`
	Permissions []string `json:"permissions,omitempty"`
	Users       int      `json:"users,omitempty"`
	Updated     string   `json:"updated_at,omitempty"`
}

// RoleCreateParams is a new role.
type RoleCreateParams struct {
	DisplayName string   `json:"display_name" jsonschema:"what to call the role"`
	Description string   `json:"description,omitempty" jsonschema:"what it is for"`
	Permissions []string `json:"permissions,omitempty" jsonschema:"the permissions it grants; get_role on an existing role shows the names"`
	MFAEnforced bool     `json:"mfa_enforced,omitempty" jsonschema:"require multi-factor authentication for anybody holding it"`
}

type roleCreate struct{ p *Plugin }

func (h *roleCreate) Plan(ctx context.Context, params RoleCreateParams) (plugins.Plan[roleState], error) {
	var plan plugins.Plan[roleState]
	if err := h.p.mutationReady(); err != nil {
		return plan, err
	}
	name := strings.TrimSpace(params.DisplayName)
	if name == "" {
		return plan, fmt.Errorf("bookstack: a role needs a name")
	}
	sortStrings(params.Permissions)
	return plugins.Plan[roleState]{
		Before: roleState{Exists: false},
		Desired: roleState{
			Exists: true, DisplayName: name, Description: params.Description,
			Permissions: params.Permissions,
		},
		Preconditions: map[string]any{"exists": false},
		Changes: []operations.Change{
			{Field: "role", From: nil, To: name},
			{Field: "permissions", From: nil, To: params.Permissions},
		},
		Impact: fmt.Sprintf("Creates the role %q granting %d permission(s). "+
			"Nobody holds it until they are given it.", name, len(params.Permissions)),
	}, nil
}

func (h *roleCreate) Apply(ctx context.Context, params RoleCreateParams, _ plugins.Plan[roleState]) (plugins.ApplyResult, error) {
	payload := map[string]any{"display_name": strings.TrimSpace(params.DisplayName)}
	if params.Description != "" {
		payload["description"] = params.Description
	}
	if len(params.Permissions) > 0 {
		payload["permissions"] = params.Permissions
	}
	if params.MFAEnforced {
		payload["mfa_enforced"] = true
	}
	raw, err := h.p.client.send(ctx, "POST", "/api/roles", payload)
	h.p.noted(err)
	if err != nil {
		return plugins.ApplyResult{}, wrapIndeterminate(err)
	}
	return applied(raw)
}

func (h *roleCreate) Observe(ctx context.Context, params RoleCreateParams) (roleState, error) {
	id, err := h.p.findRoleByName(ctx, strings.TrimSpace(params.DisplayName))
	if err != nil || id == 0 {
		return roleState{Exists: false}, err
	}
	return h.p.readRole(ctx, id)
}

// RoleUpdateParams changes a role.
type RoleUpdateParams struct {
	ID          int      `json:"id" jsonschema:"the role's numeric id"`
	DisplayName string   `json:"display_name,omitempty" jsonschema:"a new name"`
	Description string   `json:"description,omitempty" jsonschema:"a new description"`
	Permissions []string `json:"permissions,omitempty" jsonschema:"replace the permissions with these; this is the whole set, not an addition"`
}

type roleUpdate struct{ p *Plugin }

func (h *roleUpdate) Plan(ctx context.Context, params RoleUpdateParams) (plugins.Plan[roleState], error) {
	var plan plugins.Plan[roleState]
	if err := h.p.mutationReady(); err != nil {
		return plan, err
	}
	before, err := h.p.readRole(ctx, params.ID)
	if err != nil {
		return plan, err
	}
	desired := before
	changes := []operations.Change{}
	if n := strings.TrimSpace(params.DisplayName); n != "" && n != before.DisplayName {
		desired.DisplayName = n
		changes = diffField(changes, "display_name", before.DisplayName, n)
	}
	if d := params.Description; d != "" && d != before.Description {
		desired.Description = d
		changes = diffField(changes, "description", before.Description, d)
	}
	if params.Permissions != nil {
		want := append([]string(nil), params.Permissions...)
		sortStrings(want)
		// Named rather than counted: "62 becomes 48" tells an approver
		// nothing about which forty-eight.
		added, removed := diffSets(before.Permissions, want)
		if len(added) > 0 {
			changes = append(changes, operations.Change{Field: "permissions granted", From: nil, To: added})
		}
		if len(removed) > 0 {
			changes = append(changes, operations.Change{Field: "permissions taken away", From: removed, To: nil})
		}
		desired.Permissions = want
	}
	if len(changes) == 0 {
		return plan, fmt.Errorf("bookstack: nothing to change on role %d", params.ID)
	}
	impact := fmt.Sprintf("Changes the role %q.", before.DisplayName)
	if params.Permissions != nil {
		impact += fmt.Sprintf(" %d person/people hold it, and their access changes "+
			"immediately. Setting permissions replaces the whole set rather than "+
			"adding to it.", before.Users)
	}
	return plugins.Plan[roleState]{
		Before:        before,
		Desired:       desired,
		Preconditions: map[string]any{"exists": true, "updated_at": before.Updated},
		Changes:       changes,
		Impact:        impact,
		Rollback: RoleUpdateParams{
			ID: params.ID, DisplayName: before.DisplayName,
			Description: before.Description, Permissions: before.Permissions,
		},
	}, nil
}

func (h *roleUpdate) Apply(ctx context.Context, params RoleUpdateParams, _ plugins.Plan[roleState]) (plugins.ApplyResult, error) {
	payload := map[string]any{}
	if n := strings.TrimSpace(params.DisplayName); n != "" {
		payload["display_name"] = n
	}
	if params.Description != "" {
		payload["description"] = params.Description
	}
	if params.Permissions != nil {
		payload["permissions"] = params.Permissions
	}
	raw, err := h.p.client.send(ctx, "PUT", "/api/roles/"+strconv.Itoa(params.ID), payload)
	h.p.noted(err)
	if err != nil {
		return plugins.ApplyResult{}, wrapIndeterminate(err)
	}
	return applied(raw)
}

func (h *roleUpdate) Observe(ctx context.Context, params RoleUpdateParams) (roleState, error) {
	return h.p.readRole(ctx, params.ID)
}

// RoleDeleteParams names a role to remove.
type RoleDeleteParams struct {
	ID int `json:"id" jsonschema:"the role's numeric id"`
}

type roleDelete struct{ p *Plugin }

func (h *roleDelete) Plan(ctx context.Context, params RoleDeleteParams) (plugins.Plan[roleState], error) {
	var plan plugins.Plan[roleState]
	if err := h.p.mutationReady(); err != nil {
		return plan, err
	}
	before, err := h.p.readRole(ctx, params.ID)
	if err != nil {
		return plan, err
	}
	return plugins.Plan[roleState]{
		Before:        before,
		Desired:       roleState{Exists: false, ID: params.ID},
		Preconditions: map[string]any{"exists": true, "updated_at": before.Updated},
		Changes: []operations.Change{
			{Field: "role", From: before.DisplayName, To: nil},
			{Field: "people who hold it", From: before.Users, To: 0},
			{Field: "recoverable", From: false, To: false},
		},
		Impact: fmt.Sprintf("Deletes the role %q permanently. The %d person/people "+
			"holding it lose whatever it granted, and there is no recycle bin for "+
			"a role.", before.DisplayName, before.Users),
	}, nil
}

func (h *roleDelete) Apply(ctx context.Context, params RoleDeleteParams, _ plugins.Plan[roleState]) (plugins.ApplyResult, error) {
	_, err := h.p.client.send(ctx, "DELETE", "/api/roles/"+strconv.Itoa(params.ID), nil)
	h.p.noted(err)
	if err != nil {
		return plugins.ApplyResult{}, wrapIndeterminate(err)
	}
	return plugins.ApplyResult{UpstreamRef: strconv.Itoa(params.ID)}, nil
}

func (h *roleDelete) Observe(ctx context.Context, params RoleDeleteParams) (roleState, error) {
	got, err := h.p.readRole(ctx, params.ID)
	if isNotFound(err) {
		return roleState{Exists: false, ID: params.ID}, nil
	}
	return got, err
}

// --- content permissions ----------------------------------------------------

// permissionState is what the permission mutation observes.
type permissionState struct {
	Type       string           `json:"type"`
	ID         int              `json:"id"`
	Inheriting bool             `json:"inheriting"`
	Fallback   RolePermission   `json:"fallback"`
	Roles      []RolePermission `json:"role_permissions"`
}

// PermissionsUpdateParams changes who can see one item.
type PermissionsUpdateParams struct {
	Type string `json:"type" jsonschema:"what kind of item: bookshelf, book, chapter or page"`
	ID   int    `json:"id" jsonschema:"that item's numeric id"`
	// Inheriting true throws the overrides away and takes the container's
	// permissions instead, which is usually what "put it back to normal" means.
	Inheriting *bool `json:"inheriting,omitempty" jsonschema:"true to take permissions from whatever contains this item, discarding the overrides"`
	OwnerID    int   `json:"owner_id,omitempty" jsonschema:"give the item to this user"`
	// Roles replaces the whole set of role overrides.
	Roles []RolePermission `json:"role_permissions,omitempty" jsonschema:"replace the per-role overrides with these; this is the whole set"`
}

type permissionsUpdate struct{ p *Plugin }

func (h *permissionsUpdate) Plan(ctx context.Context, params PermissionsUpdateParams) (plugins.Plan[permissionState], error) {
	var plan plugins.Plan[permissionState]
	if err := h.p.mutationReady(); err != nil {
		return plan, err
	}
	kind, err := contentType(params.Type)
	if err != nil {
		return plan, err
	}
	current, err := h.p.getContentPermissions(ctx, contentPermArgs{Type: kind, ID: params.ID})
	if err != nil {
		return plan, err
	}
	before := permissionState{
		Type: kind, ID: params.ID, Inheriting: current.Inheriting,
		Fallback: current.Fallback, Roles: current.RolePermissions,
	}
	desired := before
	changes := []operations.Change{}
	if params.Inheriting != nil && *params.Inheriting != before.Inheriting {
		desired.Inheriting = *params.Inheriting
		changes = diffField(changes, "inheriting", before.Inheriting, *params.Inheriting)
	}
	if params.Roles != nil {
		desired.Roles = params.Roles
		changes = append(changes, operations.Change{
			Field: "role overrides",
			From:  describePermissions(before.Roles),
			To:    describePermissions(params.Roles),
		})
	}
	if params.OwnerID > 0 {
		to, err := h.p.readUser(ctx, params.OwnerID)
		if err != nil {
			return plan, err
		}
		from := ""
		if current.Owner != nil {
			from = current.Owner.Name
		}
		changes = diffField(changes, "owner", from, to.Name)
	}
	if len(changes) == 0 {
		return plan, fmt.Errorf("bookstack: nothing to change on the permissions "+
			"for %s %d", singular(kind+"s"), params.ID)
	}
	return plugins.Plan[permissionState]{
		Before:  before,
		Desired: desired,
		// There is no updated_at on a permission set, so what is checked is
		// the shape itself: if the overrides have moved since this was
		// proposed, the check fails and somebody looks again.
		Preconditions: map[string]any{
			"inheriting": before.Inheriting,
			"overrides":  describePermissions(before.Roles),
		},
		Changes: changes,
		Impact: fmt.Sprintf("Changes who can see and change this %s. Setting role "+
			"overrides replaces the whole set rather than adding to it, and the "+
			"effect is immediate for everybody.", singular(kind+"s")),
		Rollback: PermissionsUpdateParams{
			Type: kind, ID: params.ID,
			Inheriting: &before.Inheriting, Roles: before.Roles,
		},
	}, nil
}

func (h *permissionsUpdate) Apply(ctx context.Context, params PermissionsUpdateParams, _ plugins.Plan[permissionState]) (plugins.ApplyResult, error) {
	kind, err := contentType(params.Type)
	if err != nil {
		return plugins.ApplyResult{}, err
	}
	payload := map[string]any{}
	if params.OwnerID > 0 {
		payload["owner_id"] = params.OwnerID
	}
	if params.Inheriting != nil {
		payload["fallback_permissions"] = map[string]any{"inheriting": *params.Inheriting}
	}
	if params.Roles != nil {
		roles := make([]map[string]any, 0, len(params.Roles))
		for _, r := range params.Roles {
			roles = append(roles, map[string]any{
				"role_id": r.RoleID, "view": r.View, "create": r.Create,
				"update": r.Update, "delete": r.Delete,
			})
		}
		payload["role_permissions"] = roles
	}
	raw, err := h.p.client.send(ctx, "PUT",
		"/api/content-permissions/"+kind+"/"+strconv.Itoa(params.ID), payload)
	h.p.noted(err)
	if err != nil {
		return plugins.ApplyResult{}, wrapIndeterminate(err)
	}
	return applied(raw)
}

func (h *permissionsUpdate) Observe(ctx context.Context, params PermissionsUpdateParams) (permissionState, error) {
	kind, err := contentType(params.Type)
	if err != nil {
		return permissionState{}, err
	}
	got, err := h.p.getContentPermissions(ctx, contentPermArgs{Type: kind, ID: params.ID})
	if err != nil {
		return permissionState{}, err
	}
	return permissionState{
		Type: kind, ID: params.ID, Inheriting: got.Inheriting,
		Fallback: got.Fallback, Roles: got.RolePermissions,
	}, nil
}

// --- shared -----------------------------------------------------------------

func (p *Plugin) readUser(ctx context.Context, id int) (personState, error) {
	if id <= 0 {
		return personState{}, fmt.Errorf("bookstack: a user id is required; " +
			"list_users reports them")
	}
	got, err := p.getUser(ctx, idArgs{ID: id})
	if err != nil {
		if isNotFound(err) {
			return personState{Exists: false, ID: id}, err
		}
		return personState{}, err
	}
	roles := make([]string, 0, len(got.Roles))
	for _, r := range got.Roles {
		roles = append(roles, r.Name)
	}
	sortStrings(roles)
	return personState{
		Exists: true, ID: got.ID, Name: got.Name, Email: got.Email,
		Roles: roles, Updated: got.UpdatedAt,
	}, nil
}

func (p *Plugin) readRole(ctx context.Context, id int) (roleState, error) {
	if id <= 0 {
		return roleState{}, fmt.Errorf("bookstack: a role id is required; " +
			"list_roles reports them")
	}
	got, err := p.getRole(ctx, idArgs{ID: id})
	if err != nil {
		if isNotFound(err) {
			return roleState{Exists: false, ID: id}, err
		}
		return roleState{}, err
	}
	perms := append([]string(nil), got.Permissions...)
	sortStrings(perms)
	return roleState{
		Exists: true, ID: got.ID, DisplayName: got.DisplayName,
		Description: got.Description, Permissions: perms,
		Users: len(got.Users), Updated: got.UpdatedAt,
	}, nil
}

func (p *Plugin) describeRoles(ctx context.Context, ids []int) (string, error) {
	if len(ids) == 0 {
		return "(none)", nil
	}
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		r, err := p.readRole(ctx, id)
		if err != nil {
			return "", err
		}
		names = append(names, r.DisplayName)
	}
	sortStrings(names)
	return strings.Join(names, ", "), nil
}

func (p *Plugin) findUserByEmail(ctx context.Context, email string) (int, error) {
	pg, err := p.client.list(ctx, "/api/users", urlValues("filter[email]", email), 3)
	p.noted(err)
	if err != nil {
		return 0, err
	}
	rows, err := decodeRows[userRow](pg)
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		if strings.EqualFold(r.Email, email) {
			return r.ID, nil
		}
	}
	return 0, nil
}

func (p *Plugin) findRoleByName(ctx context.Context, name string) (int, error) {
	pg, err := p.client.list(ctx, "/api/roles", nil, 0)
	p.noted(err)
	if err != nil {
		return 0, err
	}
	rows, err := decodeRows[roleRow](pg)
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		if strings.EqualFold(strings.TrimSpace(r.DisplayName), name) {
			return r.ID, nil
		}
	}
	return 0, nil
}

// diffSets says what was added and what was taken away.
func diffSets(before, after []string) (added, removed []string) {
	has := func(list []string, v string) bool {
		for _, s := range list {
			if s == v {
				return true
			}
		}
		return false
	}
	for _, v := range after {
		if !has(before, v) {
			added = append(added, v)
		}
	}
	for _, v := range before {
		if !has(after, v) {
			removed = append(removed, v)
		}
	}
	return added, removed
}

// describePermissions renders role overrides for a diff.
func describePermissions(in []RolePermission) string {
	if len(in) == 0 {
		return "(none)"
	}
	out := make([]string, 0, len(in))
	for _, r := range in {
		name := r.RoleName
		if name == "" {
			name = "role " + strconv.Itoa(r.RoleID)
		}
		verbs := make([]string, 0, 4)
		for _, v := range []struct {
			on   bool
			word string
		}{{r.View, "view"}, {r.Create, "create"}, {r.Update, "update"}, {r.Delete, "delete"}} {
			if v.on {
				verbs = append(verbs, v.word)
			}
		}
		if len(verbs) == 0 {
			verbs = append(verbs, "nothing")
		}
		out = append(out, name+": "+strings.Join(verbs, "/"))
	}
	sortStrings(out)
	return strings.Join(out, "; ")
}

// urlValues is a one-pair query, which several lookups here want.
func urlValues(key, value string) url.Values {
	return url.Values{key: {value}}
}
