package operations

import (
	"fmt"

	"github.com/spoked/mcpd/internal/auth"
)

// RiskPolicy configures which risk levels demand extra scrutiny.
type RiskPolicy struct {
	// RequireDistinctApproverAtOrAbove is the risk level from which the
	// requester may not also be the approver. The zero value disables the
	// rule.
	RequireDistinctApproverAtOrAbove RiskLevel
}

// ApprovalPolicy decides who may approve what.
//
// It lives here rather than in the auth package because every decision it
// makes depends on an operation's risk, and auth deliberately knows nothing
// about operations.
type ApprovalPolicy struct {
	authz  *auth.Authorizer
	policy RiskPolicy
}

// NewApprovalPolicy binds an authorizer to a risk policy.
func NewApprovalPolicy(authz *auth.Authorizer, policy RiskPolicy) *ApprovalPolicy {
	return &ApprovalPolicy{authz: authz, policy: policy}
}

// AuthorizeEndpoint delegates to the underlying authorizer.
func (a *ApprovalPolicy) AuthorizeEndpoint(p *auth.Principal, plugin string) auth.Decision {
	return a.authz.AuthorizeEndpoint(p, plugin)
}

// AuthorizeTool delegates to the underlying authorizer.
func (a *ApprovalPolicy) AuthorizeTool(p *auth.Principal, plugin string, required auth.Capability) auth.Decision {
	return a.authz.AuthorizeTool(p, plugin, required)
}

// AuthorizeApproval decides whether a principal may approve a specific
// operation.
//
// Separation of duties fails closed. When the policy demands a distinct
// approver but the authentication mode cannot tell principals apart -- a
// shared static token being the obvious case -- the approval is refused.
// Silently disabling the rule would leave the configuration claiming a
// guarantee the system is not providing.
func (a *ApprovalPolicy) AuthorizeApproval(p *auth.Principal, op *Operation) auth.Decision {
	if op == nil {
		return auth.Deny(auth.CodeNotAuthorized, "no operation supplied")
	}
	if d := a.authz.AuthorizeEndpoint(p, op.Plugin); !d.Allowed {
		return d
	}
	if !p.Can(auth.CapApprove) {
		return auth.Deny(auth.CodeNotAuthorized,
			fmt.Sprintf("principal %s lacks the approve capability", p.ID))
	}
	if !a.RequiresDistinctApprover(op.Risk) {
		return auth.Allow()
	}
	if !p.Distinguishable {
		return auth.Deny(auth.CodeIdentityIndistinct, fmt.Sprintf(
			"operation risk %s requires a distinct approver, but the active "+
				"authentication mode cannot distinguish principals", op.Risk))
	}
	if p.ID == op.RequestedBy {
		return auth.Deny(auth.CodeSelfApproval, fmt.Sprintf(
			"principal %s proposed this %s-risk operation and may not approve it",
			p.ID, op.Risk))
	}
	return auth.Allow()
}

// RequiresDistinctApprover reports whether a risk level demands separation of
// duties under the configured policy.
func (a *ApprovalPolicy) RequiresDistinctApprover(risk RiskLevel) bool {
	threshold := a.policy.RequireDistinctApproverAtOrAbove
	if !threshold.Valid() {
		return false
	}
	return risk.AtLeast(threshold)
}
