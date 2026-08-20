package operations

import (
	"errors"
	"testing"
	"time"
)

var (
	base   = time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	future = base.Add(time.Hour)
	past   = base.Add(-time.Hour)
)

// newOp builds a minimally valid operation in the given state.
func newOp(state OperationState) *Operation {
	appr := future
	return &Operation{
		ID:                "op_test",
		Plugin:            "testplugin",
		Action:            "thing.do",
		State:             state,
		Risk:              RiskMedium,
		Target:            []byte(`{"id":"a"}`),
		Params:            []byte(`{"v":1}`),
		PayloadHash:       "hash",
		RequestedBy:       "user:alice",
		RequestedAt:       base,
		ExpiresAt:         future,
		ApprovalExpiresAt: &appr,
	}
}

// okGuard is a GuardContext that satisfies every guard, so tests can vary one
// field at a time.
func okGuard() GuardContext {
	return GuardContext{
		Now:                     base,
		Actor:                   "user:bob",
		RecomputedHash:          "hash",
		PreconditionsMatch:      true,
		IdentityDistinguishable: true,
	}
}

func TestValidate_LegalTransitions(t *testing.T) {
	tests := []struct {
		name    string
		from    OperationState
		to      OperationState
		trigger Trigger
		mutate  func(*Operation, *GuardContext)
	}{
		{"submit", StateDraft, StatePendingApproval, TriggerSubmit, nil},
		{"draft cancelled by requester", StateDraft, StateCancelled, TriggerCancel,
			func(_ *Operation, g *GuardContext) { g.Actor = "user:alice" }},
		{"approve", StatePendingApproval, StateApproved, TriggerApprove, nil},
		{"reject", StatePendingApproval, StateRejected, TriggerReject, nil},
		{"cancel pending", StatePendingApproval, StateCancelled, TriggerCancel,
			func(_ *Operation, g *GuardContext) { g.Actor = "user:alice" }},
		{"expire pending", StatePendingApproval, StateExpired, TriggerExpire,
			func(_ *Operation, g *GuardContext) { g.Now = future.Add(time.Minute) }},
		{"claim", StateApproved, StateExecuting, TriggerClaim, nil},
		{"cancel approved", StateApproved, StateCancelled, TriggerCancel,
			func(_ *Operation, g *GuardContext) { g.Actor = "user:alice" }},
		{"expire approved", StateApproved, StateExpired, TriggerExpire,
			func(o *Operation, g *GuardContext) {
				exp := past
				o.ApprovalExpiresAt = &exp
				g.Now = base
			}},
		{"settle succeeded", StateExecuting, StateSucceeded, TriggerSettle, nil},
		{"settle failed", StateExecuting, StateFailed, TriggerSettle, nil},
		{"settle indeterminate", StateExecuting, StateIndeterminate, TriggerSettle, nil},
		{"reconcile to succeeded", StateIndeterminate, StateSucceeded, TriggerReconcile, nil},
		{"reconcile to failed", StateIndeterminate, StateFailed, TriggerReconcile, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			op := newOp(tc.from)
			gc := okGuard()
			if tc.mutate != nil {
				tc.mutate(op, &gc)
			}
			if err := Validate(op, tc.to, tc.trigger, gc); err != nil {
				t.Fatalf("expected transition to be permitted, got %v", err)
			}
		})
	}
}

// TestValidate_IllegalTransitions walks the full cross product of states and
// asserts that anything absent from the transition table is refused. This is
// the test that catches a table edit that accidentally opens a path.
func TestValidate_IllegalTransitions(t *testing.T) {
	all := []OperationState{
		StateDraft, StatePendingApproval, StateApproved, StateExecuting,
		StateSucceeded, StateFailed, StateRejected, StateExpired,
		StateCancelled, StateIndeterminate,
	}
	triggers := []Trigger{
		TriggerSubmit, TriggerApprove, TriggerReject, TriggerCancel,
		TriggerExpire, TriggerClaim, TriggerSettle, TriggerReconcile,
	}
	for _, from := range all {
		for _, to := range all {
			for _, tr := range triggers {
				if CanTransition(from, to, tr) {
					continue
				}
				op := newOp(from)
				err := Validate(op, to, tr, okGuard())
				if err == nil {
					t.Errorf("%s -> %s via %s: expected refusal, got nil", from, to, tr)
					continue
				}
				var te *TransitionError
				if !errors.As(err, &te) {
					t.Errorf("%s -> %s via %s: want *TransitionError, got %T", from, to, tr, err)
				}
			}
		}
	}
}

func TestValidate_TerminalStatesAreFinal(t *testing.T) {
	terminal := []OperationState{
		StateSucceeded, StateFailed, StateRejected, StateExpired, StateCancelled,
	}
	for _, s := range terminal {
		t.Run(s.String(), func(t *testing.T) {
			if !s.IsTerminal() {
				t.Fatalf("%s should report as terminal", s)
			}
			if got := Transitions(s); len(got) != 0 {
				t.Fatalf("terminal state %s has outbound transitions: %v", s, got)
			}
		})
	}
	// Indeterminate is deliberately NOT terminal: it must be resolvable.
	if StateIndeterminate.IsTerminal() {
		t.Fatal("indeterminate must not be terminal; it has to be reconcilable")
	}
	if len(Transitions(StateIndeterminate)) == 0 {
		t.Fatal("indeterminate must have a resolution path")
	}
}

func TestGuard_ApprovalRequiresUnexpiredProposal(t *testing.T) {
	op := newOp(StatePendingApproval)
	gc := okGuard()
	gc.Now = future.Add(time.Second) // past the proposal deadline

	err := Validate(op, StateApproved, TriggerApprove, gc)
	assertGuardCode(t, err, CodeProposalExpired)
}

func TestGuard_SelfApprovalRefusedWhenPolicyRequiresDistinctApprover(t *testing.T) {
	op := newOp(StatePendingApproval)
	gc := okGuard()
	gc.RequireDistinctApprover = true
	gc.Actor = op.RequestedBy // same principal proposed and approved

	err := Validate(op, StateApproved, TriggerApprove, gc)
	assertGuardCode(t, err, CodeSelfApproval)
}

// A single static bearer token yields exactly one principal, so separation of
// duties cannot be enforced. The policy must refuse rather than silently
// degrade into no policy at all.
func TestGuard_SeparationOfDutiesFailsClosedWithIndistinctIdentity(t *testing.T) {
	op := newOp(StatePendingApproval)
	gc := okGuard()
	gc.RequireDistinctApprover = true
	gc.IdentityDistinguishable = false
	gc.Actor = "user:bob" // different name, but auth mode cannot vouch for it

	err := Validate(op, StateApproved, TriggerApprove, gc)
	assertGuardCode(t, err, CodeIdentityIndistinct)
}

func TestGuard_ClaimRejectsTamperedPayload(t *testing.T) {
	op := newOp(StateApproved)
	gc := okGuard()
	gc.RecomputedHash = "a-different-hash"

	err := Validate(op, StateExecuting, TriggerClaim, gc)
	assertGuardCode(t, err, CodePayloadMismatch)
}

func TestGuard_ClaimRejectsDriftedPreconditions(t *testing.T) {
	op := newOp(StateApproved)
	gc := okGuard()
	gc.PreconditionsMatch = false

	err := Validate(op, StateExecuting, TriggerClaim, gc)
	assertGuardCode(t, err, CodePreconditionChanged)
}

func TestGuard_ClaimRejectsExpiredApproval(t *testing.T) {
	op := newOp(StateApproved)
	exp := past
	op.ApprovalExpiresAt = &exp
	gc := okGuard()

	err := Validate(op, StateExecuting, TriggerClaim, gc)
	assertGuardCode(t, err, CodeApprovalExpired)
}

func TestGuard_ApprovedOperationWithoutDeadlineCannotExecute(t *testing.T) {
	op := newOp(StateApproved)
	op.ApprovalExpiresAt = nil
	gc := okGuard()

	err := Validate(op, StateExecuting, TriggerClaim, gc)
	assertGuardCode(t, err, CodeApprovalExpired)
}

func TestGuard_CancelRefusedWhileLeaseHeld(t *testing.T) {
	op := newOp(StateApproved)
	lease := future
	op.LeaseExpiresAt = &lease
	gc := okGuard()
	gc.Actor = op.RequestedBy

	err := Validate(op, StateCancelled, TriggerCancel, gc)
	assertGuardCode(t, err, CodeNotAuthorized)
}

func TestGuard_OnlyRequesterMayCancel(t *testing.T) {
	op := newOp(StatePendingApproval)
	gc := okGuard()
	gc.Actor = "user:mallory"

	err := Validate(op, StateCancelled, TriggerCancel, gc)
	assertGuardCode(t, err, CodeNotAuthorized)

	// System components may always cancel, e.g. during shutdown.
	gc.Actor = "system:reaper"
	if err := Validate(op, StateCancelled, TriggerCancel, gc); err != nil {
		t.Fatalf("system actor should be permitted to cancel: %v", err)
	}
}

func TestRiskLevel_MaxRiskNeverLowers(t *testing.T) {
	tests := []struct {
		name string
		in   []RiskLevel
		want RiskLevel
	}{
		{"empty defaults low", nil, RiskLow},
		{"unset override is ignored", []RiskLevel{RiskHigh, ""}, RiskHigh},
		{"only unset defaults low", []RiskLevel{"", ""}, RiskLow},
		{"single", []RiskLevel{RiskMedium}, RiskMedium},
		{"policy raises", []RiskLevel{RiskLow, RiskHigh}, RiskHigh},
		{"policy cannot lower", []RiskLevel{RiskCritical, RiskLow}, RiskCritical},
		{"unknown is not treated as harmless", []RiskLevel{RiskLow, RiskLevel("bogus")}, RiskLevel("bogus")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := MaxRisk(tc.in...); got != tc.want {
				t.Fatalf("MaxRisk(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func assertGuardCode(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected guard error %s, got nil", want)
	}
	var ge *GuardError
	if !errors.As(err, &ge) {
		t.Fatalf("expected *GuardError, got %T: %v", err, err)
	}
	if ge.Code() != want {
		t.Fatalf("guard code = %s, want %s", ge.Code(), want)
	}
}
