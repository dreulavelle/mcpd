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
	// AutoApprove is the standing-rule set that decides whether a person is
	// asked at all. Its zero value asks about everything, which is what an
	// unconfigured deployment must keep doing.
	AutoApprove AutoApprovalPolicy
	// Bypass is a window somebody opened to stop being asked, or nil. It is
	// consulted only after the rules have declined, and it cannot override an
	// exclusion; see applyBypass.
	Bypass *Bypass
}

// applyBypass lets an open window authorise a change the rules declined.
//
// Only ever consulted after the rules have had their say, and never allowed to
// reverse an exclusion. A rule that authorises nothing is somebody writing
// "never" about a specific action; a window opened to get through an evening
// must not cancel it, or the carve-out an operator wrote most deliberately
// would be the one most easily lost.
//
// A decision the rules already made is returned untouched, so a bypass can
// never make a change *less* likely to be approved, and the reason recorded
// when a rule authorised something still names that rule.
func applyBypass(d AutoApprovalDecision, b *Bypass, req AutoApprovalRequest, now time.Time) AutoApprovalDecision {
	if d.AutoApprove || b == nil || d.Excluded() {
		return d
	}
	covers, reason := b.Covers(req, now)
	if !covers {
		return d
	}
	return AutoApprovalDecision{AutoApprove: true, Bypass: b, Reason: reason}
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
	// Reversible is the mutation's declaration that an inverse exists. It is
	// read only by the auto-approval policy, which refuses to authorise a
	// change nothing can undo: the case for a standing authorisation is that a
	// mistake is cheap to correct, and it does not survive the absence of a
	// correction.
	Reversible bool

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
		s.log.WarnContext(ctx, "proposal denied", "principal", p.ID, "plugin", req.Plugin,
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

	// A classification this build does not define is refused here rather than
	// carried further. MaxRisk ranks an unknown level above every known one,
	// so it arrives having already outranked everything -- which is the right
	// direction, and also means nothing downstream can make sense of it: the
	// schema's CHECK would refuse the insert with a constraint error naming no
	// cause, and coercing it to critical would be this host inventing a meaning
	// for a word a plugin chose.
	//
	// Saying so plainly is the whole of the fix. It is reachable from any
	// plugin that returns a risk override -- an out-of-process one that knows a
	// level this host does not, or simply a typo -- and the failure has to be
	// legible to whoever has to correct it.
	if !risk.Valid() {
		return nil, &GuardError{
			ErrCode: CodeInvalidRisk,
			Detail: fmt.Sprintf(
				"%s classified %s as %q, which is not a risk level this host defines; "+
					"nothing can be authorised against a classification it cannot read",
				req.Plugin, req.Action, risk),
		}
	}

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

	gc := s.guardContext(p.ID, now)
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
				// Recorded because it is what the auto-approval policy refuses
				// on, and a reader asking why a change was put to a person
				// should not have to go to the plugin's source to find out.
				"reversible": req.Reversible,
			}),
		Event:          s.event(subjectFor(StatePendingApproval), op),
		IdempotencyTTL: s.policyFn().ProposalTTL * 2,
	})
	if err != nil {
		return nil, err
	}

	s.notify()
	s.log.InfoContext(ctx, "mutation proposed",
		"operation_id", stored.ID, "plugin", stored.Plugin, "action", stored.Action,
		"risk", stored.Risk, "principal", p.ID, "expires_at", stored.ExpiresAt)

	// Whether a person is asked is decided here, after the record exists.
	// Proposing first is not an ordering detail: a change that is refused,
	// declined, or interrupted still has to leave a durable account of what
	// was proposed, and a decision taken before the row was written would
	// sometimes have nothing to attach itself to.
	return s.autoApprove(ctx, stored, req.Reversible), nil
}

// autoApprove authorises an operation from a standing rule when one covers it.
//
// It never returns an error. A rule that does not apply, a rule set that
// cannot be read, a transition lost to a race -- all of them leave the
// operation exactly where Propose put it, awaiting a person, which is the
// direction to fail in. The caller gets the operation as it now stands.
func (s *Service) autoApprove(ctx context.Context, op *Operation, reversible bool) *Operation {
	// A replayed proposal returns the original operation, which may already be
	// approved, executing or settled. Only a proposal still awaiting a
	// decision is one this can decide.
	if op.State != StatePendingApproval {
		return op
	}

	policy := s.policyFn()
	decision := policy.AutoApprove.Evaluate(AutoApprovalRequest{
		Plugin:     op.Plugin,
		Action:     op.Action,
		Principal:  op.RequestedBy,
		Risk:       op.Risk,
		Reversible: reversible,
	})
	now := s.now()
	decision = applyBypass(decision, policy.Bypass, AutoApprovalRequest{
		Plugin: op.Plugin, Action: op.Action, Principal: op.RequestedBy,
		Risk: op.Risk, Reversible: reversible,
	}, now)

	if !decision.AutoApprove {
		s.log.DebugContext(ctx, "this change is being put to a person",
			"operation_id", op.ID, "plugin", op.Plugin, "action", op.Action,
			"risk", op.Risk, "reason", decision.Reason)
		return op
	}
	if decision.Bypass != nil {
		// Warn, not info. A change running because somebody switched the
		// asking off is the line in the log that explains an approval nobody
		// remembers giving.
		s.log.WarnContext(ctx, "a change was authorised by a bypass rather than by a rule",
			"operation_id", op.ID, "plugin", op.Plugin, "action", op.Action,
			"risk", op.Risk, "bypass", decision.Bypass.ID,
			"opened_by", decision.Bypass.CreatedBy, "expires", decision.Bypass.ExpiresAt)
	}
	// The same guard every approval passes. A standing rule decides who
	// authorises, not what may be authorised: an expired proposal is still
	// expired, and the transition table is still the transition table.
	//
	// AuthorizeApproval is deliberately not consulted. It asks whether this
	// principal may approve, and no principal is approving -- the authority is
	// an administrator's rule, and the proposer's own right to approve is
	// beside the point. What bounds the proposer is CapPropose, checked above.
	if err := Validate(op, StateApproved, TriggerApprove, s.guardContext(PolicyActor, now)); err != nil {
		s.log.WarnContext(ctx, "this change is authorised but cannot be approved",
			"operation_id", op.ID, "authority", decision.Authority(), "error", err)
		return op
	}

	// The same snapshot the decision was taken from. Reading the policy again
	// here could pair a rule that has since been deleted with a TTL that has
	// since changed.
	approvalExpiry := now.Add(policy.ApprovalTTL)
	stored, err := s.repo.Transition(ctx, TransitionRequest{
		OperationID: op.ID,
		From:        StatePendingApproval,
		To:          StateApproved,
		Actor:       PolicyActor,
		Reason:      decision.Reason,
		Approval: &ApprovalFields{
			ApprovedBy:        PolicyActor,
			ApprovedAt:        now,
			ApprovalExpiresAt: approvalExpiry,
			AuthorizedByRule:  decision.Authority(),
		},
		Audit: s.audit("operation.approved", op, PolicyActor, StatePendingApproval, StateApproved,
			policyAudit(decision, op, approvalExpiry)),
		Event: s.event(subjectFor(StateApproved), op),
	})
	if err != nil {
		// Losing the race is ordinary: a person got there first, or the reaper
		// expired the proposal. Either way the row is the authority and it
		// says what happened -- so re-read it rather than handing back the
		// copy from before the race, which would report pending_approval for
		// an operation that is by now approved, expired or running.
		s.log.WarnContext(ctx, "this change is authorised but the approval did not land",
			"operation_id", op.ID, "authority", decision.Authority(), "error", err)
		if fresh, readErr := s.repo.Get(ctx, op.ID); readErr == nil {
			return fresh
		}
		return op
	}

	s.notify()
	s.log.InfoContext(ctx, "mutation approved without being asked about",
		"operation_id", stored.ID, "plugin", stored.Plugin, "action", stored.Action,
		"risk", stored.Risk, "authority", decision.Authority(), "reason", decision.Reason,
		"requester", stored.RequestedBy, "execute_by", approvalExpiry)
	return stored
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

// ApproveInline records an approval given through a client's own confirmation
// prompt rather than a deliberate call to the approve tool.
//
// It is a distinct method from Approve because the audit trail should say
// which it was. The two carry different evidence: an explicit approval was
// made by someone who had been shown the full before-and-after, while an
// inline one was a yes/no answered in the flow of a conversation. Both are
// human decisions made in the same place; they are not the same decision.
//
// The refusal above the ceiling withholds the shortcut, never the decision:
// the person still settles it here, by being shown the change and telling the
// assistant to go ahead. Nothing in this package requires anyone to open the
// dashboard, and nothing should -- an approval that costs a context switch is
// one people arrange not to need.
func (s *Service) ApproveInline(ctx context.Context, p *auth.Principal, operationID string) (*Operation, error) {
	op, err := s.repo.Get(ctx, operationID)
	if err != nil {
		return nil, err
	}
	if !s.policyFn().InlineApproval.Allows(op.Risk) {
		return nil, &GuardError{
			ErrCode: CodeNotAuthorized,
			Detail: fmt.Sprintf(
				"a %s-risk change cannot be settled by a yes/no prompt; show the "+
					"person what will change, in full, and call approve_operation "+
					"once they say to go ahead", op.Risk),
		}
	}
	return s.approve(ctx, p, op, "approved in conversation", "inline")
}

// Approve authorizes a pending operation from the dashboard or the approve
// tool.
func (s *Service) approve(ctx context.Context, p *auth.Principal, op *Operation, reason, channel string) (*Operation, error) {
	if d := s.authz.AuthorizeApproval(p, op); !d.Allowed {
		s.log.WarnContext(ctx, "approval denied",
			"operation_id", op.ID, "principal", p.ID, "channel", channel,
			"code", d.Code, "reason", d.Reason)
		return nil, &GuardError{ErrCode: d.Code, Detail: d.Reason}
	}

	now := s.now()
	gc := s.guardContext(p.ID, now)
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
	s.log.InfoContext(ctx, "mutation approved",
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
	if err := Validate(op, StateRejected, TriggerReject, s.guardContext(p.ID, s.now())); err != nil {
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
	s.log.InfoContext(ctx, "mutation rejected",
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
	if err := Validate(op, StateCancelled, TriggerCancel, s.guardContext(p.ID, s.now())); err != nil {
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
//
// It takes an actor rather than a principal because not every transition has
// one: a standing rule approves as "system:policy", and there is no principal
// behind that to hand in.
func (s *Service) guardContext(actor string, now time.Time) GuardContext {
	return GuardContext{
		Now:                now,
		Actor:              actor,
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

// policyAudit describes an approval nobody was asked to make.
//
// What authorised it is recorded in full rather than only by name, whether
// that was a rule or this host's own default. Both can be edited or deleted,
// and an entry naming something whose meaning has since changed would describe
// an authorisation that never happened. This entry stays true on its own.
func policyAudit(d AutoApprovalDecision, op *Operation, executeBy time.Time) map[string]any {
	detail := map[string]any{
		"reason":     d.Reason,
		"execute_by": executeBy,
		// "policy" rather than "dashboard" or "inline": the trail has to
		// distinguish a decision somebody made from one nobody was asked to
		// make.
		"channel":        "policy",
		"authority":      d.Authority(),
		"proposed_by":    op.RequestedBy,
		"asked_a_person": false,
	}
	if d.Rule != nil {
		detail["rule"] = d.Rule.ID
		detail["rule_scope"] = d.Rule.Scope()
		detail["rule_max_risk"] = d.Rule.MaxRisk.String()
		detail["rule_note"] = d.Rule.Note
	}
	return detail
}
