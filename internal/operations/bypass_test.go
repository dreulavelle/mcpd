package operations

import (
	"testing"
	"time"
)

var bypassNow = time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)

func openBypass(mut func(*Bypass)) *Bypass {
	b := &Bypass{
		ID: "byp_1", Ceiling: RiskMedium, CreatedBy: "user:someone",
		ExpiresAt: bypassNow.Add(time.Hour), Reason: "working through a change",
	}
	if mut != nil {
		mut(b)
	}
	return b
}

func bypassRequest(mut func(*AutoApprovalRequest)) AutoApprovalRequest {
	r := AutoApprovalRequest{
		Plugin: "graylog", Action: "stream.pause", Principal: "svc:chatgpt",
		Risk: RiskLow, Reversible: true,
	}
	if mut != nil {
		mut(&r)
	}
	return r
}

// The whole point: a change the rules would put to a person runs while the
// window is open.
func TestBypassAuthorisesWhatTheRulesDeclined(t *testing.T) {
	declined := AutoApprovalDecision{Reason: "no rule covers this change"}

	got := applyBypass(declined, openBypass(nil), bypassRequest(nil), bypassNow)
	if !got.AutoApprove {
		t.Fatalf("the bypass did not authorise: %s", got.Reason)
	}
	if got.Bypass == nil {
		t.Fatal("the decision does not record which bypass authorised it")
	}
	if got.Rule != nil {
		t.Error("a bypass recorded a rule as the authority; no rule matched")
	}
	if got.Authority() != "bypass:byp_1" {
		t.Errorf("authority = %q, want it distinguishable from a rule id", got.Authority())
	}
}

// The most important refusal. An exclusion is somebody writing "never" about a
// specific action, and an evening's convenience must not cancel it -- the
// carve-out written most deliberately would be the one most easily lost.
func TestBypassDoesNotOverrideAnExclusion(t *testing.T) {
	exclusion := &AutoApprovalRule{ID: "rule_never", Plugin: "graylog"}
	if !exclusion.authorisesNothing() {
		t.Fatal("the fixture is not an exclusion, so this proves nothing")
	}
	declined := AutoApprovalDecision{Rule: exclusion, Reason: "excluded"}

	got := applyBypass(declined, openBypass(nil), bypassRequest(nil), bypassNow)
	if got.AutoApprove {
		t.Fatal("a bypass overrode an explicit exclusion")
	}
	if got.Rule != exclusion {
		t.Error("the refusal no longer names the rule that made it")
	}
}

// A grant that simply did not reach far enough is not an exclusion, and a
// bypass may cover it.
func TestBypassCoversAGrantThatFellShort(t *testing.T) {
	grant := &AutoApprovalRule{ID: "rule_low", Plugin: "graylog", MaxRisk: RiskLow}
	declined := AutoApprovalDecision{Rule: grant, Reason: "authorises up to low"}

	got := applyBypass(declined, openBypass(nil), bypassRequest(func(r *AutoApprovalRequest) {
		r.Risk = RiskMedium
	}), bypassNow)
	if !got.AutoApprove {
		t.Fatalf("the bypass did not cover a short grant: %s", got.Reason)
	}
}

// Everything a rule cannot do, a bypass cannot do either.
func TestBypassRefusesWhatARuleWould(t *testing.T) {
	for _, tc := range []struct {
		name   string
		bypass *Bypass
		req    AutoApprovalRequest
	}{
		{
			"an irreversible mutation",
			openBypass(nil),
			bypassRequest(func(r *AutoApprovalRequest) { r.Reversible = false }),
		},
		{
			"a risk above the ceiling",
			openBypass(func(b *Bypass) { b.Ceiling = RiskLow }),
			bypassRequest(func(r *AutoApprovalRequest) { r.Risk = RiskHigh }),
		},
		{
			"a critical ceiling",
			openBypass(func(b *Bypass) { b.Ceiling = RiskCritical }),
			bypassRequest(func(r *AutoApprovalRequest) { r.Risk = RiskCritical }),
		},
		{
			"a risk this host does not recognise",
			openBypass(nil),
			bypassRequest(func(r *AutoApprovalRequest) { r.Risk = RiskLevel("catastrophic") }),
		},
		{
			"another plugin",
			openBypass(func(b *Bypass) { b.Plugin = "observium" }),
			bypassRequest(nil),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			covers, reason := tc.bypass.Covers(tc.req, bypassNow)
			if covers {
				t.Fatalf("the bypass authorised %s", tc.name)
			}
			if reason == "" {
				t.Error("the refusal says nothing about why")
			}
		})
	}
}

// The defining property. A window that has closed authorises nothing, and
// there is no value of the field that means "never closes".
func TestBypassExpires(t *testing.T) {
	b := openBypass(nil)

	if covers, _ := b.Covers(bypassRequest(nil), bypassNow.Add(59*time.Minute)); !covers {
		t.Error("the window refused a change while it was still open")
	}
	if covers, reason := b.Covers(bypassRequest(nil), bypassNow.Add(time.Hour)); covers {
		t.Fatalf("the window authorised a change at the moment it expired: %s", reason)
	}
	if covers, _ := b.Covers(bypassRequest(nil), bypassNow.Add(2*time.Hour)); covers {
		t.Fatal("an expired window still authorises")
	}
	if b.Remaining(bypassNow.Add(2*time.Hour)) != 0 {
		t.Error("an expired window reports time remaining")
	}
}

// A bypass must never make a change less likely to be approved than the rules
// alone would have.
func TestBypassNeverReversesAnApproval(t *testing.T) {
	approved := AutoApprovalDecision{
		AutoApprove: true,
		Rule:        &AutoApprovalRule{ID: "rule_ok", MaxRisk: RiskHigh},
		Reason:      "rule_ok authorises this",
	}
	// A window that covers nothing at all.
	got := applyBypass(approved, openBypass(func(b *Bypass) { b.Plugin = "elsewhere" }),
		bypassRequest(nil), bypassNow)

	if !got.AutoApprove {
		t.Fatal("a bypass turned an approval into a refusal")
	}
	if got.Authority() != "rule_ok" {
		t.Errorf("authority = %q; the rule that decided must still be named", got.Authority())
	}
}

func TestNoBypassChangesNothing(t *testing.T) {
	declined := AutoApprovalDecision{Reason: "no rule covers this change"}
	got := applyBypass(declined, nil, bypassRequest(nil), bypassNow)
	if got.AutoApprove {
		t.Fatal("a change was authorised with no bypass open")
	}
}
