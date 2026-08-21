// Package auth separates authentication (who is calling) from authorization
// (what they may do). The two are deliberately distinct types: a verified
// identity carries no permissions on its own, and a permission check never
// re-derives identity.
package auth

import (
	"fmt"
	"slices"
	"strings"
)

// Role is a coarse capability bundle.
//
// There are two, and the line between them is administering the host rather
// than operating it. A user reads, proposes, and approves -- everything the
// integrations exist to do. An administrator additionally changes settings,
// makes and assigns tunnels, manages accounts, and clears history.
//
// It used to be four, laddered viewer -> operator -> approver -> admin. The
// finer steps were never asked for and the ladder invited a reading it did not
// support: separating proposing from approving only means something when the
// two are different people, and the second-approver rule that would have made
// that so was dropped. Two roles say what is actually enforced.
type Role string

const (
	// RoleUser may read, propose, and approve.
	RoleUser Role = "user"
	// RoleAdmin may additionally administer the host and its plugins.
	RoleAdmin Role = "admin"
)

// Valid reports whether r is a recognised role.
func (r Role) Valid() bool {
	switch r {
	case RoleUser, RoleAdmin:
		return true
	}
	return false
}

func (r Role) String() string { return string(r) }

// Capability names a single permitted action.
type Capability string

const (
	CapRead    Capability = "read"
	CapPropose Capability = "propose"
	CapApprove Capability = "approve"
	CapAdmin   Capability = "admin"
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

// roleCapabilities is the authoritative role-to-capability mapping.
var roleCapabilities = map[Role][]Capability{
	RoleUser:  {CapRead, CapPropose, CapApprove},
	RoleAdmin: {CapRead, CapPropose, CapApprove, CapAdmin},
}

// Wildcard grants access to every plugin. It is spelled out rather than
// represented by an empty set, so that a misconfigured principal with no
// plugins listed is denied everything rather than granted everything.
const Wildcard = "*"

// Principal is a verified caller. It is produced only by a TokenVerifier and
// is immutable thereafter.
type Principal struct {
	// ID uniquely identifies the caller, e.g. "user:alice" or
	// "service:chatgpt-connector". It appears in every audit record.
	ID string

	// DisplayName is for dashboards and logs. It is never used for
	// authorization decisions.
	DisplayName string

	// Role is the caller's capability bundle.
	Role Role

	// Plugins lists the plugin endpoints this principal may reach, or the
	// single element Wildcard. This is what lets one agent be handed access to
	// exactly one plugin while another sees a different set.
	Plugins []string

	// TokenID identifies the credential used, for revocation and audit. It is
	// never the credential itself.
	TokenID string
}

// Can reports whether the principal holds a capability.
func (p *Principal) Can(c Capability) bool {
	if p == nil {
		return false
	}
	return slices.Contains(roleCapabilities[p.Role], c)
}

// CanAccessPlugin reports whether the principal may reach a plugin endpoint.
//
// A principal with no plugins listed is denied everything. Empty means "no
// grants were made", which is the safe reading of an incomplete configuration.
func (p *Principal) CanAccessPlugin(name string) bool {
	if p == nil || name == "" {
		return false
	}
	for _, allowed := range p.Plugins {
		if allowed == Wildcard || allowed == name {
			return true
		}
	}
	return false
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
	if !p.Role.Valid() {
		return fmt.Errorf("auth: principal %s has unknown role %q", p.ID, p.Role)
	}
	if len(p.Plugins) == 0 {
		return fmt.Errorf("auth: principal %s grants no plugin access; "+
			"list plugins explicitly or use %q", p.ID, Wildcard)
	}
	for _, name := range p.Plugins {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("auth: principal %s has an empty plugin grant", p.ID)
		}
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
	return fmt.Sprintf("%s(%s)", p.ID, p.Role)
}

// Anonymous is the principal for unauthenticated requests. It holds no role
// and no plugin grants, so every capability check against it fails.
func Anonymous() *Principal {
	return &Principal{ID: "anonymous", Role: "", Plugins: nil}
}

// Equal reports whether two principals grant exactly the same thing.
//
// Plugin order is not significant: the same grants listed differently are the
// same grants, and treating them as a change would restart a tunnel for
// nothing.
func (p Principal) Equal(other Principal) bool {
	if p.ID != other.ID || p.Role != other.Role ||
		p.TokenID != other.TokenID {
		return false
	}
	if len(p.Plugins) != len(other.Plugins) {
		return false
	}
	mine := slices.Clone(p.Plugins)
	theirs := slices.Clone(other.Plugins)
	slices.Sort(mine)
	slices.Sort(theirs)
	return slices.Equal(mine, theirs)
}
