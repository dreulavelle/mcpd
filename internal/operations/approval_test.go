package operations

import (
	"testing"

	"github.com/spoked/mcpd/internal/auth"
)

func policy(threshold RiskLevel) *ApprovalPolicy {
	return NewApprovalPolicy(auth.NewAuthorizer(), RiskPolicy{
		RequireDistinctApproverAtOrAbove: threshold,
	})
}

func principal(id string, role auth.Role, distinguishable bool) *auth.Principal {
	return &auth.Principal{
		ID: id, Role: role,
		Plugins:         []string{"cnmaestro"},
		Distinguishable: distinguishable,
	}
}

func opWithRisk(risk RiskLevel) *Operation {
	return &Operation{Plugin: "cnmaestro", Risk: risk, RequestedBy: "user:alice"}
}

func TestAuthorizeApproval_SeparationOfDuties(t *testing.T) {
	p := policy(RiskHigh)

	alice := principal("user:alice", auth.RoleApprover, true)
	bob := principal("user:bob", auth.RoleApprover, true)
	shared := principal("svc:shared", auth.RoleApprover, false)

	tests := []struct {
		name    string
		p       *auth.Principal
		op      *Operation
		allowed bool
		code    string
	}{
		{"self-approval below threshold is fine", alice, opWithRisk(RiskMedium), true, ""},
		{"self-approval at threshold refused", alice, opWithRisk(RiskHigh), false, auth.CodeSelfApproval},
		{"self-approval above threshold refused", alice, opWithRisk(RiskCritical), false, auth.CodeSelfApproval},
		{"distinct approver permitted", bob, opWithRisk(RiskCritical), true, ""},
		{"indistinct identity fails closed", shared, opWithRisk(RiskHigh), false, auth.CodeIdentityIndistinct},
		{"indistinct identity fine below threshold", shared, opWithRisk(RiskLow), true, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := p.AuthorizeApproval(tc.p, tc.op)
			if d.Allowed != tc.allowed {
				t.Fatalf("Allowed = %v, want %v (reason: %s)", d.Allowed, tc.allowed, d.Reason)
			}
			if !tc.allowed && d.Code != tc.code {
				t.Fatalf("code = %s, want %s", d.Code, tc.code)
			}
		})
	}
}

func TestAuthorizeApproval_RequiresApproveCapability(t *testing.T) {
	p := policy("")
	operator := principal("user:o", auth.RoleOperator, true)

	if d := p.AuthorizeApproval(operator, opWithRisk(RiskLow)); d.Allowed {
		t.Fatal("an operator must not be able to approve")
	}
}

func TestAuthorizeApproval_RequiresPluginAccess(t *testing.T) {
	p := policy("")
	outsider := &auth.Principal{
		ID: "user:x", Role: auth.RoleApprover,
		Plugins: []string{"netbox"}, Distinguishable: true,
	}
	if d := p.AuthorizeApproval(outsider, opWithRisk(RiskLow)); d.Allowed {
		t.Fatal("approving an operation for an ungranted plugin must be refused")
	}
}

func TestRequiresDistinctApprover(t *testing.T) {
	tests := []struct {
		threshold RiskLevel
		risk      RiskLevel
		want      bool
	}{
		{RiskHigh, RiskLow, false},
		{RiskHigh, RiskMedium, false},
		{RiskHigh, RiskHigh, true},
		{RiskHigh, RiskCritical, true},
		{RiskCritical, RiskHigh, false},
		{RiskCritical, RiskCritical, true},
		// An unset threshold disables the rule entirely.
		{"", RiskCritical, false},
	}
	for _, tc := range tests {
		p := policy(tc.threshold)
		if got := p.RequiresDistinctApprover(tc.risk); got != tc.want {
			t.Errorf("threshold %q, risk %q: got %v, want %v",
				tc.threshold, tc.risk, got, tc.want)
		}
	}
}
