package operations

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/spoked/mcpd/internal/auth"
)

// Policy configures approval behaviour.
type Policy struct {
	// ProposalTTL bounds how long a proposal awaits a decision.
	ProposalTTL time.Duration
	// ApprovalTTL bounds how long an approval remains executable. Without it,
	// an approval granted weeks ago could still execute against a network that
	// has since changed.
	ApprovalTTL time.Duration
	// LeaseTTL bounds an execution claim before the reaper reclaims it.
	LeaseTTL time.Duration
	// InlineApproval bounds which changes may be approved from a conversation
	// rather than the dashboard.
	InlineApproval InlineApprovalPolicy
	// RiskOverrides raises the risk of specific actions beyond what the plugin
	// declared, keyed "plugin.action". It can only raise.
	RiskOverrides map[string]RiskLevel
}

// Service owns the approval workflow. It is the only thing that moves an
// operation through the state machine.
type Service struct {
	repo  Repository
	authz *ApprovalPolicy
	// policyFn is read on every use rather than captured, so a setting changed
	// at runtime applies to the next operation instead of the next restart.
	policyFn func() Policy
	log      *slog.Logger
	now      func() time.Time
	// notify wakes the outbox drain after a commit, so an approval reaches the
	// executor immediately rather than on the next poll.
	notify func()
	ids    IDGenerator
}

// IDGenerator produces identifiers. It is injectable so tests can be
// deterministic.
type IDGenerator interface {
	OperationID() string
	EventID() string
	AttemptID() string
}

// NewService builds the approval service.
func NewService(repo Repository, authz *ApprovalPolicy, policyFn func() Policy, log *slog.Logger, now func() time.Time, ids IDGenerator, notify func()) *Service {
	if now == nil {
		now = time.Now
	}
	if notify == nil {
		notify = func() {}
	}
	if policyFn == nil {
		policyFn = func() Policy { return Policy{} }
	}
	return &Service{
		repo: repo, authz: authz, policyFn: policyFn,
		log: log, now: now, ids: ids, notify: notify,
	}
}

// ProposeRequest describes a mutation a caller wants recorded.
type ProposeRequest struct {
	Plugin string
	Action string
	Risk   RiskLevel

	Target        json.RawMessage
	Params        json.RawMessage
	Before        json.RawMessage
	Desired       json.RawMessage
	Preconditions json.RawMessage
	Rollback      json.RawMessage
	Changes       []Change
	Impact        string
	// Verifiable is the mutation's declaration that its outcome can be
	// confirmed by re-reading the target. It travels with the proposal so the
	// executor reads it from the stored row rather than from the plugin it is
	// about to run.
	Verifiable bool

	// IdempotencyKey collapses repeated proposals of the same intent. When
	// empty it is derived from the payload hash, so a client that retries a
	// timed-out proposal does not create a second operation.
	IdempotencyKey string
	CorrelationID  string
}

// Propose records a mutation for approval. Nothing is changed upstream.
//
// This is the only entry point a plugin's propose tool reaches. It is
// deliberately not called "execute": the operation it returns is a record of
// intent, and the caller is expected to say so plainly to the model.
func (s *Service) Propose(ctx context.Context, p *auth.Principal, req ProposeRequest) (*Operation, error) {
	if d := s.authz.AuthorizeTool(p, req.Plugin, auth.CapPropose); !d.Allowed {
		s.log.Warn("proposal denied", "principal", p.ID, "plugin", req.Plugin,
			"action", req.Action, "code", d.Code, "reason", d.Reason)
		return nil, d.Error()
	}

	hash, err := PayloadHash(req.Plugin, req.Action, req.Target, req.Params)
	if err != nil {
		return nil, fmt.Errorf("operations: compute payload hash: %w", err)
	}

	// Risk may be raised by policy but never lowered, so a plugin cannot
	// declare its way out of an operator's classification.
	risk := MaxRisk(req.Risk, s.policyFn().RiskOverrides[req.Plugin+"."+req.Action])

	now := s.now()
	idem := req.IdempotencyKey
	if idem == "" {
		idem = hash
	}

	op := &Operation{
		ID:             s.ids.OperationID(),
		Plugin:         req.Plugin,
		Action:         req.Action,
		State:          StatePendingApproval,
		Risk:           risk,
		Target:         req.Target,
		Params:         req.Params,
		PayloadHash:    hash,
		Before:         req.Before,
		Desired:        req.Desired,
		Preconditions:  req.Preconditions,
		Rollback:       req.Rollback,
		Changes:        req.Changes,
		Impact:         req.Impact,
		Verifiable:     req.Verifiable,
		RequestedBy:    p.ID,
		RequestedAt:    now,
		ExpiresAt:      now.Add(s.policyFn().ProposalTTL),
		CorrelationID:  req.CorrelationID,
		IdempotencyKey: idem,
	}

	gc := s.guardContext(p, now)
	gc.RecomputedHash = hash
	if err := Validate(&Operation{
		State: StateDraft, PayloadHash: hash, ExpiresAt: op.ExpiresAt,
	}, StatePendingApproval, TriggerSubmit, gc); err != nil {
		return nil, err
	}

	stored, err := s.repo.Propose(ctx, RepoProposeRequest{
		Operation:   op,
		RequestHash: hash,
		Audit: s.audit("operation.proposed", op, p.ID, "", StatePendingApproval,
			map[string]any{
				"impact":  op.Impact,
				"changes": op.Changes,
				// Recorded at proposal so the trail says what this operation
				// could ever have proved, not only what it went on to prove.
				"assurance":     op.Assurance().String(),
				"verifiable":    op.Verifiable,
				"drift_checked": op.DriftChecked(),
			}),
		Event:          s.event(subjectFor(StatePendingApproval), op),
		IdempotencyTTL: s.policyFn().ProposalTTL * 2,
	})
	if err != nil {
		return nil, err
	}

	s.notify()
	s.log.Info("mutation proposed",
		"operation_id", stored.ID, "plugin", stored.Plugin, "action", stored.Action,
		"risk", stored.Risk, "principal", p.ID, "expires_at", stored.ExpiresAt)
	return stored, nil
}

// Approve authorizes a pending operation.
//
// It deliberately takes only an operation ID. Accepting mutation parameters
// here would let the approving call differ from the one that was reviewed,
// which is the entire failure mode the two-step flow exists to prevent.
func (s *Service) Approve(ctx context.Context, p *auth.Principal, operationID, reason string) (*Operation, error) {
	op, err := s.repo.Get(ctx, operationID)
	if err != nil {
		return nil, err
	}
	return s.approve(ctx, p, op, reason, "dashboard")
}

// ApproveInline records an approval given through the client rather than the
// dashboard.
//
// It is a distinct method from Approve because the audit trail should say
// which it was. The two carry different evidence: a dashboard approval was
// made by someone looking at the full before-and-after, while an inline one
// was a confirmation in a conversation. Both are human decisions; they are not
// the same decision.
func (s *Service) ApproveInline(ctx context.Context, p *auth.Principal, operationID string) (*Operation, error) {
	op, err := s.repo.Get(ctx, operationID)
	if err != nil {
		return nil, err
	}
	if !s.policyFn().InlineApproval.Allows(op.Risk) {
		return nil, &GuardError{
			ErrCode: CodeNotAuthorized,
			Detail: fmt.Sprintf(
				"a %s-risk change cannot be approved from a conversation; "+
					"approve it in the dashboard", op.Risk),
		}
	}
	return s.approve(ctx, p, op, "approved in conversation", "inline")
}

// Approve authorizes a pending operation from the dashboard or the approve
// tool.
func (s *Service) approve(ctx context.Context, p *auth.Principal, op *Operation, reason, channel string) (*Operation, error) {
	if d := s.authz.AuthorizeApproval(p, op); !d.Allowed {
		s.log.Warn("approval denied",
			"operation_id", op.ID, "principal", p.ID, "channel", channel,
			"code", d.Code, "reason", d.Reason)
		return nil, &GuardError{ErrCode: d.Code, Detail: d.Reason}
	}

	now := s.now()
	gc := s.guardContext(p, now)
	if err := Validate(op, StateApproved, TriggerApprove, gc); err != nil {
		return nil, err
	}

	approvalExpiry := now.Add(s.policyFn().ApprovalTTL)
	stored, err := s.repo.Transition(ctx, TransitionRequest{
		OperationID: op.ID,
		From:        StatePendingApproval,
		To:          StateApproved,
		Actor:       p.ID,
		Reason:      reason,
		Approval: &ApprovalFields{
			ApprovedBy:        p.ID,
			ApprovedAt:        now,
			ApprovalExpiresAt: approvalExpiry,
		},
		Audit: s.audit("operation.approved", op, p.ID, StatePendingApproval, StateApproved,
			map[string]any{
				"reason":     reason,
				"execute_by": approvalExpiry,
				// Recorded so the trail distinguishes a decision made at a
				// dashboard from one made in a chat window.
				"channel": channel,
			}),
		Event: s.event(subjectFor(StateApproved), op),
	})
	if err != nil {
		return nil, err
	}

	s.notify()
	s.log.Info("mutation approved",
		"operation_id", op.ID, "plugin", op.Plugin, "action", op.Action,
		"risk", op.Risk, "approver", p.ID, "requester", op.RequestedBy,
		"channel", channel, "execute_by", approvalExpiry)
	return stored, nil
}

// AwaitOutcome waits for an operation to leave the approved and executing
// states, so an inline approval can report what actually happened.
//
// It polls rather than subscribing because the caller is a single tool
// invocation with a short deadline, and a missed event would leave it hanging
// where a poll simply reports "still running".
func (s *Service) AwaitOutcome(ctx context.Context, operationID string, timeout time.Duration) (*Operation, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()

	var last *Operation
	for {
		op, err := s.repo.Get(ctx, operationID)
		if err != nil {
			return last, err
		}
		last = op
		if op.State != StateApproved && op.State != StateExecuting {
			return op, nil
		}
		select {
		case <-ctx.Done():
			// Not an error: the change is still running, and saying so is
			// more useful than failing.
			return last, nil
		case <-ticker.C:
		}
	}
}

// Reject refuses a pending operation.
func (s *Service) Reject(ctx context.Context, p *auth.Principal, operationID, reason string) (*Operation, error) {
	op, err := s.repo.Get(ctx, operationID)
	if err != nil {
		return nil, err
	}
	if d := s.authz.AuthorizeApproval(p, op); !d.Allowed {
		// Rejecting requires the same standing as approving. Anyone able to
		// veto a change is making a decision about it.
		return nil, &GuardError{ErrCode: d.Code, Detail: d.Reason}
	}
	if err := Validate(op, StateRejected, TriggerReject, s.guardContext(p, s.now())); err != nil {
		return nil, err
	}

	stored, err := s.repo.Transition(ctx, TransitionRequest{
		OperationID: op.ID,
		From:        StatePendingApproval,
		To:          StateRejected,
		Actor:       p.ID,
		Reason:      reason,
		Terminal:    true,
		Audit: s.audit("operation.rejected", op, p.ID, StatePendingApproval, StateRejected,
			map[string]any{"reason": reason}),
		Event: s.event(subjectFor(StateRejected), op),
	})
	if err != nil {
		return nil, err
	}
	s.notify()
	s.log.Info("mutation rejected",
		"operation_id", op.ID, "approver", p.ID, "reason", reason)
	return stored, nil
}

// Cancel withdraws an operation the caller proposed.
func (s *Service) Cancel(ctx context.Context, p *auth.Principal, operationID, reason string) (*Operation, error) {
	op, err := s.repo.Get(ctx, operationID)
	if err != nil {
		return nil, err
	}
	if d := s.authz.AuthorizeTool(p, op.Plugin, auth.CapPropose); !d.Allowed {
		return nil, d.Error()
	}
	if err := Validate(op, StateCancelled, TriggerCancel, s.guardContext(p, s.now())); err != nil {
		return nil, err
	}

	stored, err := s.repo.Transition(ctx, TransitionRequest{
		OperationID: op.ID,
		From:        op.State,
		To:          StateCancelled,
		Actor:       p.ID,
		Reason:      reason,
		Terminal:    true,
		Audit: s.audit("operation.cancelled", op, p.ID, op.State, StateCancelled,
			map[string]any{"reason": reason}),
		Event: s.event(subjectFor(StateCancelled), op),
	})
	if err != nil {
		return nil, err
	}
	s.notify()
	return stored, nil
}

// Get loads an operation the principal is allowed to see.
func (s *Service) Get(ctx context.Context, p *auth.Principal, operationID string) (*Operation, error) {
	op, err := s.repo.Get(ctx, operationID)
	if err != nil {
		return nil, err
	}
	// Visibility follows plugin access. An agent scoped to one integration
	// must not read operations belonging to another.
	if d := s.authz.AuthorizeEndpoint(p, op.Plugin); !d.Allowed {
		return nil, ErrNotFound
	}
	return op, nil
}

// List returns operations for a plugin the principal may reach.
func (s *Service) List(ctx context.Context, p *auth.Principal, plugin string, states []OperationState, limit int) ([]*Operation, error) {
	if d := s.authz.AuthorizeEndpoint(p, plugin); !d.Allowed {
		return nil, d.Error()
	}
	return s.repo.List(ctx, ListFilter{Plugin: plugin, States: states, Limit: limit})
}

// guardContext assembles the inputs every transition guard inspects.
func (s *Service) guardContext(p *auth.Principal, now time.Time) GuardContext {
	return GuardContext{
		Now:                now,
		Actor:              p.ID,
		PreconditionsMatch: true,
	}
}

// audit builds an audit entry. Detail is marshalled here rather than by
// callers so that a failure to encode cannot silently drop the record.
func (s *Service) audit(kind string, op *Operation, actor string, from, to OperationState, detail map[string]any) AuditEntry {
	body, err := json.Marshal(detail)
	if err != nil {
		body = []byte(`{"error":"detail could not be encoded"}`)
	}
	return AuditEntry{
		EventID:       s.ids.EventID(),
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

// event builds the outbox envelope.
//
// The payload is deliberately thin: an identifier and enough context to route
// on. A consumer needing more must read the database, which is the only place
// the truth lives.
func (s *Service) event(subject string, op *Operation) OutboxEvent {
	payload, _ := json.Marshal(map[string]any{
		"operation_id": op.ID,
		"plugin":       op.Plugin,
		"action":       op.Action,
		"risk":         op.Risk,
	})
	return OutboxEvent{
		ID:            s.ids.EventID(),
		Subject:       subject,
		OperationID:   op.ID,
		CorrelationID: op.CorrelationID,
		Payload:       payload,
	}
}

// PolicyForTest exposes the current policy so tests can assert it is read
// live rather than captured at construction.
func (s *Service) PolicyForTest() Policy { return s.policyFn() }
