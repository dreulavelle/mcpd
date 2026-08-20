package auth

import (
	"fmt"

	"github.com/spoked/mcpd/internal/operations"
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

// Authorizer makes access decisions. It holds no identity state: every method
// takes the principal explicitly, which keeps decisions auditable and testable
// in isolation.
type Authorizer struct {
	policy RiskPolicy
}

// NewAuthorizer returns an authorizer bound to a risk policy.
func NewAuthorizer(p RiskPolicy) *Authorizer { return &Authorizer{policy: p} }

// RiskPolicy configures which risk levels demand extra scrutiny.
type RiskPolicy struct {
	// RequireDistinctApproverAtOrAbove is the risk level from which the
	// requester may not also be the approver. Zero value disables the rule.
	RequireDistinctApproverAtOrAbove operations.RiskLevel

	// MaxAutoApprovableRisk is reserved for a future policy permitting
	// low-risk mutations to skip approval. It is unused today; nothing is
	// auto-approved.
	MaxAutoApprovableRisk operations.RiskLevel
}

// AuthorizeEndpoint decides whether a principal may reach a plugin's MCP
// endpoint at all.
//
// This runs before any tool is dispatched, so a principal without a grant for
// a plugin cannot even enumerate its tools. Access is a property of the
// endpoint, not of individual tools, which is what makes "give this agent
// exactly one plugin" a single grant rather than a per-tool audit.
func (a *Authorizer) AuthorizeEndpoint(p *Principal, plugin string) Decision {
	if !p.Can(CapRead) {
		return Deny(operations.CodeNotAuthorized,
			fmt.Sprintf("principal %s holds no read capability", p.ID))
	}
	if !p.CanAccessPlugin(plugin) {
		return Deny(operations.CodeNotAuthorized,
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
		return Deny(operations.CodeNotAuthorized,
			fmt.Sprintf("principal %s lacks capability %q", p.ID, required))
	}
	return Allow()
}

// AuthorizeApproval decides whether a principal may approve a specific
// operation.
//
// Separation of duties fails closed. When the policy demands a distinct
// approver but the authentication mode cannot tell principals apart — a shared
// static token being the obvious case — the approval is refused. Silently
// disabling the rule would leave the configuration claiming a guarantee the
// system is not providing.
func (a *Authorizer) AuthorizeApproval(p *Principal, op *operations.Operation) Decision {
	if op == nil {
		return Deny(operations.CodeNotAuthorized, "no operation supplied")
	}
	if d := a.AuthorizeEndpoint(p, op.Plugin); !d.Allowed {
		return d
	}
	if !p.Can(CapApprove) {
		return Deny(operations.CodeNotAuthorized,
			fmt.Sprintf("principal %s lacks the approve capability", p.ID))
	}
	if !a.RequiresDistinctApprover(op.Risk) {
		return Allow()
	}
	if !p.Distinguishable {
		return Deny(operations.CodeIdentityIndistinct, fmt.Sprintf(
			"operation risk %s requires a distinct approver, but the %s authentication "+
				"mode cannot distinguish principals", op.Risk, p.TokenID))
	}
	if p.ID == op.RequestedBy {
		return Deny(operations.CodeSelfApproval, fmt.Sprintf(
			"principal %s proposed this %s-risk operation and may not approve it", p.ID, op.Risk))
	}
	return Allow()
}

// RequiresDistinctApprover reports whether a risk level demands separation of
// duties under the configured policy.
func (a *Authorizer) RequiresDistinctApprover(risk operations.RiskLevel) bool {
	threshold := a.policy.RequireDistinctApproverAtOrAbove
	if !threshold.Valid() {
		return false
	}
	return risk.AtLeast(threshold)
}

// AuthorizeAdmin decides whether a principal may perform host administration.
func (a *Authorizer) AuthorizeAdmin(p *Principal) Decision {
	if !p.Can(CapAdmin) {
		return Deny(operations.CodeNotAuthorized,
			fmt.Sprintf("principal %s is not an administrator", p.ID))
	}
	return Allow()
}

// VisiblePlugins filters a set of plugin names to those the principal may
// reach. The admin dashboard and any discovery endpoint use it so that a
// principal never learns a plugin exists that it cannot use.
func (a *Authorizer) VisiblePlugins(p *Principal, all []string) []string {
	out := make([]string, 0, len(all))
	for _, name := range all {
		if p.CanAccessPlugin(name) {
			out = append(out, name)
		}
	}
	return out
}
