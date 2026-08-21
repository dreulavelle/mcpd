package operations

import (
	"testing"

	"github.com/spoked/mcpd/internal/auth"
)

func policy() *ApprovalPolicy {
	return NewApprovalPolicy(auth.NewAuthorizer())
}

func principal(id string, role auth.Role) *auth.Principal {
	return &auth.Principal{ID: id, Role: role, Plugins: []string{"cnmaestro"}}
}

func opWithRisk(risk RiskLevel) *Operation {
	return &Operation{Plugin: "cnmaestro", Risk: risk, RequestedBy: "user:alice"}
}

// Approval happens in the conversation with whoever asked for the change, so
// there is no second-person rule: a proposal only the requester can see is not
// one a colleague could act on. What remains is capability, which still bites.
func TestAuthorizeApproval_RequiresTheApproveCapability(t *testing.T) {
	p := policy()

	viewer := principal("user:viewer", auth.RoleViewer)
	if d := p.AuthorizeApproval(viewer, opWithRisk(RiskLow)); d.Allowed {
		t.Fatal("a viewer must not be able to approve")
	}

	approver := principal("user:alice", auth.RoleApprover)
	for _, risk := range []RiskLevel{RiskLow, RiskMedium, RiskHigh, RiskCritical} {
		if d := p.AuthorizeApproval(approver, opWithRisk(risk)); !d.Allowed {
			t.Fatalf("%s risk was refused: %s", risk, d.Reason)
		}
	}
}

// Reaching a plugin the principal was not granted is still refused, and it is
// the check that keeps a scoped credential scoped.
func TestAuthorizeApproval_RefusesAnUngrantedPlugin(t *testing.T) {
	elsewhere := &auth.Principal{
		ID: "user:alice", Role: auth.RoleApprover, Plugins: []string{"echo"},
	}
	if d := policy().AuthorizeApproval(elsewhere, opWithRisk(RiskLow)); d.Allowed {
		t.Fatal("approving inside an ungranted plugin must be refused")
	}
}
