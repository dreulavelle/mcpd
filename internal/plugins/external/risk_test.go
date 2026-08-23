package external

import (
	"testing"

	"github.com/spoked/mcpd/internal/operations"
)

// An unrecognised risk override used to be dropped, which was the most
// dangerous reading of it available.
//
// The chain it opened: a plugin returns "catastrophic" -- a typo, or a level a
// newer plugin knows and this host does not -- the override vanishes, the
// mutation goes on looking like the low risk it declared statically, a low
// ceiling auto-approves it, the executor re-plans through this same code and
// drops the value a second time, so CodeRiskRaised never fires, and Apply
// runs. Two separate refusals defeated by one silent assignment.
func TestPlanFrom_AnUnrecognisedRiskOverrideSurvives(t *testing.T) {
	tests := []struct {
		name  string
		given string
		want  operations.RiskLevel
	}{
		{"a level this build does not define", "catastrophic", "catastrophic"},
		{"a level it does", "high", operations.RiskHigh},
		{"a typo", "hihg", "hihg"},
		{"nothing at all", "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := planFrom("thing", PlanResult{RiskOverride: tc.given})
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if plan.RiskOverride != nil {
					t.Fatalf("override = %q, want none", *plan.RiskOverride)
				}
				return
			}
			if plan.RiskOverride == nil {
				t.Fatalf("override was dropped; %q must reach the host to be refused", tc.given)
			}
			if *plan.RiskOverride != tc.want {
				t.Errorf("override = %q, want %q", *plan.RiskOverride, tc.want)
			}
		})
	}
}

// The value has to arrive somewhere that refuses it, and the two places that
// matter both rank an unknown level above everything they define.
func TestPlanFrom_AnUnrecognisedOverrideRaisesRiskAndBlocksAuthorisation(t *testing.T) {
	plan, err := planFrom("thing", PlanResult{RiskOverride: "catastrophic"})
	if err != nil {
		t.Fatal(err)
	}

	// Risk may be raised and never lowered, and an unknown level outranks
	// every known one -- so a mutation declaring itself low arrives high.
	raised := operations.MaxRisk(operations.RiskLow, *plan.RiskOverride)
	if raised != "catastrophic" {
		t.Fatalf("raised risk = %q; an unknown level must outrank a known one", raised)
	}

	// And nothing authorises it, however permissive the rule.
	rules, err := operations.NormaliseRules([]operations.AutoApprovalRule{{
		ID: "everything", Plugin: operations.RuleAny, Action: operations.RuleAny,
		Principal: operations.RuleAny, MaxRisk: operations.RiskHigh,
	}})
	if err != nil {
		t.Fatal(err)
	}
	d := operations.AutoApprovalPolicy{Rules: rules}.Evaluate(operations.AutoApprovalRequest{
		Plugin: "thing", Action: "thing.set", Principal: "user:alice",
		Risk: raised, Reversible: true,
	})
	if d.AutoApprove {
		t.Fatalf("a change classified %q was authorised by rule %q", raised, d.RuleID())
	}
}
