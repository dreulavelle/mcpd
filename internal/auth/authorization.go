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
	if !p.Can(CapRead) {
		return Deny(CodeNotAuthorized,
			fmt.Sprintf("principal %s holds no read capability", p.ID))
	}
	if !p.CanAccessPlugin(plugin) {
		return Deny(CodeNotAuthorized,
			fmt.Sprintf("principal %s is not granted access to plugin %q", p.ID, plugin))
	}
	return Allow()
}

// AuthorizeTool decides whether a principal may invoke a tool. Read tools need
// CapRead; a tool that proposes a mutation needs CapPropose.
func (a *Authorizer) AuthorizeTool(p *Principal, plugin string, required Capability) Decision {
	if d := a.AuthorizeEndpoint(p, plugin); !d.Allowed {
		return d
	}
	if !p.Can(required) {
		return Deny(CodeNotAuthorized,
			fmt.Sprintf("principal %s lacks capability %q", p.ID, required))
	}
	return Allow()
}

// AuthorizeAdmin decides whether a principal may perform host administration.
func (a *Authorizer) AuthorizeAdmin(p *Principal) Decision {
	if !p.Can(CapAdmin) {
		return Deny(CodeNotAuthorized,
			fmt.Sprintf("principal %s is not an administrator", p.ID))
	}
	return Allow()
}

// VisiblePlugins filters plugin names to those the principal may reach.
//
// The dashboard and any discovery endpoint use it so that a principal never
// learns a plugin exists that it cannot use.
func (a *Authorizer) VisiblePlugins(p *Principal, all []string) []string {
	out := make([]string, 0, len(all))
	for _, name := range all {
		if p.CanAccessPlugin(name) {
			out = append(out, name)
		}
	}
	return out
}
