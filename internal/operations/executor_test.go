package operations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeRunner is a scripted plugin. Each hook can be overridden per test, and
// every call is counted so a test can assert that a dangerous one never ran.
type fakeRunner struct {
	mu sync.Mutex

	planFn    func(params json.RawMessage) (PlanResult, error)
	applyFn   func(params json.RawMessage) (ApplyOutcome, error)
	observeFn func(params json.RawMessage) (json.RawMessage, error)

	planCalls    atomic.Int32
	applyCalls   atomic.Int32
	observeCalls atomic.Int32
}

func (f *fakeRunner) Plan(_ context.Context, _, _ string, params json.RawMessage) (PlanResult, error) {
	f.planCalls.Add(1)
	if f.planFn != nil {
		return f.planFn(params)
	}
	return PlanResult{
		Preconditions: json.RawMessage(`{"channel":"36"}`),
		Desired:       json.RawMessage(`{"channel":"149"}`),
	}, nil
}

func (f *fakeRunner) Apply(_ context.Context, _, _ string, params json.RawMessage, _ PlanResult) (ApplyOutcome, error) {
	f.applyCalls.Add(1)
	if f.applyFn != nil {
		return f.applyFn(params)
	}
	return ApplyOutcome{UpstreamRef: "job-1"}, nil
}

func (f *fakeRunner) Observe(_ context.Context, _, _ string, params json.RawMessage) (json.RawMessage, error) {
	f.observeCalls.Add(1)
	if f.observeFn != nil {
		return f.observeFn(params)
	}
	return json.RawMessage(`{"channel":"149"}`), nil
}

// memRepo is an in-memory Repository with the same guard semantics as SQLite:
// transitions are compare-and-set, so losing a race is an ordinary outcome.
type memRepo struct {
	mu     sync.Mutex
	ops    map[string]*Operation
	audit  []AuditEntry
	events []OutboxEvent
}

func newMemRepo() *memRepo {
	return &memRepo{ops: make(map[string]*Operation)}
}

func (m *memRepo) put(op *Operation) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *op
	m.ops[op.ID] = &cp
}

func (m *memRepo) Propose(_ context.Context, req RepoProposeRequest) (*Operation, error) {
	m.put(req.Operation)
	m.record(req.Audit, req.Event)
	return req.Operation, nil
}

func (m *memRepo) Get(_ context.Context, id string) (*Operation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	op, ok := m.ops[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *op
	return &cp, nil
}

func (m *memRepo) List(context.Context, ListFilter) ([]*Operation, error) { return nil, nil }

func (m *memRepo) Transition(_ context.Context, req TransitionRequest) (*Operation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	op, ok := m.ops[req.OperationID]
	if !ok {
		return nil, ErrNotFound
	}
	if op.State != req.From {
		return nil, ErrStateConflict
	}
	op.State = req.To
	op.ErrorCode = req.ErrorCode
	op.ErrorDetail = req.ErrorDetail
	if req.Approval != nil {
		op.ApprovedBy = req.Approval.ApprovedBy
		t := req.Approval.ApprovedAt
		op.ApprovedAt = &t
		e := req.Approval.ApprovalExpiresAt
		op.ApprovalExpiresAt = &e
		op.AuthorizedByRule = req.Approval.AuthorizedByRule
	}
	m.record(req.Audit, req.Event)
	cp := *op
	return &cp, nil
}

func (m *memRepo) Claim(_ context.Context, req ClaimRequest) (*Operation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	op, ok := m.ops[req.OperationID]
	if !ok {
		return nil, ErrNotFound
	}
	// Exactly the conditions the SQL WHERE clause encodes.
	if op.State != StateApproved || op.PayloadHash != req.ExpectedHash {
		return nil, ErrClaimLost
	}
	op.State = StateExecuting
	op.LeaseOwner = req.InstanceID
	op.AttemptCount++
	m.record(req.Audit, req.Event)
	cp := *op
	return &cp, nil
}

func (m *memRepo) Settle(_ context.Context, req SettleRequest) (*Operation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	op, ok := m.ops[req.OperationID]
	if !ok {
		return nil, ErrNotFound
	}
	op.State = req.To
	op.OutcomeVerified = req.Verified
	op.Observed = req.Observed
	op.ErrorCode = req.ErrorCode
	op.ErrorDetail = req.ErrorDetail
	op.LeaseOwner = ""
	m.record(req.Audit, req.Event)
	cp := *op
	return &cp, nil
}

func (m *memRepo) DueForExpiry(_ context.Context, now time.Time, _ int) ([]*Operation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Operation
	for _, op := range m.ops {
		switch op.State {
		case StatePendingApproval:
			if !now.Before(op.ExpiresAt) {
				cp := *op
				out = append(out, &cp)
			}
		case StateApproved:
			if op.ApprovalExpiresAt != nil && !now.Before(*op.ApprovalExpiresAt) {
				cp := *op
				out = append(out, &cp)
			}
		case StateExecuting:
			if op.LeaseExpiresAt != nil && !now.Before(*op.LeaseExpiresAt) {
				cp := *op
				out = append(out, &cp)
			}
		}
	}
	return out, nil
}

func (m *memRepo) Claimable(context.Context, int) ([]*Operation, error) { return nil, nil }

func (m *memRepo) record(a AuditEntry, e OutboxEvent) {
	m.audit = append(m.audit, a)
	m.events = append(m.events, e)
}

func (m *memRepo) subjects() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.events))
	for _, e := range m.events {
		out = append(out, e.Subject)
	}
	return out
}

// approvedOp builds an operation ready to execute.
func approvedOp(t *testing.T) *Operation {
	t.Helper()
	target := json.RawMessage(`{"mac":"AA:BB","radio_id":2}`)
	params := json.RawMessage(`{"channel":"149"}`)
	hash, err := PayloadHash("cnmaestro", "device.set_radio_channel", target, params)
	if err != nil {
		t.Fatal(err)
	}
	expiry := base.Add(15 * time.Minute)
	lease := base.Add(2 * time.Minute)
	return &Operation{
		ID: "op_exec", Plugin: "cnmaestro", Action: "device.set_radio_channel",
		State: StateApproved, Risk: RiskMedium,
		Target: target, Params: params, PayloadHash: hash,
		Preconditions:     json.RawMessage(`{"channel":"36"}`),
		Desired:           json.RawMessage(`{"channel":"149"}`),
		Verifiable:        true,
		RequestedBy:       "user:alice",
		ApprovedBy:        "user:bob",
		RequestedAt:       base,
		ExpiresAt:         base.Add(30 * time.Minute),
		ApprovalExpiresAt: &expiry,
		LeaseExpiresAt:    &lease,
		CorrelationID:     "corr-1",
	}
}

func newExecutor(repo Repository, runner MutationRunner) *Executor {
	return NewExecutor(repo, runner, ExecutorConfig{
		InstanceID:     "mcpd-test",
		VerifyAttempts: 2,
		VerifyBackoff:  time.Millisecond,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)),
		func() time.Time { return base },
		NewULIDGenerator(func() time.Time { return base }), nil)
}

func TestExecutor_HappyPath(t *testing.T) {
	repo := newMemRepo()
	repo.put(approvedOp(t))
	runner := &fakeRunner{}

	if err := newExecutor(repo, runner).Execute(context.Background(), "op_exec"); err != nil {
		t.Fatal(err)
	}

	op, _ := repo.Get(context.Background(), "op_exec")
	if op.State != StateSucceeded {
		t.Fatalf("state = %s, want succeeded (error: %s)", op.State, op.ErrorDetail)
	}
	if op.OutcomeVerified == nil || !*op.OutcomeVerified {
		t.Fatal("a succeeded operation must be marked verified")
	}
	if runner.applyCalls.Load() != 1 {
		t.Fatalf("Apply ran %d times, want exactly 1", runner.applyCalls.Load())
	}
	if runner.observeCalls.Load() == 0 {
		t.Fatal("success must be confirmed by observation, not by the write returning")
	}
	if op.LeaseOwner != "" {
		t.Fatal("settling must release the lease")
	}
}

// Drift is the case the whole precondition mechanism exists for: the target
// changed after approval, so applying would overwrite an unreviewed change.
func TestExecutor_PreconditionDriftRefusesToApply(t *testing.T) {
	repo := newMemRepo()
	repo.put(approvedOp(t))
	runner := &fakeRunner{
		planFn: func(json.RawMessage) (PlanResult, error) {
			// Someone moved the radio to channel 44 in the meantime.
			return PlanResult{
				Preconditions: json.RawMessage(`{"channel":"44"}`),
				Desired:       json.RawMessage(`{"channel":"149"}`),
			}, nil
		},
	}

	if err := newExecutor(repo, runner).Execute(context.Background(), "op_exec"); err != nil {
		t.Fatal(err)
	}

	op, _ := repo.Get(context.Background(), "op_exec")
	if op.State != StateFailed {
		t.Fatalf("state = %s, want failed", op.State)
	}
	if op.ErrorCode != CodePreconditionChanged {
		t.Fatalf("error code = %s, want %s", op.ErrorCode, CodePreconditionChanged)
	}
	if runner.applyCalls.Load() != 0 {
		t.Fatal("Apply must never run when preconditions drifted")
	}
}

// A payload altered after approval must not execute, even though the state
// machine would otherwise allow it.
func TestExecutor_TamperedPayloadRefusesToApply(t *testing.T) {
	repo := newMemRepo()
	op := approvedOp(t)
	op.Params = json.RawMessage(`{"channel":"36"}`) // hash no longer matches
	repo.put(op)
	runner := &fakeRunner{}

	if err := newExecutor(repo, runner).Execute(context.Background(), "op_exec"); err != nil {
		t.Fatal(err)
	}

	got, _ := repo.Get(context.Background(), "op_exec")
	if got.ErrorCode != CodePayloadMismatch {
		t.Fatalf("error code = %s, want %s", got.ErrorCode, CodePayloadMismatch)
	}
	if runner.applyCalls.Load() != 0 {
		t.Fatal("Apply must never run for a payload that does not match its hash")
	}
}

// An ambiguous upstream failure must land in indeterminate, never failed.
// Recording it as a failure invites a retry that double-applies.
func TestExecutor_AmbiguousFailureIsIndeterminate(t *testing.T) {
	repo := newMemRepo()
	repo.put(approvedOp(t))
	runner := &fakeRunner{
		applyFn: func(json.RawMessage) (ApplyOutcome, error) {
			return ApplyOutcome{UpstreamRef: "job-9"},
				fmt.Errorf("post timed out: %w", ErrIndeterminate)
		},
	}

	if err := newExecutor(repo, runner).Execute(context.Background(), "op_exec"); err != nil {
		t.Fatal(err)
	}

	op, _ := repo.Get(context.Background(), "op_exec")
	if op.State != StateIndeterminate {
		t.Fatalf("state = %s, want indeterminate", op.State)
	}
	if op.State.IsTerminal() {
		t.Fatal("indeterminate must not be terminal; it awaits reconciliation")
	}
	if op.ErrorCode != CodeIndeterminate {
		t.Fatalf("error code = %s, want %s", op.ErrorCode, CodeIndeterminate)
	}
}

// A write that succeeds but never converges must not be reported as success.
func TestExecutor_UnverifiableOutcomeIsIndeterminate(t *testing.T) {
	repo := newMemRepo()
	repo.put(approvedOp(t))
	runner := &fakeRunner{
		observeFn: func(json.RawMessage) (json.RawMessage, error) {
			// The radio never moves.
			return json.RawMessage(`{"channel":"36"}`), nil
		},
	}

	if err := newExecutor(repo, runner).Execute(context.Background(), "op_exec"); err != nil {
		t.Fatal(err)
	}

	op, _ := repo.Get(context.Background(), "op_exec")
	if op.State != StateIndeterminate {
		t.Fatalf("state = %s, want indeterminate", op.State)
	}
	if op.ErrorCode != CodeVerificationFailed {
		t.Fatalf("error code = %s, want %s", op.ErrorCode, CodeVerificationFailed)
	}
	if runner.observeCalls.Load() < 2 {
		t.Fatal("verification must retry before giving up")
	}
}

// Verification must be bounded. An upstream that never converges has to be
// reported rather than retried forever.
func TestExecutor_VerificationIsBounded(t *testing.T) {
	repo := newMemRepo()
	repo.put(approvedOp(t))
	runner := &fakeRunner{
		observeFn: func(json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"channel":"36"}`), nil
		},
	}
	ex := NewExecutor(repo, runner, ExecutorConfig{
		InstanceID: "mcpd-test", VerifyAttempts: 3, VerifyBackoff: time.Millisecond,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)),
		func() time.Time { return base },
		NewULIDGenerator(func() time.Time { return base }), nil)

	if err := ex.Execute(context.Background(), "op_exec"); err != nil {
		t.Fatal(err)
	}
	if got := runner.observeCalls.Load(); got != 3 {
		t.Fatalf("Observe ran %d times, want exactly the configured 3", got)
	}
}

// Redelivery of the same event must not apply the mutation twice.
func TestExecutor_RedeliveryIsIdempotent(t *testing.T) {
	repo := newMemRepo()
	repo.put(approvedOp(t))
	runner := &fakeRunner{}
	ex := newExecutor(repo, runner)
	ctx := context.Background()

	for range 3 {
		if err := ex.Execute(ctx, "op_exec"); err != nil {
			t.Fatal(err)
		}
	}
	if got := runner.applyCalls.Load(); got != 1 {
		t.Fatalf("Apply ran %d times across three deliveries, want exactly 1", got)
	}
}

// Concurrent executors must produce exactly one Apply.
func TestExecutor_ConcurrentExecutionAppliesOnce(t *testing.T) {
	repo := newMemRepo()
	repo.put(approvedOp(t))
	runner := &fakeRunner{}

	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_ = newExecutor(repo, runner).Execute(context.Background(), "op_exec")
		}()
	}
	close(start)
	wg.Wait()

	if got := runner.applyCalls.Load(); got != 1 {
		t.Fatalf("Apply ran %d times under concurrency, want exactly 1", got)
	}
	op, _ := repo.Get(context.Background(), "op_exec")
	if op.AttemptCount != 1 {
		t.Fatalf("attempt_count = %d, want 1", op.AttemptCount)
	}
}

func TestExecutor_EmitsLifecycleEvents(t *testing.T) {
	repo := newMemRepo()
	repo.put(approvedOp(t))
	if err := newExecutor(repo, &fakeRunner{}).Execute(context.Background(), "op_exec"); err != nil {
		t.Fatal(err)
	}
	subjects := repo.subjects()
	for _, want := range []string{"mcp.operation.executing", "mcp.operation.succeeded"} {
		found := false
		for _, s := range subjects {
			if s == want {
				found = true
			}
		}
		if !found {
			t.Errorf("no %s event was queued; got %v", want, subjects)
		}
	}
}

func TestExecutor_IgnoresOperationsNotApproved(t *testing.T) {
	repo := newMemRepo()
	op := approvedOp(t)
	op.State = StatePendingApproval
	repo.put(op)
	runner := &fakeRunner{}

	if err := newExecutor(repo, runner).Execute(context.Background(), "op_exec"); err != nil {
		t.Fatal(err)
	}
	if runner.planCalls.Load() != 0 || runner.applyCalls.Load() != 0 {
		t.Fatal("an unapproved operation must not be touched")
	}
}

func TestExecutor_MissingOperation(t *testing.T) {
	repo := newMemRepo()
	err := newExecutor(repo, &fakeRunner{}).Execute(context.Background(), "op_nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// --- reaper ---------------------------------------------------------------

func newReaper(repo Repository, now time.Time) *Reaper {
	return NewReaper(repo, slog.New(slog.NewTextHandler(io.Discard, nil)),
		func() time.Time { return now },
		NewULIDGenerator(func() time.Time { return now }), nil, time.Minute)
}

func TestReaper_ExpiresProposalsAndApprovals(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*Operation)
		wantCode string
	}{
		{"expired proposal", func(op *Operation) {
			op.State = StatePendingApproval
			op.ExpiresAt = base.Add(-time.Minute)
		}, CodeProposalExpired},
		{"expired approval", func(op *Operation) {
			op.State = StateApproved
			exp := base.Add(-time.Minute)
			op.ApprovalExpiresAt = &exp
		}, CodeApprovalExpired},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := newMemRepo()
			op := approvedOp(t)
			tc.mutate(op)
			repo.put(op)

			newReaper(repo, base).Sweep(context.Background())

			got, _ := repo.Get(context.Background(), "op_exec")
			if got.State != StateExpired {
				t.Fatalf("state = %s, want expired", got.State)
			}
			if got.ErrorCode != tc.wantCode {
				t.Fatalf("error code = %s, want %s", got.ErrorCode, tc.wantCode)
			}
		})
	}
}

// A worker that died mid-execution leaves an unknown outcome, not a failure.
func TestReaper_ExpiredLeaseBecomesIndeterminate(t *testing.T) {
	repo := newMemRepo()
	op := approvedOp(t)
	op.State = StateExecuting
	lease := base.Add(-time.Minute)
	op.LeaseExpiresAt = &lease
	op.LeaseOwner = "mcpd-dead"
	repo.put(op)

	newReaper(repo, base).Sweep(context.Background())

	got, _ := repo.Get(context.Background(), "op_exec")
	if got.State != StateIndeterminate {
		t.Fatalf("state = %s, want indeterminate -- recording a crashed execution "+
			"as failed would invite a retry that double-applies", got.State)
	}
	if got.ErrorCode != CodeLeaseExpired {
		t.Fatalf("error code = %s, want %s", got.ErrorCode, CodeLeaseExpired)
	}
}

func TestReaper_LeavesLiveOperationsAlone(t *testing.T) {
	repo := newMemRepo()
	repo.put(approvedOp(t)) // deadlines are in the future
	newReaper(repo, base).Sweep(context.Background())

	got, _ := repo.Get(context.Background(), "op_exec")
	if got.State != StateApproved {
		t.Fatalf("state = %s, want approved to be left alone", got.State)
	}
}

// --- redaction ------------------------------------------------------------

func TestRedact(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		mustNot string
	}{
		{"authorization header", `request failed: Authorization: Bearer sk-secret-value-here`, "sk-secret-value-here"},
		{"bearer inline", `got 401 for bearer abcdefghijklmnop`, "abcdefghijklmnop"},
		{"query token", `GET /api/v2/devices?access_token=tok_abc123&limit=10`, "tok_abc123"},
		{"json field", `{"client_secret":"shhh-very-secret"}`, "shhh-very-secret"},
		{"url userinfo", `dial https://admin:hunter2@cnmaestro.local/api`, "hunter2"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := redact(tc.in)
			if contains(got, tc.mustNot) {
				t.Fatalf("redact(%q) = %q, which still contains %q", tc.in, got, tc.mustNot)
			}
		})
	}
}

func TestRedact_BoundsLength(t *testing.T) {
	long := ""
	for range 5000 {
		long += "x"
	}
	if len(redact(long)) > 1100 {
		t.Fatal("redact must bound its output so an HTML error page cannot fill the audit trail")
	}
}

func contains(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// An unverifiable mutation must not be recorded as verified.
//
// The bug: verify() short-circuited to true whenever there was no desired
// state to compare, the operation settled with outcome_verified = 1, and the
// note handed to the model said the change had been "confirmed by re-reading
// the target". Nothing had been read. Null is the honest column value, because
// "not checked" is a different fact from "checked and did not match".
func TestExecutor_UnverifiableMutationSettlesUnchecked(t *testing.T) {
	repo := newMemRepo()
	op := approvedOp(t)
	op.Verifiable = false
	op.Desired = nil
	repo.put(op)

	runner := &fakeRunner{}
	if err := newExecutor(repo, runner).Execute(context.Background(), op.ID); err != nil {
		t.Fatalf("execute: %v", err)
	}

	got, _ := repo.Get(context.Background(), op.ID)
	if got.State != StateSucceeded {
		t.Fatalf("state = %s, want succeeded", got.State)
	}
	if got.OutcomeVerified != nil {
		t.Fatalf("outcome_verified = %v, want null: nothing was compared", *got.OutcomeVerified)
	}
	if runner.observeCalls.Load() != 0 {
		t.Fatalf("Observe ran %d times; a mutation that cannot be verified is not observed",
			runner.observeCalls.Load())
	}
	if got.Assurance() != AssuranceGatedCall {
		t.Fatalf("assurance = %s, want gated_call", got.Assurance())
	}
}

// The ordinary case still proves itself: a verifiable mutation whose target
// reads back as requested settles verified.
func TestExecutor_VerifiableMutationStillConfirms(t *testing.T) {
	repo := newMemRepo()
	op := approvedOp(t)
	repo.put(op)

	runner := &fakeRunner{}
	if err := newExecutor(repo, runner).Execute(context.Background(), op.ID); err != nil {
		t.Fatalf("execute: %v", err)
	}

	got, _ := repo.Get(context.Background(), op.ID)
	if got.State != StateSucceeded {
		t.Fatalf("state = %s, want succeeded", got.State)
	}
	if got.OutcomeVerified == nil || !*got.OutcomeVerified {
		t.Fatalf("outcome_verified = %v, want true", got.OutcomeVerified)
	}
	if runner.observeCalls.Load() == 0 {
		t.Fatal("a verifiable mutation must re-read the target")
	}
	if got.Assurance() != AssuranceReviewedChange {
		t.Fatalf("assurance = %s, want reviewed_change", got.Assurance())
	}
}

// A verifiable delete desires absence, and observing absence confirms it. This
// is why verifiability is declared rather than inferred from an empty Desired:
// the same empty field means "nothing to check" for one mutation and "the
// thing should be gone" for another.
func TestExecutor_VerifiableAbsenceIsConfirmedByObservingNothing(t *testing.T) {
	repo := newMemRepo()
	op := approvedOp(t)
	op.Desired = nil
	repo.put(op)

	runner := &fakeRunner{
		planFn: func(json.RawMessage) (PlanResult, error) {
			return PlanResult{Preconditions: json.RawMessage(`{"channel":"36"}`)}, nil
		},
		observeFn: func(json.RawMessage) (json.RawMessage, error) { return nil, nil },
	}
	if err := newExecutor(repo, runner).Execute(context.Background(), op.ID); err != nil {
		t.Fatalf("execute: %v", err)
	}

	got, _ := repo.Get(context.Background(), op.ID)
	if got.State != StateSucceeded {
		t.Fatalf("state = %s, want succeeded", got.State)
	}
	if got.OutcomeVerified == nil || !*got.OutcomeVerified {
		t.Fatalf("outcome_verified = %v, want true", got.OutcomeVerified)
	}
}

// A mutation declaring no preconditions has not passed a drift check, it has
// skipped one. It still executes -- refusing every such mutation would be a
// different decision -- but the audit entry says which of the two happened,
// and the operation reports itself as a gated call rather than a reviewed
// change.
func TestExecutor_NoPreconditionsRecordsThatDriftWasNotChecked(t *testing.T) {
	repo := newMemRepo()
	op := approvedOp(t)
	op.Preconditions = nil
	repo.put(op)

	runner := &fakeRunner{
		planFn: func(json.RawMessage) (PlanResult, error) {
			return PlanResult{Desired: json.RawMessage(`{"channel":"149"}`)}, nil
		},
	}
	if err := newExecutor(repo, runner).Execute(context.Background(), op.ID); err != nil {
		t.Fatalf("execute: %v", err)
	}

	got, _ := repo.Get(context.Background(), op.ID)
	if got.State != StateSucceeded {
		t.Fatalf("state = %s, want succeeded", got.State)
	}
	if got.Assurance() != AssuranceGatedCall {
		t.Fatalf("assurance = %s, want gated_call: nothing checked for drift", got.Assurance())
	}

	var claim *AuditEntry
	for i, e := range repo.audit {
		if e.Kind == "operation.executing" {
			claim = &repo.audit[i]
		}
	}
	if claim == nil {
		t.Fatal("no operation.executing audit entry")
	}
	var detail map[string]any
	if err := json.Unmarshal(claim.Detail, &detail); err != nil {
		t.Fatal(err)
	}
	if detail["drift"] != "not_checked" {
		t.Fatalf("audit drift = %v, want not_checked", detail["drift"])
	}
}

// The hole one step later than the propose-time check: a change a standing
// rule authorised as routine, whose re-plan immediately before execution says
// it is not. Nobody ever looked at this change, so the authorisation does not
// cover what is now about to happen, and the write must not go out.
func TestExecutor_RefusesAnAutoApprovedChangeWhoseRiskWasRaised(t *testing.T) {
	repo := newMemRepo()
	op := approvedOp(t)
	op.Risk = RiskLow
	op.ApprovedBy = PolicyActor
	op.AuthorizedByRule = "routine-radio"
	repo.put(op)

	raised := RiskHigh
	runner := &fakeRunner{planFn: func(json.RawMessage) (PlanResult, error) {
		return PlanResult{
			Preconditions: json.RawMessage(`{"channel":"36"}`),
			Desired:       json.RawMessage(`{"channel":"149"}`),
			RiskOverride:  &raised,
		}, nil
	}}

	if err := newExecutor(repo, runner).Execute(context.Background(), "op_exec"); err != nil {
		t.Fatal(err)
	}

	if runner.applyCalls.Load() != 0 {
		t.Fatal("nothing may be written upstream once the authorisation no longer covers the change")
	}
	stored, _ := repo.Get(context.Background(), "op_exec")
	if stored.State != StateFailed {
		t.Fatalf("state = %s, want failed", stored.State)
	}
	if stored.ErrorCode != CodeRiskRaised {
		t.Fatalf("error_code = %q, want %q", stored.ErrorCode, CodeRiskRaised)
	}
	if !strings.Contains(stored.ErrorDetail, "routine-radio") {
		t.Errorf("the refusal must name the rule it outran: %q", stored.ErrorDetail)
	}
}

// The same raise where a person approved is not a refusal. They looked at this
// change and said yes to it; a reclassification does not unmake their decision,
// and treating it as one would make every approval provisional.
func TestExecutor_ARaisedRiskDoesNotUnmakeAHumanApproval(t *testing.T) {
	repo := newMemRepo()
	op := approvedOp(t)
	op.Risk = RiskLow
	repo.put(op)

	raised := RiskHigh
	runner := &fakeRunner{planFn: func(json.RawMessage) (PlanResult, error) {
		return PlanResult{
			Preconditions: json.RawMessage(`{"channel":"36"}`),
			Desired:       json.RawMessage(`{"channel":"149"}`),
			RiskOverride:  &raised,
		}, nil
	}}

	if err := newExecutor(repo, runner).Execute(context.Background(), "op_exec"); err != nil {
		t.Fatal(err)
	}
	stored, _ := repo.Get(context.Background(), "op_exec")
	if stored.State != StateSucceeded {
		t.Fatalf("state = %s, want succeeded (%s)", stored.State, stored.ErrorDetail)
	}
}
