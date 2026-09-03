package auth

import "time"

// Role is a named set of host permissions.
//
// Three are built in and cannot be edited or deleted, so "what does Operator
// mean on this host" has exactly one answer everywhere it is used. Any number
// of custom roles can be composed beside them, each starting as a copy of one.
// A role attaches to a user, a key, a ChatGPT account, a static token, or a
// group, and the same struct describes it on every one of those.
//
// A role says what its holder may *do*. What they may *reach* is a separate
// axis, carried by Grants, and keeping the two apart is what lets either be
// read on its own: "why can this key edit settings" is answered by its role
// and "why can it see Graylog" by its grants, and nothing has to be read twice.
type Role struct {
	ID          string
	Name        string
	Description string
	// Builtin marks a role this build defines. Its permissions are what
	// BuiltinRoles says, re-applied at startup, so an area added in a later
	// version reaches every administrator without anybody editing anything.
	Builtin     bool
	Permissions Permissions
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	// Assigned counts the users, keys, accounts and groups holding the role.
	// Populated by the store's listings, because "delete this role" is a
	// question about who holds it.
	Assigned int
}

// Identifiers of the built-in roles. Fixed rather than generated, so a
// migration, a configuration file and a test can all name one.
const (
	// RoleReader reads everything and changes nothing. What an auditor or a
	// status screen gets.
	RoleReader = "role_reader"
	// RoleOperator reads everything, decides on proposed changes, and
	// administers nothing. What an ordinary user used to be.
	RoleOperator = "role_operator"
	// RoleAdministrator holds every area at its highest level.
	RoleAdministrator = "role_administrator"
)

// BuiltinRoles returns the roles this build defines, in display order.
//
// This is the authority for what they carry. The rows in the roles table are
// re-synchronised from it at startup, which is what makes the built-in roles
// safe to lean on: nobody can edit Administrator into something that cannot
// administer, and a new area lands in it without a migration.
func BuiltinRoles() []Role {
	everythingRead := Permissions{}
	for _, a := range Areas {
		everythingRead[a] = LevelRead
	}
	// Access is the one area a reader does not see: listing who can sign in
	// and which keys exist is a wider view than any one account's own work,
	// and the same rule kept the Users page behind admin before roles existed.
	delete(everythingRead, AreaAccess)

	operator := everythingRead.Merge(Permissions{AreaApprovals: LevelDecide})

	everything := Permissions{}
	for _, a := range Areas {
		everything[a] = a.Levels()[len(a.Levels())-1]
	}

	return []Role{
		{
			ID: RoleReader, Name: "Reader", Builtin: true,
			Description: "Reads everything except who has access, and changes nothing.",
			Permissions: everythingRead,
		},
		{
			ID: RoleOperator, Name: "Operator", Builtin: true,
			Description: "Reads everything, decides on proposed changes, and administers nothing.",
			Permissions: operator,
		},
		{
			ID: RoleAdministrator, Name: "Administrator", Builtin: true,
			Description: "Everything, including who has access to this host.",
			Permissions: everything,
		},
	}
}

// BuiltinRole returns one built-in role by id, or false.
func BuiltinRole(id string) (Role, bool) {
	for _, r := range BuiltinRoles() {
		if r.ID == id {
			return r, true
		}
	}
	return Role{}, false
}

// IsBuiltinRole reports whether id names a built-in role.
func IsBuiltinRole(id string) bool {
	_, ok := BuiltinRole(id)
	return ok
}

// LegacyRoleID maps the two role names this host used to have onto the
// built-in roles that mean the same thing. For a configuration file written
// before roles were editable, and for nothing else.
func LegacyRoleID(name string) (string, bool) {
	switch name {
	case "user", "operator", RoleOperator:
		return RoleOperator, true
	case "admin", "administrator", RoleAdministrator:
		return RoleAdministrator, true
	case "reader", RoleReader:
		return RoleReader, true
	}
	return "", false
}
