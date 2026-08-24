package operations

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/auth"
)

// --- rule resolution -------------------------------------------------------

func rule(id, plugin, action, principal string, max RiskLevel) AutoApprovalRule {
	return AutoApprovalRule{
		ID: id, Plugin: plugin, Action: action, Principal: principal, MaxRisk: max,
	}
}

func request(risk RiskLevel) AutoApprovalRequest {
	return AutoApprovalRequest{
		Plugin: "cnmaestro", Action: "device.set_radio_channel",
		Principal: "user:alice", Risk: risk, Reversible: true,
	}
}

// The default is to ask. A deployment that upgrades into this feature and
// configures nothing must behave exactly as it did before, because a policy
// that silently loosens on upgrade is the wrong failure direction whatever
// else it gets right.
func TestAutoApprovalPolicy_AsksAboutEverythingByDefault(t *testing.T) {
	var unconfigured AutoApprovalPolicy

	for _, risk := range []RiskLevel{RiskLow, RiskMedium, RiskHigh, RiskCritical} {
		d := unconfigured.Evaluate(request(risk))
		if d.AutoApprove {
			t.Errorf("%s risk auto-approved with no rules configured", risk)
		}
		if d.Rule != nil {
			t.Errorf("%s risk named a rule that does not exist: %v", risk, d.Rule)
		}
		if !strings.Contains(d.Reason, "no rule covers") {
			t.Errorf("%s risk: reason %q does not say why", risk, d.Reason)
		}
	}
}

func TestAutoApprovalPolicy_Evaluate(t *testing.T) {
	tests := []struct {
		name     string
		rules    []AutoApprovalRule
		req      AutoApprovalRequest
		want     bool
		wantRule string
		reason   string
		// raw skips NormaliseRules, so a rule set that could only be built in
		// process -- never stored -- is judged by Evaluate on its own.
		raw bool
	}{
		{
			name:     "a matching ceiling authorises",
			rules:    []AutoApprovalRule{rule("routine", "cnmaestro", RuleAny, RuleAny, RiskLow)},
			req:      request(RiskLow),
			want:     true,
			wantRule: "routine",
		},
		{
			name:     "a change above the ceiling is asked about",
			rules:    []AutoApprovalRule{rule("routine", "cnmaestro", RuleAny, RuleAny, RiskLow)},
			req:      request(RiskMedium),
			want:     false,
			wantRule: "routine",
			reason:   "authorises up to low",
		},
		{
			name:  "another plugin's rule does not reach this one",
			rules: []AutoApprovalRule{rule("elsewhere", "echo", RuleAny, RuleAny, RiskHigh)},
			req:   request(RiskLow),
			want:  false,
		},
		{
			name:  "another principal's rule does not reach this one",
			rules: []AutoApprovalRule{rule("robot", RuleAny, RuleAny, "svc:ci", RiskHigh)},
			req:   request(RiskLow),
			want:  false,
		},
		{
			name:     "a wildcard rule reaches everything",
			rules:    []AutoApprovalRule{rule("everything", RuleAny, RuleAny, RuleAny, RiskMedium)},
			req:      request(RiskMedium),
			want:     true,
			wantRule: "everything",
		},
		{
			name: "an action carve-out beats the plugin-wide rule that would allow it",
			rules: []AutoApprovalRule{
				rule("plugin-wide", "cnmaestro", RuleAny, RuleAny, RiskHigh),
				rule("never-reboot", "cnmaestro", "device.reboot", RuleAny, ""),
			},
			req: AutoApprovalRequest{
				Plugin: "cnmaestro", Action: "device.reboot",
				Principal: "user:alice", Risk: RiskLow, Reversible: true,
			},
			want:     false,
			wantRule: "never-reboot",
			reason:   "excludes this from automatic authorisation",
		},
		{
			name: "an action carve-out beats a broad grant to the principal",
			rules: []AutoApprovalRule{
				rule("trusted-robot", RuleAny, RuleAny, "svc:ci", RiskHigh),
				rule("never-reboot", "cnmaestro", "device.reboot", RuleAny, ""),
			},
			req: AutoApprovalRequest{
				Plugin: "cnmaestro", Action: "device.reboot",
				Principal: "svc:ci", Risk: RiskLow, Reversible: true,
			},
			want:     false,
			wantRule: "never-reboot",
		},
		{
			// The specificity score says the grant wins: the exclusion is
			// {plugin:*, action:device.reboot} at 2, the grant is
			// {plugin:cnmaestro, action:*} at 4. It auto-approved a device
			// reboot, which is the failure the whole feature has to not have.
			name: "an exclusion scoped more loosely than the grant still wins",
			rules: []AutoApprovalRule{
				rule("plugin-wide", "cnmaestro", RuleAny, RuleAny, RiskHigh),
				rule("never-reboot", RuleAny, "device.reboot", RuleAny, ""),
			},
			req: AutoApprovalRequest{
				Plugin: "cnmaestro", Action: "device.reboot",
				Principal: "user:alice", Risk: RiskLow, Reversible: true,
			},
			want:     false,
			wantRule: "never-reboot",
			reason:   "excludes this from automatic authorisation",
		},
		{
			// The same failure spelled the other way: an operator excluding
			// one service token, scoring 1 against the same grant's 4.
			name: "an excluded principal stays excluded under a broader grant",
			rules: []AutoApprovalRule{
				rule("plugin-wide", "cnmaestro", RuleAny, RuleAny, RiskHigh),
				rule("never-bot", RuleAny, RuleAny, "svc:bot", ""),
			},
			req: AutoApprovalRequest{
				Plugin: "cnmaestro", Action: "device.set_radio_channel",
				Principal: "svc:bot", Risk: RiskLow, Reversible: true,
			},
			want:     false,
			wantRule: "never-bot",
		},
		{
			// Deny-wins must not become deny-everywhere. An exclusion is
			// still scoped, and one written for a different plugin has
			// nothing to say about this change.
			name: "an exclusion scoped elsewhere does not leak into this grant",
			rules: []AutoApprovalRule{
				rule("plugin-wide", "cnmaestro", RuleAny, RuleAny, RiskHigh),
				rule("never-reboot", "echo", "device.reboot", RuleAny, ""),
			},
			req: AutoApprovalRequest{
				Plugin: "cnmaestro", Action: "device.reboot",
				Principal: "user:alice", Risk: RiskLow, Reversible: true,
			},
			want:     true,
			wantRule: "plugin-wide",
		},
		{
			// The same, one dimension over: an exclusion naming another
			// principal leaves this one's grant alone.
			name: "an exclusion for another principal does not reach this one",
			rules: []AutoApprovalRule{
				rule("plugin-wide", "cnmaestro", RuleAny, RuleAny, RiskHigh),
				rule("never-bot", RuleAny, RuleAny, "svc:bot", ""),
			},
			req:      request(RiskLow),
			want:     true,
			wantRule: "plugin-wide",
		},
		{
			// Specificity still orders two grants against each other, which is
			// the job it is good at and the only one it now has.
			name: "the more specific of two grants decides",
			rules: []AutoApprovalRule{
				rule("anyone", "cnmaestro", "device.set_radio_channel", RuleAny, RiskLow),
				rule("alice", "cnmaestro", "device.set_radio_channel", "user:alice", RiskMedium),
			},
			req:      request(RiskMedium),
			want:     true,
			wantRule: "alice",
		},
		{
			// The cost of deny-wins, pinned so it is a decision rather than a
			// surprise: an exclusion cannot be granted an exception. An
			// operator wanting "only alice" writes the narrow grant and no
			// exclusion, because the absence of a grant already means ask.
			name: "a grant cannot carve an exception out of an exclusion",
			rules: []AutoApprovalRule{
				rule("nobody", "cnmaestro", "device.set_radio_channel", RuleAny, ""),
				rule("alice", "cnmaestro", "device.set_radio_channel", "user:alice", RiskMedium),
			},
			req:      request(RiskMedium),
			want:     false,
			wantRule: "nobody",
		},
		{
			// A ceiling no rule may carry cannot be smuggled past Evaluate by
			// constructing the policy directly. NormaliseRules refuses it, and
			// so does the function that decides whether a human is skipped.
			name: "a critical ceiling authorises nothing even unvalidated",
			rules: []AutoApprovalRule{
				rule("bold", RuleAny, RuleAny, RuleAny, RiskCritical),
			},
			req:      request(RiskLow),
			want:     false,
			wantRule: "bold",
			reason:   "which no rule may",
			raw:      true,
		},
		{
			name: "an unrecognised ceiling authorises nothing even unvalidated",
			rules: []AutoApprovalRule{
				rule("odd", RuleAny, RuleAny, RuleAny, "spicy"),
			},
			req:      request(RiskLow),
			want:     false,
			wantRule: "odd",
			reason:   "is not a risk level",
			raw:      true,
		},
		{
			name:  "an irreversible change is never authorised, whatever the rule says",
			rules: []AutoApprovalRule{rule("everything", RuleAny, RuleAny, RuleAny, RiskHigh)},
			req: AutoApprovalRequest{
				Plugin: "cnmaestro", Action: "device.factory_reset",
				Principal: "user:alice", Risk: RiskLow, Reversible: false,
			},
			want:   false,
			reason: "no way back",
		},
		{
			name:  "an unrecognised risk is never authorised",
			rules: []AutoApprovalRule{rule("everything", RuleAny, RuleAny, RuleAny, RiskHigh)},
			req: AutoApprovalRequest{
				Plugin: "cnmaestro", Action: "device.set_radio_channel",
				Principal: "user:alice", Risk: "spicy", Reversible: true,
			},
			want:   false,
			reason: "not a classification",
		},
		{
			name:  "an unclassified change is never authorised",
			rules: []AutoApprovalRule{rule("everything", RuleAny, RuleAny, RuleAny, RiskHigh)},
			req: AutoApprovalRequest{
				Plugin: "cnmaestro", Action: "device.set_radio_channel",
				Principal: "user:alice", Risk: "", Reversible: true,
			},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rules := tc.rules
			if !tc.raw {
				var err error
				if rules, err = NormaliseRules(tc.rules); err != nil {
					t.Fatalf("rules should be valid: %v", err)
				}
			}
			d := AutoApprovalPolicy{Rules: rules}.Evaluate(tc.req)
			if d.AutoApprove != tc.want {
				t.Errorf("auto_approve = %v, want %v (%s)", d.AutoApprove, tc.want, d.Reason)
			}
			if d.RuleID() != tc.wantRule {
				t.Errorf("rule = %q, want %q", d.RuleID(), tc.wantRule)
			}
			if tc.reason != "" && !strings.Contains(d.Reason, tc.reason) {
				t.Errorf("reason %q does not mention %q", d.Reason, tc.reason)
			}
		})
	}
}

// Resolution must not depend on the order rules happen to be stored in. An
// operator answering "which rule applied" from the configuration alone has to
// get the same answer the host does.
func TestAutoApprovalPolicy_ResolutionIsOrderIndependent(t *testing.T) {
	rules := []AutoApprovalRule{
		rule("everything", RuleAny, RuleAny, RuleAny, RiskHigh),
		rule("plugin-wide", "cnmaestro", RuleAny, RuleAny, RiskMedium),
		rule("this-action", "cnmaestro", "device.set_radio_channel", RuleAny, RiskLow),
		rule("this-person", RuleAny, RuleAny, "user:alice", RiskHigh),
	}
	orders := [][]AutoApprovalRule{
		{rules[0], rules[1], rules[2], rules[3]},
		{rules[3], rules[2], rules[1], rules[0]},
		{rules[2], rules[0], rules[3], rules[1]},
	}
	for i, order := range orders {
		normalised, err := NormaliseRules(order)
		if err != nil {
			t.Fatalf("order %d: %v", i, err)
		}
		d := AutoApprovalPolicy{Rules: normalised}.Evaluate(request(RiskLow))
		if d.RuleID() != "this-action" {
			t.Errorf("order %d chose %q, want the most specific rule", i, d.RuleID())
		}
	}
}

// An exclusion wins from anywhere in the set, including from the position a
// specificity-ordered resolution would have visited last.
func TestAutoApprovalPolicy_AnExclusionWinsFromAnyPosition(t *testing.T) {
	grant := rule("plugin-wide", "cnmaestro", RuleAny, RuleAny, RiskHigh)
	broad := rule("everything", RuleAny, RuleAny, RuleAny, RiskHigh)
	deny := rule("never-reboot", RuleAny, "device.reboot", RuleAny, "")

	orders := [][]AutoApprovalRule{
		{deny, grant, broad},
		{grant, broad, deny},
		{broad, deny, grant},
	}
	req := AutoApprovalRequest{
		Plugin: "cnmaestro", Action: "device.reboot",
		Principal: "user:alice", Risk: RiskLow, Reversible: true,
	}
	for i, order := range orders {
		normalised, err := NormaliseRules(order)
		if err != nil {
			t.Fatalf("order %d: %v", i, err)
		}
		d := AutoApprovalPolicy{Rules: normalised}.Evaluate(req)
		if d.AutoApprove {
			t.Errorf("order %d auto-approved a reboot under rule %q", i, d.RuleID())
		}
		if d.RuleID() != "never-reboot" {
			t.Errorf("order %d blamed %q, want never-reboot", i, d.RuleID())
		}
	}
}

func TestNormaliseRules(t *testing.T) {
	tests := []struct {
		name    string
		rules   []AutoApprovalRule
		wantErr string
	}{
		{
			name:    "a rule needs an id",
			rules:   []AutoApprovalRule{{Plugin: "cnmaestro", MaxRisk: RiskLow}},
			wantErr: "rule id",
		},
		{
			name: "two rules cannot share an id",
			rules: []AutoApprovalRule{
				rule("same", "cnmaestro", RuleAny, RuleAny, RiskLow),
				rule("same", "echo", RuleAny, RuleAny, RiskLow),
			},
			wantErr: "both called",
		},
		{
			name: "two rules cannot share a scope",
			rules: []AutoApprovalRule{
				rule("first", "cnmaestro", RuleAny, RuleAny, RiskLow),
				rule("second", "cnmaestro", RuleAny, RuleAny, RiskHigh),
			},
			wantErr: "one scope, one rule",
		},
		{
			name:    "a rule cannot authorise a critical change",
			rules:   []AutoApprovalRule{rule("bold", RuleAny, RuleAny, RuleAny, RiskCritical)},
			wantErr: "a person has to see",
		},
		{
			name:    "an unknown ceiling is refused",
			rules:   []AutoApprovalRule{rule("odd", RuleAny, RuleAny, RuleAny, "spicy")},
			wantErr: "not a risk level",
		},
		{
			name: "a note cannot break a log line",
			rules: []AutoApprovalRule{{
				ID: "chatty", Plugin: RuleAny, Action: RuleAny, Principal: RuleAny,
				MaxRisk: RiskLow, Note: "fine\nDELETED EVERYTHING",
			}},
			wantErr: "control characters",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NormaliseRules(tc.rules)
			if err == nil {
				t.Fatalf("want an error mentioning %q, got none", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

// An omitted selector means "anything", and is stored that way so a rule read
// back says what it does.
func TestNormaliseRules_FillsInTheWildcard(t *testing.T) {
	out, err := NormaliseRules([]AutoApprovalRule{{ID: "bare", MaxRisk: RiskLow}})
	if err != nil {
		t.Fatal(err)
	}
	got := out[0]
	if got.Plugin != RuleAny || got.Action != RuleAny || got.Principal != RuleAny {
		t.Fatalf("selectors = %s, want all wildcards", got.Scope())
	}
}

// --- the service ----------------------------------------------------------

func testService(t *testing.T, repo Repository, policy Policy) *Service {
	t.Helper()
	if policy.ProposalTTL == 0 {
		policy.ProposalTTL = 30 * time.Minute
	}
	if policy.ApprovalTTL == 0 {
		policy.ApprovalTTL = 15 * time.Minute
	}
	now := func() time.Time { return base }
	return NewService(repo, NewApprovalPolicy(auth.NewAuthorizer()),
		func() Policy { return policy },
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		now, NewULIDGenerator(now), nil)
}

func proposal(risk RiskLevel, reversible bool) ProposeRequest {
	return ProposeRequest{
		Plugin: "cnmaestro", Action: "device.set_radio_channel", Risk: risk,
		Target:        json.RawMessage(`{"mac":"AA:BB"}`),
		Params:        json.RawMessage(`{"channel":"149"}`),
		Preconditions: json.RawMessage(`{"channel":"36"}`),
		Desired:       json.RawMessage(`{"channel":"149"}`),
		Verifiable:    true,
		Reversible:    reversible,
	}
}

func proposer() *auth.Principal {
	return &auth.Principal{
		ID: "user:alice", Role: auth.RoleUser, Plugins: []string{"cnmaestro"},
	}
}

func rulesPolicy(t *testing.T, rules ...AutoApprovalRule) Policy {
	t.Helper()
	normalised, err := NormaliseRules(rules)
	if err != nil {
		t.Fatalf("rules should be valid: %v", err)
	}
	return Policy{AutoApprove: AutoApprovalPolicy{Rules: normalised}}
}

// Nothing changes for a deployment that has configured nothing.
func TestPropose_WithNoRulesEveryMutationStillAsks(t *testing.T) {
	repo := newMemRepo()
	svc := testService(t, repo, Policy{})

	for _, risk := range []RiskLevel{RiskLow, RiskMedium, RiskHigh} {
		op, err := svc.Propose(context.Background(), proposer(), proposal(risk, true))
		if err != nil {
			t.Fatalf("%s: %v", risk, err)
		}
		if op.State != StatePendingApproval {
			t.Errorf("%s risk landed in %s, want pending_approval", risk, op.State)
		}
		if op.AutoApproved() {
			t.Errorf("%s risk was authorised by rule %q with no rules configured",
				risk, op.AuthorizedByRule)
		}
	}
}

// The whole feature, end to end: a covered change is approved without anyone
// being asked, the operation exists as an ordinary operation, and the audit
// entry says which rule authorised it.
func TestPropose_ALowRiskChangeUnderARuleIsAuthorisedWithoutAsking(t *testing.T) {
	repo := newMemRepo()
	svc := testService(t, repo, rulesPolicy(t,
		rule("routine-radio", "cnmaestro", RuleAny, RuleAny, RiskLow)))

	op, err := svc.Propose(context.Background(), proposer(), proposal(RiskLow, true))
	if err != nil {
		t.Fatal(err)
	}

	if op.State != StateApproved {
		t.Fatalf("state = %s, want approved", op.State)
	}
	if op.AuthorizedByRule != "routine-radio" {
		t.Errorf("authorized_by_rule = %q, want routine-radio", op.AuthorizedByRule)
	}
	if op.ApprovedBy != PolicyActor {
		t.Errorf("approved_by = %q; a rule approved this, not a person", op.ApprovedBy)
	}
	if op.ApprovalExpiresAt == nil {
		t.Error("an authorised operation still needs an execute-by deadline")
	}
	// It is a real operation: the payload is frozen and hashed like any other,
	// so the executor's claim can still refuse a tampered row.
	if op.PayloadHash == "" {
		t.Error("an authorised operation must still carry a frozen payload hash")
	}

	// And the trail says what authorised it. An entry saying "auto-approved"
	// without naming the rule is the unprovable approval the gate exists to
	// prevent.
	entry := auditOfKind(t, repo, "operation.approved")
	if entry.Actor != PolicyActor {
		t.Errorf("audit actor = %q, want %q", entry.Actor, PolicyActor)
	}
	detail := decodeDetail(t, entry)
	for key, want := range map[string]any{
		"rule":       "routine-radio",
		"channel":    "policy",
		"rule_scope": "cnmaestro/* for *",
	} {
		if got := detail[key]; got != want {
			t.Errorf("audit detail %s = %v, want %v", key, got, want)
		}
	}
}

// Assurance is orthogonal. "Nobody was asked" and "nothing can be proved" are
// different facts, and an auto-approved change that can prove itself is still
// a reviewed change.
func TestPropose_AuthorisingByRuleDoesNotChangeAssurance(t *testing.T) {
	repo := newMemRepo()
	svc := testService(t, repo, rulesPolicy(t,
		rule("routine", RuleAny, RuleAny, RuleAny, RiskLow)))

	verifiable, err := svc.Propose(context.Background(), proposer(), proposal(RiskLow, true))
	if err != nil {
		t.Fatal(err)
	}
	if !verifiable.AutoApproved() {
		t.Fatal("this change should have been authorised by the rule")
	}
	if got := verifiable.Assurance(); got != AssuranceReviewedChange {
		t.Errorf("assurance = %s, want reviewed_change", got)
	}

	req := proposal(RiskLow, true)
	req.Verifiable = false
	req.IdempotencyKey = "second"
	gated, err := svc.Propose(context.Background(), proposer(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !gated.AutoApproved() {
		t.Fatal("this change should have been authorised too")
	}
	if got := gated.Assurance(); got != AssuranceGatedCall {
		t.Errorf("assurance = %s, want gated_call", got)
	}
}

// The obvious hole. A mutation whose declared risk qualifies, whose plan
// reclassifies it upward for these specific parameters, must go back to a
// person -- the ceiling is compared against the risk as it finally stands.
func TestPropose_APlanThatRaisesRiskPastTheCeilingIsAskedAbout(t *testing.T) {
	repo := newMemRepo()
	svc := testService(t, repo, rulesPolicy(t,
		rule("routine", "cnmaestro", RuleAny, RuleAny, RiskLow)))

	// What the registry hands over: MaxRisk of the spec's declaration and the
	// plan's override, computed before Propose is called.
	req := proposal(MaxRisk(RiskLow, RiskHigh), true)
	op, err := svc.Propose(context.Background(), proposer(), req)
	if err != nil {
		t.Fatal(err)
	}
	if op.State != StatePendingApproval {
		t.Fatalf("state = %s, want pending_approval", op.State)
	}
	if op.AutoApproved() {
		t.Fatalf("a change the plan raised to %s was authorised by rule %q",
			op.Risk, op.AuthorizedByRule)
	}

	// The same proposal without the plan's override does qualify, which is what
	// makes the case above about the raise rather than about the rule.
	baseline := proposal(RiskLow, true)
	baseline.IdempotencyKey = "baseline"
	unraised, err := svc.Propose(context.Background(), proposer(), baseline)
	if err != nil {
		t.Fatal(err)
	}
	if !unraised.AutoApproved() {
		t.Fatal("without the raise this change is covered by the rule")
	}
}

// An operator's own override raises risk the same way, and the policy sees the
// raised value rather than what the plugin declared.
func TestPropose_AnOperatorRiskOverrideAlsoPutsTheChangeBack(t *testing.T) {
	repo := newMemRepo()
	policy := rulesPolicy(t, rule("routine", "cnmaestro", RuleAny, RuleAny, RiskLow))
	policy.RiskOverrides = map[string]RiskLevel{
		"cnmaestro.device.set_radio_channel": RiskHigh,
	}
	svc := testService(t, repo, policy)

	op, err := svc.Propose(context.Background(), proposer(), proposal(RiskLow, true))
	if err != nil {
		t.Fatal(err)
	}
	if op.AutoApproved() {
		t.Fatalf("an overridden %s change was authorised by rule %q", op.Risk, op.AuthorizedByRule)
	}
}

// A change nothing can undo is never authorised in advance, whatever the rule
// says. The argument for a standing authorisation is that a mistake is cheap
// to correct.
func TestPropose_AnIrreversibleMutationIsNeverAuthorisedByRule(t *testing.T) {
	repo := newMemRepo()
	svc := testService(t, repo, rulesPolicy(t,
		rule("everything", RuleAny, RuleAny, RuleAny, RiskHigh)))

	op, err := svc.Propose(context.Background(), proposer(), proposal(RiskLow, false))
	if err != nil {
		t.Fatal(err)
	}
	if op.AutoApproved() {
		t.Fatalf("an irreversible change was authorised by rule %q", op.AuthorizedByRule)
	}
	if op.State != StatePendingApproval {
		t.Fatalf("state = %s, want pending_approval", op.State)
	}
}

// A rule decides who authorises, not what may be authorised. Every guard an
// approval passes still applies, so an expired proposal stays where it is
// rather than being swept into approved by a rule that covers it.
func TestAutoApprove_DoesNotBypassTheApprovalGuards(t *testing.T) {
	repo := newMemRepo()
	svc := testService(t, repo, rulesPolicy(t,
		rule("routine", RuleAny, RuleAny, RuleAny, RiskLow)))

	expired := &Operation{
		ID: "op_expired", Plugin: "cnmaestro", Action: "device.set_radio_channel",
		State: StatePendingApproval, Risk: RiskLow,
		PayloadHash: "hash", RequestedBy: "user:alice",
		RequestedAt: base.Add(-time.Hour), ExpiresAt: base.Add(-time.Minute),
	}
	repo.put(expired)

	got := svc.autoApprove(context.Background(), expired, true)
	if got.State != StatePendingApproval {
		t.Fatalf("state = %s, want pending_approval", got.State)
	}
	if got.AutoApproved() {
		t.Fatalf("an expired proposal was authorised by rule %q", got.AuthorizedByRule)
	}
}

// A replayed proposal returns the operation that already exists, which may
// have moved on. Deciding again about a change that is already approved,
// running or settled would be deciding about the past.
func TestAutoApprove_LeavesAnOperationThatHasMovedOn(t *testing.T) {
	repo := newMemRepo()
	svc := testService(t, repo, rulesPolicy(t,
		rule("routine", RuleAny, RuleAny, RuleAny, RiskLow)))

	settled := &Operation{
		ID: "op_done", Plugin: "cnmaestro", Action: "device.set_radio_channel",
		State: StateSucceeded, Risk: RiskLow, RequestedBy: "user:alice",
		RequestedAt: base, ExpiresAt: base.Add(time.Hour),
	}
	repo.put(settled)

	if got := svc.autoApprove(context.Background(), settled, true); got.State != StateSucceeded {
		t.Fatalf("state = %s, want succeeded", got.State)
	}
	if len(repo.audit) != 0 {
		t.Fatalf("a settled operation produced %d audit entries", len(repo.audit))
	}
}

func auditOfKind(t *testing.T, repo *memRepo, kind string) AuditEntry {
	t.Helper()
	repo.mu.Lock()
	defer repo.mu.Unlock()
	for _, e := range repo.audit {
		if e.Kind == kind {
			return e
		}
	}
	t.Fatalf("no %s audit entry was written", kind)
	return AuditEntry{}
}

func decodeDetail(t *testing.T, e AuditEntry) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(e.Detail, &out); err != nil {
		t.Fatalf("audit detail is not readable: %v", err)
	}
	return out
}

// A misspelled selector must be an error, never a silently wider rule.
//
// {"principle": "svc:agent"} was accepted, discarded, and the real principal
// defaulted to every principal -- so an operator writing a rule for one
// service token got one covering everybody, with nothing saying so.
func TestDecodeRules_RefusesAMisspelledSelector(t *testing.T) {
	if _, err := DecodeRules([]byte(
		`[{"id":"x","principle":"svc:agent","max_risk":"low"}]`)); err == nil {
		t.Fatal("a misspelled selector must be refused, not widened to every principal")
	} else if !strings.Contains(err.Error(), "principle") {
		t.Errorf("the error must name the field: %v", err)
	}

	// The correct spelling still works, and still means what it says.
	rules, err := DecodeRules([]byte(`[{"id":"x","principal":"svc:agent","max_risk":"low"}]`))
	if err != nil {
		t.Fatalf("the correct spelling must be accepted: %v", err)
	}
	if rules[0].Principal != "svc:agent" {
		t.Errorf("principal = %q, want svc:agent", rules[0].Principal)
	}
	if rules[0].Plugin != RuleAny {
		t.Errorf("an omitted plugin should be the wildcard, got %q", rules[0].Plugin)
	}
}

// An explicit null widens exactly the way a typo does, and decoding into a
// plain struct cannot tell it from an absent field.
func TestDecodeRules_RefusesAnExplicitNull(t *testing.T) {
	for _, body := range []string{
		`[{"id":"x","plugin":null,"max_risk":"low"}]`,
		`[{"id":"x","principal":null,"max_risk":"low"}]`,
		`[{"id":"x","max_risk":null}]`,
		`[null]`,
	} {
		if _, err := DecodeRules([]byte(body)); err == nil {
			t.Errorf("%s was accepted; null is not a value", body)
		}
	}
}

// An empty selector is not a third spelling of the wildcard.
func TestDecodeRules_RefusesAnEmptySelector(t *testing.T) {
	if _, err := DecodeRules([]byte(`[{"id":"x","plugin":"","max_risk":"low"}]`)); err == nil {
		t.Fatal(`an empty plugin must be refused; "*" is how a rule says any`)
	}
}

// Strictness must not cost the ordinary spellings. An absent selector is the
// wildcard, and that is the whole convenience the strictness protects.
func TestDecodeRules_AcceptsTheOrdinarySpellings(t *testing.T) {
	rules, err := DecodeRules([]byte(
		`[{"id":"routine","plugin":"cnmaestro","max_risk":"low","note":"fine"},
		  {"id":"never-reboot","plugin":"cnmaestro","action":"device.reboot","max_risk":""}]`))
	if err != nil {
		t.Fatalf("valid rules were refused: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("decoded %d rules, want 2", len(rules))
	}
	// Sorted most specific first, which is the order resolution considers.
	if rules[0].ID != "never-reboot" {
		t.Errorf("first rule = %q, want the more specific one", rules[0].ID)
	}
}

// An empty or absent stored value is no rules, not an error. A host that has
// never been configured must not log a failure every time it proposes.
func TestDecodeRules_TreatsAnEmptyValueAsNoRules(t *testing.T) {
	for _, body := range []string{"", "  ", "[]"} {
		rules, err := DecodeRules([]byte(body))
		if err != nil {
			t.Errorf("%q: %v", body, err)
		}
		if len(rules) != 0 {
			t.Errorf("%q gave %d rules, want none", body, len(rules))
		}
	}
}

// What a change meets when no rule covers it.
//
// This is the setting that decides whether an assistant's write runs or waits,
// and it is the lowest-precedence thing in the model: an exclusion still wins
// outright, a matching grant still decides, and a change that cannot be undone
// is still held whatever it says.
func TestEvaluate_WhenNoRuleCovers(t *testing.T) {
	change := AutoApprovalRequest{
		Plugin: "echo", Action: "set_label", Principal: "svc:chatgpt",
		Risk: RiskMedium, Reversible: true,
	}

	for _, tc := range []struct {
		name      string
		unmatched RiskLevel
		policy    []AutoApprovalRule
		want      bool
	}{
		{name: "nothing set asks, as it always did", want: false},
		{name: "a ceiling below the change asks", unmatched: RiskLow, want: false},
		{name: "a ceiling at the change authorises", unmatched: RiskMedium, want: true},
		{name: "a ceiling above the change authorises", unmatched: RiskHigh, want: true},
		{
			// The whole point of keeping exclusions above this: a carve-out
			// written for one dangerous action has to keep working when the
			// default is opened up, or opening it up quietly deletes it.
			name:      "an exclusion beats the default outright",
			unmatched: RiskHigh,
			policy: []AutoApprovalRule{{
				ID: "never-relabel", Plugin: "echo", Action: "set_label",
				Principal: RuleAny, MaxRisk: "",
			}},
			want: false,
		},
		{
			// A grant is more specific than "everything else", so its ceiling
			// is the one that applies -- including when it is lower.
			name:      "a matching grant decides instead",
			unmatched: RiskHigh,
			policy: []AutoApprovalRule{{
				ID: "reads-only", Plugin: "echo", Action: "set_label",
				Principal: RuleAny, MaxRisk: RiskLow,
			}},
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := AutoApprovalPolicy{Rules: tc.policy, Unmatched: tc.unmatched}
			got := p.Evaluate(change)
			if got.AutoApprove != tc.want {
				t.Errorf("AutoApprove = %v, want %v (%s)", got.AutoApprove, tc.want, got.Reason)
			}
			if got.Reason == "" {
				t.Error("a decision with no reason cannot be explained to anybody")
			}
		})
	}
}

// A change with no way back is put to a person however open the default is.
// That floor is a property of the change, not of the configuration.
func TestEvaluate_TheDefaultDoesNotReachAnIrreversibleChange(t *testing.T) {
	p := AutoApprovalPolicy{Unmatched: RiskHigh}
	got := p.Evaluate(AutoApprovalRequest{
		Plugin: "echo", Action: "delete", Principal: "svc:chatgpt",
		Risk: RiskLow, Reversible: false,
	})
	if got.AutoApprove {
		t.Error("a change that cannot be undone was authorised in advance")
	}
}

// "No rule authorised this" and "the host's own default did" are different
// answers to why a change ran unasked, and the trail has to tell them apart.
func TestAuthority_NamesTheDefaultWhenNoRuleDecided(t *testing.T) {
	p := AutoApprovalPolicy{Unmatched: RiskHigh}
	got := p.Evaluate(AutoApprovalRequest{
		Plugin: "echo", Action: "set_label", Principal: "svc:chatgpt",
		Risk: RiskLow, Reversible: true,
	})
	if !got.AutoApprove {
		t.Fatalf("expected the default to authorise: %s", got.Reason)
	}
	if got.RuleID() != "" {
		t.Errorf("RuleID = %q; no rule decided this", got.RuleID())
	}
	if got.Authority() != DefaultAuthority {
		t.Errorf("Authority = %q, want %q", got.Authority(), DefaultAuthority)
	}
}
