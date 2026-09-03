package operations

import (
	"fmt"

	"github.com/spoked/mcpd/internal/auth"
)

// ApprovalPolicy decides who may approve what.
//
// It lives here rather than in the auth package because every decision it
// makes depends on an operation's risk, and auth deliberately knows nothing
// about operations.
type ApprovalPolicy struct {
	authz *auth.Authorizer
}

// NewApprovalPolicy binds an authorizer.
func NewApprovalPolicy(authz *auth.Authorizer) *ApprovalPolicy {
	return &ApprovalPolicy{authz: authz}
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
// There is deliberately no rule requiring a second person. Approval happens in
// the conversation with the person who asked for the change, and a proposal
// only that person can see is not one a colleague can act on -- so the rule
// produced changes nobody could ever enact rather than changes two people
// agreed on.
func (a *ApprovalPolicy) AuthorizeApproval(p *auth.Principal, op *Operation) auth.Decision {
	if op == nil {
		return auth.Deny(auth.CodeNotAuthorized, "no operation supplied")
	}
	if d := a.authz.AuthorizeEndpoint(p, op.Plugin); !d.Allowed {
		return d
	}
	if !p.Can(auth.PermApprovalsDecide) {
		return auth.Deny(auth.CodeNotAuthorized,
			fmt.Sprintf("principal %s may not decide on approvals", p.ID))
	}
	return auth.Allow()
}
