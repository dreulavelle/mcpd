package auth

import (
	"fmt"
)

// Authorization error codes. They are stable identifiers suitable for audit
// records, not prose.
const (
	CodeNotAuthorized = "NOT_AUTHORIZED"
)

// Decision is the outcome of an authorization check. It carries a reason so
// that a refusal can be logged and audited precisely, while the response to
// the caller stays deliberately vague.
type Decision struct {
	Allowed bool
	// Code is a stable identifier suitable for audit records.
	Code string
	// Reason is operator-facing detail. It is never returned to the caller.
	Reason string
}

// Allow returns a permitting decision.
func Allow() Decision { return Decision{Allowed: true} }

// Deny returns a refusing decision.
func Deny(code, reason string) Decision {
	return Decision{Allowed: false, Code: code, Reason: reason}
}

// Error renders a decision as an error, or nil when permitted.
func (d Decision) Error() error {
	if d.Allowed {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrForbidden, d.Code)
}

// Authorizer makes access decisions.
//
// It holds no identity state: every method takes the principal explicitly,
// which keeps decisions auditable and testable in isolation. It also knows
// nothing about operations or risk -- those decisions live in the operations
// package, beside the domain they concern.
type Authorizer struct{}

// NewAuthorizer returns an authorizer.
func NewAuthorizer() *Authorizer { return &Authorizer{} }

// AuthorizeEndpoint decides whether a principal may reach a plugin's MCP
// endpoint at all.
//
// This runs before any tool is dispatched, so a principal without a grant for
// a plugin cannot even enumerate its tools. Access is a property of the
// endpoint rather than of individual tools, which is what makes "give this
// agent exactly one plugin" a single grant instead of a per-tool audit.
func (a *Authorizer) AuthorizeEndpoint(p *Principal, plugin string) Decision {
	if !p.Reaches(plugin, LevelRead) {
		return Deny(CodeNotAuthorized,
			fmt.Sprintf("principal %s is not granted access to plugin %q", p.String(), plugin))
	}
	return Allow()
}

// AuthorizeTool decides whether a principal may invoke a tool that declares a
// capability.
//
// This is the one translation from a tool's vocabulary into the host's. A
// read needs the plugin at read; a proposal needs it at write; deciding needs
// approvals at decide; and a read whose answer is itself the privilege needs
// plugins at write, the permission that could have read the credential out
// of the plugin's settings anyway.
func (a *Authorizer) AuthorizeTool(p *Principal, plugin string, required Capability) Decision {
	if d := a.AuthorizeEndpoint(p, plugin); !d.Allowed {
		return d
	}
	switch required {
	case CapRead, "":
		return Allow()
	case CapPropose:
		if !p.Reaches(plugin, LevelWrite) {
			return Deny(CodeNotAuthorized,
				fmt.Sprintf("principal %s holds plugin %q at read, not write", p.String(), plugin))
		}
		return Allow()
	case CapApprove:
		if !p.Can(PermApprovalsDecide) {
			return Deny(CodeNotAuthorized,
				fmt.Sprintf("principal %s may not decide on approvals", p.String()))
		}
		return Allow()
	case CapAdmin:
		if !p.Can(PermPluginsWrite) {
			return Deny(CodeNotAuthorized,
				fmt.Sprintf("principal %s may not administer plugins", p.String()))
		}
		return Allow()
	}
	return Deny(CodeNotAuthorized,
		fmt.Sprintf("tool declares unknown capability %q", required))
}

// VisiblePlugins filters plugin names to those the principal may reach.
//
// The dashboard and any discovery endpoint use it so that a principal never
// learns a plugin exists that it cannot use.
func (a *Authorizer) VisiblePlugins(p *Principal, all []string) []string {
	out := make([]string, 0, len(all))
	for _, name := range all {
		if p.Reaches(name, LevelRead) {
			out = append(out, name)
		}
	}
	return out
}
