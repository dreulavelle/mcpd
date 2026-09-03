// Package auth separates authentication (who is calling) from authorization
// (what they may do). The two are deliberately distinct types: a verified
// identity carries no permissions on its own, and a permission check never
// re-derives identity.
//
// Two questions, two predicates. Can answers what a caller may *do* on this
// host, from the permissions its role and its groups' roles carry. Reaches
// answers what it may *reach* through this host, from its grants. Nothing
// else in the process decides either; a rule applied here is applied
// everywhere.
package auth

import (
	"fmt"
	"strings"
)

// Capability is what a tool declares it takes to call.
//
// It is the plugin author's vocabulary rather than the host's: a tool says
// whether it reads, proposes a change, decides on one, or reveals something
// that is itself the privilege. The authorizer translates that into a grant
// level or a host permission. Kept separate from Permission so that a plugin
// written against one version of this host keeps working when the host's
// permission areas change.
type Capability string

const (
	// CapRead is an ordinary read of the plugin. Needs the plugin at read.
	CapRead Capability = "read"
	// CapPropose proposes a change through the plugin. Needs it at write.
	CapPropose Capability = "propose"
	// CapApprove decides on a proposed change. Needs the plugin at read and
	// approvals at decide.
	CapApprove Capability = "approve"
	// CapAdmin is a read whose answer is itself the privilege -- a credential,
	// a passcode. Needs the plugin at read and plugins at write, which is the
	// permission that could have read the credential out of the plugin's own
	// settings anyway.
	CapAdmin Capability = "admin"
)

// Valid reports whether c is a recognised capability. Plugins name one when a
// tool needs more than read, so it is checked rather than assumed.
func (c Capability) Valid() bool {
	switch c {
	case CapRead, CapPropose, CapApprove, CapAdmin:
		return true
	}
	return false
}

// Principal is a verified caller. It is produced only by a TokenVerifier and
// is immutable thereafter.
type Principal struct {
	// ID uniquely identifies the caller, e.g. "user:alice" or
	// "service:chatgpt-connector". It appears in every audit record, and
	// every stored approval rule matches on it, so it never changes shape.
	ID string

	// DisplayName is for dashboards and logs. It is never used for
	// authorization decisions.
	DisplayName string

	// RoleID and RoleName say which role the caller holds directly. For
	// rendering and for the ledger; Permissions is what decides.
	RoleID   string
	RoleName string

	// Permissions is everything the caller may do: its own role, merged with
	// the role of every group it belongs to. Resolved when the credential is
	// verified, so a role edited or a membership changed takes effect on the
	// next request.
	Permissions Permissions

	// Grants is everything the caller may reach, resolved the same way. An
	// empty list reaches nothing, which is the safe reading of an incomplete
	// configuration.
	Grants Grants

	// TokenID identifies the credential used, for revocation and audit. It is
	// never the credential itself.
	TokenID string

	// Pending reports a registration nobody has approved yet.
	//
	// It is separate from the role rather than a role of its own, because it
	// is not a smaller set of rights -- it is the absence of a decision. The
	// person has proved who they are, which is what lets them see a page
	// saying so; what they have not got is anybody's word that they may do
	// anything here.
	//
	// The zero value is the safe one and that is deliberate. Every principal
	// that is not an account -- a static token, the tunnel's identity -- is
	// constructed without touching this field and is therefore not pending,
	// which is what those callers have always been.
	Pending bool
}

// Can reports whether the principal holds a permission.
//
// A pending registration holds none, whatever its row says its role is. The
// check belongs here rather than in each handler because this is the one
// function every host-permission decision in the process goes through, so a
// permission withheld here is withheld everywhere -- the dashboard API, the
// MCP endpoint, a tool call, and anything added later that forgets pending
// accounts exist.
func (p *Principal) Can(perm Permission) bool {
	if p == nil || p.Pending {
		return false
	}
	return p.Permissions.Holds(perm)
}

// Reaches reports whether the principal may use a plugin at a level.
//
// Pending holds nothing here either, for the same reason as Can.
func (p *Principal) Reaches(plugin string, level Level) bool {
	if p == nil || p.Pending {
		return false
	}
	return p.Grants.Reaches(plugin, level)
}

// CanAccessPlugin reports whether the principal may reach a plugin at all.
// Reaches at read, under the name the rest of the process has always used.
func (p *Principal) CanAccessPlugin(name string) bool {
	return p.Reaches(name, LevelRead)
}

// PermissionList lists everything the principal may do, in display order.
//
// It is derived from Can rather than kept alongside it, so that a page
// rendering "what you may do" cannot disagree with the check that decides it.
// The dashboard draws its controls from this list. Never nil: an empty list
// is an answer, and a principal that holds nothing should render as holding
// nothing rather than as a question nobody asked.
func (p *Principal) PermissionList() []Permission {
	if p == nil || p.Pending {
		return []Permission{}
	}
	return p.Permissions.List()
}

// Validate checks that a principal is internally coherent. It runs when
// credentials are loaded, so a malformed grant fails at startup rather than at
// the first request.
func (p *Principal) Validate() error {
	if p == nil {
		return fmt.Errorf("auth: nil principal")
	}
	if strings.TrimSpace(p.ID) == "" {
		return fmt.Errorf("auth: principal requires an id")
	}
	if err := p.Permissions.Validate(); err != nil {
		return fmt.Errorf("auth: principal %s: %w", p.ID, err)
	}
	if len(p.Grants) == 0 {
		return fmt.Errorf("auth: principal %s grants no plugin access; "+
			"list plugins explicitly or use %q", p.ID, Wildcard)
	}
	if err := p.Grants.Validate(); err != nil {
		return fmt.Errorf("auth: principal %s: %w", p.ID, err)
	}
	return nil
}

// String renders a principal for logs. It deliberately omits the token id so
// that log output cannot be correlated back to a specific credential by
// someone reading only the logs.
func (p *Principal) String() string {
	if p == nil {
		return "anonymous"
	}
	role := p.RoleName
	if role == "" {
		role = p.RoleID
	}
	return fmt.Sprintf("%s(%s)", p.ID, role)
}

// Anonymous is the principal for unauthenticated requests. It holds no
// permission and no grant, so every check against it fails.
func Anonymous() *Principal {
	return &Principal{ID: "anonymous"}
}

// Equal reports whether two principals grant exactly the same thing.
//
// Order is not significant: the same grants listed differently are the same
// grants, and treating them as a change would restart a tunnel for nothing.
func (p Principal) Equal(other Principal) bool {
	if p.ID != other.ID || p.RoleID != other.RoleID ||
		p.TokenID != other.TokenID || p.Pending != other.Pending {
		return false
	}
	return p.Permissions.Equal(other.Permissions) && p.Grants.Equal(other.Grants)
}
