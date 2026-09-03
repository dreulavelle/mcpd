package operations

import (
	"testing"

	"github.com/spoked/mcpd/internal/auth"
)

func policy() *ApprovalPolicy {
	return NewApprovalPolicy(auth.NewAuthorizer())
}

// principal builds a caller holding a built-in role and write on cnmaestro,
// which is the shape every approval check here is about: a role that decides
// what may be done, and a grant that decides what may be reached.
func principal(id, roleID string) *auth.Principal {
	r, ok := auth.BuiltinRole(roleID)
	if !ok {
		panic("no such built-in role: " + roleID)
	}
	return &auth.Principal{
		ID: id, RoleID: r.ID, RoleName: r.Name, Permissions: r.Permissions,
		Grants: auth.GrantsAt([]string{"cnmaestro"}, auth.LevelWrite),
	}
}

func opWithRisk(risk RiskLevel) *Operation {
	return &Operation{Plugin: "cnmaestro", Risk: risk, RequestedBy: "user:alice"}
}

// Approval happens in the conversation with whoever asked for the change, so
// there is no second-person rule: a proposal only the requester can see is not
// one a colleague could act on. What remains is the permission, which still
// bites.
//
// Every role that holds approvals:decide approves at every risk. That is the
// point of not splitting the decision by risk: a deployment where only
// administrators could approve would send every routine change to the one
// person holding the settings, and a gate that inconvenient is one people
// route around.
func TestAuthorizeApproval_RequiresApprovalsDecide(t *testing.T) {
	p := policy()

	for _, roleID := range []string{auth.RoleOperator, auth.RoleAdministrator} {
		actor := principal("user:alice", roleID)
		for _, risk := range []RiskLevel{RiskLow, RiskMedium, RiskHigh, RiskCritical} {
			if d := p.AuthorizeApproval(actor, opWithRisk(risk)); !d.Allowed {
				t.Fatalf("%s at %s risk was refused: %s", actor.RoleName, risk, d.Reason)
			}
		}
	}

	// A principal carrying no role at all holds nothing, which is what an
	// unauthenticated caller looks like by the time it reaches here.
	if d := p.AuthorizeApproval(auth.Anonymous(), opWithRisk(RiskLow)); d.Allowed {
		t.Fatal("an anonymous caller must not be able to approve")
	}
}

// Seeing the queue and settling something in it are two levels of one area,
// and the split is the whole reason approvals use decide rather than write.
// A Reader holds approvals:read and reaches the plugin at write, so nothing
// but the missing level can be what refuses it.
func TestAuthorizeApproval_ReadingTheQueueIsNotDecidingOnIt(t *testing.T) {
	reader := principal("user:auditor", auth.RoleReader)
	if !reader.Can(auth.PermApprovalsRead) {
		t.Fatal("a Reader must be able to see the approvals queue")
	}
	if d := policy().AuthorizeApproval(reader, opWithRisk(RiskLow)); d.Allowed {
		t.Fatal("holding approvals:read but not decide must not settle an approval")
	}
}

// Reaching a plugin the principal was not granted is still refused, and it is
// the check that keeps a scoped credential scoped -- whatever its role says it
// may decide.
func TestAuthorizeApproval_RefusesAnUngrantedPlugin(t *testing.T) {
	elsewhere := principal("user:alice", auth.RoleOperator)
	elsewhere.Grants = auth.GrantsAt([]string{"echo"}, auth.LevelWrite)

	if d := policy().AuthorizeApproval(elsewhere, opWithRisk(RiskLow)); d.Allowed {
		t.Fatal("approving inside an ungranted plugin must be refused")
	}
}
