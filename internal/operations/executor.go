package operations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// MutationRunner is the executor's view of a plugin's mutation handler. The
// plugins package supplies an implementation; the domain never sees the
// plugin's concrete types.
type MutationRunner interface {
	// Plan re-reads current state so preconditions can be checked against
	// reality immediately before the write.
	Plan(ctx context.Context, plugin, action string, params json.RawMessage) (PlanResult, error)
	// Apply performs the upstream write. It must be called at most once per
	// attempt and must report an ambiguous outcome by wrapping
	// ErrIndeterminate.
	Apply(ctx context.Context, plugin, action string, params json.RawMessage, plan PlanResult) (ApplyOutcome, error)
	// Observe re-reads state for verification and reconciliation.
	Observe(ctx context.Context, plugin, action string, params json.RawMessage) (json.RawMessage, error)
}

// PlanResult is the type-erased output of a plugin's Plan.
type PlanResult struct {
	Before        json.RawMessage
	Desired       json.RawMessage
	Preconditions json.RawMessage
	Changes       []Change
	Impact        string
	Rollback      json.RawMessage
	RiskOverride  *RiskLevel
	// Handle carries the plugin's typed plan through to Apply without a JSON
	// round trip, so state that does not survive serialisation is preserved.
	Handle any
}

// ApplyOutcome reports what an upstream write produced.
type ApplyOutcome struct {
	UpstreamRef string
	// Async reports that upstream accepted the request but has not applied it
	// yet, so success requires observation rather than an HTTP status.
	Async bool
}

// ExecutorConfig tunes execution.
type ExecutorConfig struct {
	// InstanceID identifies this process in leases and attempt records.
	InstanceID string
	// LeaseTTL bounds a claim before the reaper reclaims it.
	LeaseTTL time.Duration
	// Concurrency bounds simultaneous executions. Infrastructure mutations are
	// rare and consequential, so this is small by default: a burst of parallel
	// changes to the same network is rarely what anyone wants.
	Concurrency int
	// VerifyAttempts bounds how many times the verifier re-observes before
	// giving up. Never unbounded: an upstream that never converges must
	// eventually be reported rather than retried forever.
	VerifyAttempts int
	// VerifyBackoff is the first delay between observations.
	VerifyBackoff time.Duration
	// VerifyMaxBackoff caps that delay.
	VerifyMaxBackoff time.Duration
}

func (c *ExecutorConfig) withDefaults() {
	if c.LeaseTTL <= 0 {
		c.LeaseTTL = 2 * time.Minute
	}
	if c.Concurrency <= 0 {
		c.Concurrency = 2
	}
	if c.VerifyAttempts <= 0 {
		c.VerifyAttempts = 6
	}
	if c.VerifyBackoff <= 0 {
		c.VerifyBackoff = 2 * time.Second
	}
	if c.VerifyMaxBackoff <= 0 {
		c.VerifyMaxBackoff = 30 * time.Second
	}
}

// Executor performs approved mutations.
//
// It is the component that must never get it wrong, so it trusts nothing it is
// handed. An event announcing an approval is only a hint to look; every fact
// the executor acts on is reloaded from the database and revalidated.
type Executor struct {
	repo   Repository
	runner MutationRunner
	cfg    ExecutorConfig
	log    *slog.Logger
	now    func() time.Time
	ids    IDGenerator
	notify func()
	sem    chan struct{}
}

// NewExecutor builds an executor.
func NewExecutor(repo Repository, runner MutationRunner, cfg ExecutorConfig, log *slog.Logger, now func() time.Time, ids IDGenerator, notify func()) *Executor {
	cfg.withDefaults()
	if now == nil {
		now = time.Now
	}
	if notify == nil {
		notify = func() {}
	}
	return &Executor{
		repo: repo, runner: runner, cfg: cfg, log: log,
		now: now, ids: ids, notify: notify,
		sem: make(chan struct{}, cfg.Concurrency),
	}
}

// Execute attempts one operation, identified only by ID.
//
// Taking just an identifier is deliberate. Everything else -- the payload, the
// approval, the risk -- is read from the database, so a forged or stale event
// cannot smuggle in different instructions.
func (e *Executor) Execute(ctx context.Context, operationID string) error {
	select {
	case e.sem <- struct{}{}:
		defer func() { <-e.sem }()
	case <-ctx.Done():
		return ctx.Err()
	}

	op, err := e.repo.Get(ctx, operationID)
	if err != nil {
		return err
	}
	if op.State != StateApproved {
		// Already handled, or never eligible. Redelivery lands here and is
		// silently correct.
		return nil
	}

	// 1. Recompute the payload hash from what is stored. A mismatch means the
	//    row was altered after approval.
	recomputed, err := Recompute(op)
	if err != nil {
		return fmt.Errorf("operations: recompute payload hash: %w", err)
	}
	if recomputed != op.PayloadHash {
		e.log.Error("stored payload does not match its hash; refusing to execute",
			"operation_id", op.ID, "plugin", op.Plugin, "action", op.Action)
		return e.settleFailure(ctx, op, "", CodePayloadMismatch,
			"the stored mutation payload does not match the hash recorded at approval")
	}

	// 2. Re-plan against live upstream state and compare preconditions. This
	//    is where drift is caught: the same code that produced the diff the
	//    approver read now reports what the target actually looks like.
	plan, err := e.runner.Plan(ctx, op.Plugin, op.Action, op.Params)
	if err != nil {
		e.log.Warn("could not re-plan before execution",
			"operation_id", op.ID, "error", err)
		return e.settleFailure(ctx, op, "", CodeUpstreamFailed,
			"the target could not be read before execution")
	}
	drift := CheckDrift(op.Preconditions, plan.Preconditions)
	switch drift {
	case DriftDetected:
		e.log.Warn("target changed since the operation was proposed; refusing to execute",
			"operation_id", op.ID, "plugin", op.Plugin, "action", op.Action)
		return e.settleFailure(ctx, op, "", CodePreconditionChanged,
			"the target changed after this operation was approved, so applying it "+
				"would overwrite a change nobody reviewed")
	case DriftNotChecked:
		// Said out loud rather than passed over. A mutation that declared no
		// preconditions has not survived a drift check, it has skipped one,
		// and the audit entry below records which of the two happened.
		e.log.Warn("this mutation declares no preconditions, so drift could not be checked",
			"operation_id", op.ID, "plugin", op.Plugin, "action", op.Action)
	}

	// 2b. The same re-plan may reclassify the change. Where a person approved,
	//     they approved this change and a revised classification does not
	//     unmake their decision. Where a standing rule did, nobody ever looked:
	//     the rule authorised a change of one severity and the target now says
	//     it is another, so the authorisation does not cover what is about to
	//     happen. Refusing sends it back through propose, where the raised risk
	//     is what the policy sees and a person is asked.
	if op.AutoApproved() && plan.RiskOverride != nil {
		if raised := MaxRisk(op.Risk, *plan.RiskOverride); raised != op.Risk {
			e.log.Warn("re-planning raised the risk of a change nobody was asked about; refusing to execute",
				"operation_id", op.ID, "plugin", op.Plugin, "action", op.Action,
				"authorized_risk", op.Risk, "current_risk", raised, "rule", op.AuthorizedByRule)
			return e.settleFailure(ctx, op, "", CodeRiskRaised, fmt.Sprintf(
				"rule %s authorised this as a %s change and re-reading the target "+
					"classified it %s; nobody has seen it at that classification",
				op.AuthorizedByRule, op.Risk, raised))
		}
	}

	// 3. Claim. The guarded update is what guarantees at-most-once execution:
	//    losing the race is an ordinary outcome, not an error.
	attemptID := e.ids.AttemptID()
	claimed, err := e.repo.Claim(ctx, ClaimRequest{
		OperationID:    op.ID,
		ExpectedHash:   op.PayloadHash,
		InstanceID:     e.cfg.InstanceID,
		LeaseExpiresAt: e.now().Add(e.cfg.LeaseTTL),
		AttemptID:      attemptID,
		Audit: auditFor(e.ids.EventID(), "operation.executing", op,
			"system:executor", StateApproved, StateExecuting,
			map[string]any{
				"instance": e.cfg.InstanceID,
				"attempt":  attemptID,
				// What was actually checked, rather than the absence of a
				// complaint. "not_checked" and "none" are different facts.
				"drift":      drift.String(),
				"verifiable": op.Verifiable,
				"assurance":  op.Assurance().String(),
			}),
		Event: eventFor(e.ids.EventID(), subjectFor(StateExecuting), op),
	})
	if errors.Is(err, ErrClaimLost) {
		e.log.Debug("another worker claimed the operation first", "operation_id", op.ID)
		return nil
	}
	if err != nil {
		return err
	}
	e.notify()

	return e.apply(ctx, claimed, attemptID, plan)
}

// apply performs the upstream write and settles the outcome.
func (e *Executor) apply(ctx context.Context, op *Operation, attemptID string, plan PlanResult) error {
	e.log.Info("executing mutation",
		"operation_id", op.ID, "plugin", op.Plugin, "action", op.Action,
		"risk", op.Risk, "approved_by", op.ApprovedBy, "attempt", op.AttemptCount)

	outcome, err := e.runner.Apply(ctx, op.Plugin, op.Action, op.Params, plan)

	switch {
	case errors.Is(err, ErrIndeterminate):
		// The write may or may not have landed. Recording this as a failure
		// would invite a retry that double-applies it.
		e.log.Error("upstream outcome is indeterminate; manual reconciliation required",
			"operation_id", op.ID, "plugin", op.Plugin, "action", op.Action,
			"upstream_ref", outcome.UpstreamRef, "error", err)
		return e.settle(ctx, op, attemptID, StateIndeterminate, nil, nil,
			outcome.UpstreamRef, CodeIndeterminate, redact(err.Error()))

	case err != nil:
		return e.settle(ctx, op, attemptID, StateFailed, nil, nil,
			outcome.UpstreamRef, CodeUpstreamFailed, redact(err.Error()))
	}

	if !op.Verifiable {
		// The mutation declared that re-reading the target proves nothing
		// about the write, so nothing is read and nothing is claimed. The
		// verified column stays null: false would say a check ran and
		// disagreed, true would say one ran and agreed, and neither happened.
		e.log.Info("mutation applied; this action cannot be confirmed by re-reading the target",
			"operation_id", op.ID, "plugin", op.Plugin, "action", op.Action,
			"upstream_ref", outcome.UpstreamRef)
		return e.settle(ctx, op, attemptID, StateSucceeded, nil, nil,
			outcome.UpstreamRef, "", "")
	}

	// HTTP success is not success. Verify by reading the target back.
	observed, verified, vErr := e.verify(ctx, op, plan)
	if !verified {
		detail := "the target could not be re-read to confirm the change"
		if vErr != nil {
			detail = redact(vErr.Error())
		}
		e.log.Warn("mutation applied but could not be verified",
			"operation_id", op.ID, "error", vErr)
		return e.settle(ctx, op, attemptID, StateIndeterminate, observed, boolPtr(false),
			outcome.UpstreamRef, CodeVerificationFailed, detail)
	}

	e.log.Info("mutation applied and verified",
		"operation_id", op.ID, "plugin", op.Plugin, "action", op.Action,
		"upstream_ref", outcome.UpstreamRef)
	return e.settle(ctx, op, attemptID, StateSucceeded, observed, boolPtr(true),
		outcome.UpstreamRef, "", "")
}

// verify re-observes the target until it matches the desired state.
//
// It is only reached for a mutation that declared itself verifiable, and that
// declaration is what makes the comparison meaningful. An absent desired state
// is compared like any other: for a delete, observing nothing is exactly the
// confirmation wanted. What used to happen instead was a short circuit that
// returned "verified" without looking at the target at all.
//
// The retry is bounded and the backoff is capped. An upstream that never
// converges has to be reported rather than retried forever, because an
// operation stuck in executing holds a lease and blocks its own reconciliation.
func (e *Executor) verify(ctx context.Context, op *Operation, plan PlanResult) (json.RawMessage, bool, error) {
	desired := op.Desired
	if len(desired) == 0 {
		desired = plan.Desired
	}

	backoff := e.cfg.VerifyBackoff
	var observed json.RawMessage
	var lastErr error

	for attempt := range e.cfg.VerifyAttempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return observed, false, ctx.Err()
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, e.cfg.VerifyMaxBackoff)
		}

		obs, err := e.runner.Observe(ctx, op.Plugin, op.Action, op.Params)
		if err != nil {
			lastErr = err
			continue
		}
		observed = obs

		if CanonicalEqual(desired, observed) {
			return observed, true, nil
		}
		lastErr = fmt.Errorf("observed state does not yet match the requested state")
	}
	return observed, false, lastErr
}

// settle records a terminal or indeterminate outcome.
func (e *Executor) settle(ctx context.Context, op *Operation, attemptID string, to OperationState, observed json.RawMessage, verified *bool, upstreamRef, errCode, errDetail string) error {
	_, err := e.repo.Settle(ctx, SettleRequest{
		OperationID: op.ID,
		AttemptID:   attemptID,
		To:          to,
		Actor:       "system:executor",
		Verified:    verified,
		Observed:    observed,
		UpstreamRef: upstreamRef,
		ErrorCode:   errCode,
		ErrorDetail: errDetail,
		Audit: auditFor(e.ids.EventID(), "operation."+to.String(), op,
			"system:executor", StateExecuting, to,
			map[string]any{
				"verified":     verified,
				"upstream_ref": upstreamRef,
				"error_code":   errCode,
			}),
		Event: eventFor(e.ids.EventID(), subjectFor(to), op),
	})
	if err != nil {
		return err
	}
	e.notify()
	return nil
}

// settleFailure records a refusal discovered before any upstream call.
//
// The transition is from approved rather than executing, because nothing was
// claimed: refusing to start is not the same as starting and failing, and the
// audit trail should not suggest otherwise.
func (e *Executor) settleFailure(ctx context.Context, op *Operation, attemptID, code, detail string) error {
	_, err := e.repo.Transition(ctx, TransitionRequest{
		OperationID: op.ID,
		From:        StateApproved,
		To:          StateFailed,
		Actor:       "system:executor",
		Reason:      detail,
		Terminal:    true,
		ErrorCode:   code,
		ErrorDetail: detail,
		Audit: auditFor(e.ids.EventID(), "operation.failed", op,
			"system:executor", StateApproved, StateFailed,
			map[string]any{"error_code": code, "detail": detail}),
		Event: eventFor(e.ids.EventID(), subjectFor(StateFailed), op),
	})
	if errors.Is(err, ErrStateConflict) {
		// Something else already moved it. Nothing to do.
		return nil
	}
	if err != nil {
		return err
	}
	e.notify()
	return nil
}

func boolPtr(b bool) *bool { return &b }

// auditFor builds an audit entry outside the service.
func auditFor(eventID, kind string, op *Operation, actor string, from, to OperationState, detail map[string]any) AuditEntry {
	body, err := json.Marshal(detail)
	if err != nil {
		body = []byte(`{"error":"detail could not be encoded"}`)
	}
	return AuditEntry{
		EventID:       eventID,
		Kind:          kind,
		OperationID:   op.ID,
		Plugin:        op.Plugin,
		Action:        op.Action,
		Actor:         actor,
		FromState:     from,
		ToState:       to,
		Risk:          op.Risk,
		CorrelationID: op.CorrelationID,
		Detail:        body,
	}
}

// eventFor builds an outbox envelope outside the service.
func eventFor(eventID, subject string, op *Operation) OutboxEvent {
	payload, _ := json.Marshal(map[string]any{
		"operation_id": op.ID,
		"plugin":       op.Plugin,
		"action":       op.Action,
		"risk":         op.Risk,
	})
	return OutboxEvent{
		ID:            eventID,
		Subject:       subject,
		OperationID:   op.ID,
		CorrelationID: op.CorrelationID,
		Payload:       payload,
	}
}
