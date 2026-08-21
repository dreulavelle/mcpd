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
//
// Both roles approve, at every risk. That is the point of collapsing the four:
// a deployment where only administrators could approve would send every routine
// change to the one person holding the settings, and a gate that inconvenient
// is one people route around.
func TestAuthorizeApproval_RequiresTheApproveCapability(t *testing.T) {
	p := policy()

	for _, role := range []auth.Role{auth.RoleUser, auth.RoleAdmin} {
		actor := principal("user:alice", role)
		for _, risk := range []RiskLevel{RiskLow, RiskMedium, RiskHigh, RiskCritical} {
			if d := p.AuthorizeApproval(actor, opWithRisk(risk)); !d.Allowed {
				t.Fatalf("%s at %s risk was refused: %s", role, risk, d.Reason)
			}
		}
	}

	// A principal carrying no role at all holds nothing, which is what an
	// unauthenticated caller looks like by the time it reaches here.
	if d := p.AuthorizeApproval(auth.Anonymous(), opWithRisk(RiskLow)); d.Allowed {
		t.Fatal("an anonymous caller must not be able to approve")
	}
}

// Reaching a plugin the principal was not granted is still refused, and it is
// the check that keeps a scoped credential scoped.
func TestAuthorizeApproval_RefusesAnUngrantedPlugin(t *testing.T) {
	elsewhere := &auth.Principal{
		ID: "user:alice", Role: auth.RoleUser, Plugins: []string{"echo"},
	}
	if d := policy().AuthorizeApproval(elsewhere, opWithRisk(RiskLow)); d.Allowed {
		t.Fatal("approving inside an ungranted plugin must be refused")
	}
}
