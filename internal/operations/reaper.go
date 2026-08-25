package operations

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// Reaper enforces the deadlines the state machine depends on.
//
// Every timeout in this system is enforced here rather than by a timer held in
// memory, so a restart does not lose them and a clock skew does not silently
// extend an approval.
type Reaper struct {
	repo   Repository
	log    *slog.Logger
	now    func() time.Time
	ids    IDGenerator
	notify func()

	interval time.Duration
	batch    int
}

// NewReaper builds the deadline enforcer.
func NewReaper(repo Repository, log *slog.Logger, now func() time.Time, ids IDGenerator, notify func(), interval time.Duration) *Reaper {
	if now == nil {
		now = time.Now
	}
	if notify == nil {
		notify = func() {}
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &Reaper{
		repo: repo, log: log, now: now, ids: ids, notify: notify,
		interval: interval, batch: 100,
	}
}

// Run sweeps until ctx is cancelled.
func (r *Reaper) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	// Sweep immediately: a restart is exactly when deadlines are most likely
	// to have passed unnoticed.
	r.Sweep(ctx)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			r.Sweep(ctx)
		}
	}
}

// Sweep processes every operation whose deadline has passed.
func (r *Reaper) Sweep(ctx context.Context) {
	due, err := r.repo.DueForExpiry(ctx, r.now(), r.batch)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			r.log.ErrorContext(ctx, "reaper could not read expiring operations", "error", err)
		}
		return
	}
	for _, op := range due {
		if ctx.Err() != nil {
			return
		}
		r.reap(ctx, op)
	}
}

// reap acts on one overdue operation.
func (r *Reaper) reap(ctx context.Context, op *Operation) {
	switch op.State {
	case StatePendingApproval:
		r.expire(ctx, op, "the proposal expired before anyone approved it")

	case StateApproved:
		// An approval that was never executed. Letting it stand would mean an
		// approval granted long ago could still fire against a network that
		// has changed since.
		r.expire(ctx, op, "the approval expired before the change was applied")

	case StateExecuting:
		// The lease lapsed with no settlement, which means the worker holding
		// it died mid-execution. Whether the upstream write landed is
		// genuinely unknown, so this must not be recorded as a failure: a
		// failure invites a retry, and a retry would double-apply.
		r.log.ErrorContext(ctx, "execution lease expired without settlement; outcome unknown",
			"operation_id", op.ID, "plugin", op.Plugin, "action", op.Action,
			"lease_owner", op.LeaseOwner, "attempts", op.AttemptCount)

		_, err := r.repo.Settle(ctx, SettleRequest{
			OperationID: op.ID,
			To:          StateIndeterminate,
			Actor:       "system:reaper",
			Reason:      "execution lease expired without settlement",
			ErrorCode:   CodeLeaseExpired,
			ErrorDetail: "the worker holding this operation stopped before recording an outcome, " +
				"so whether the change was applied is unknown",
			Audit: auditFor(r.ids.EventID(), "operation.indeterminate", op,
				"system:reaper", StateExecuting, StateIndeterminate,
				map[string]any{"lease_owner": op.LeaseOwner}),
			Event: eventFor(r.ids.EventID(), subjectFor(StateIndeterminate), op),
		})
		if err != nil && !errors.Is(err, ErrStateConflict) {
			r.log.ErrorContext(ctx, "failed to record an expired lease", "operation_id", op.ID, "error", err)
			return
		}
		r.notify()
	}
}

// expire moves an overdue operation to expired.
func (r *Reaper) expire(ctx context.Context, op *Operation, reason string) {
	_, err := r.repo.Transition(ctx, TransitionRequest{
		OperationID: op.ID,
		From:        op.State,
		To:          StateExpired,
		Actor:       "system:reaper",
		Reason:      reason,
		Terminal:    true,
		ErrorCode:   expiryCode(op.State),
		ErrorDetail: reason,
		Audit: auditFor(r.ids.EventID(), "operation.expired", op,
			"system:reaper", op.State, StateExpired,
			map[string]any{"reason": reason}),
		Event: eventFor(r.ids.EventID(), subjectFor(StateExpired), op),
	})
	if errors.Is(err, ErrStateConflict) {
		// Someone approved, rejected, or cancelled it between the scan and
		// the write. That is the guard working.
		return
	}
	if err != nil {
		r.log.ErrorContext(ctx, "failed to expire an operation", "operation_id", op.ID, "error", err)
		return
	}
	r.log.InfoContext(ctx, "operation expired",
		"operation_id", op.ID, "plugin", op.Plugin, "action", op.Action,
		"previous_state", op.State)
	r.notify()
}

func expiryCode(from OperationState) string {
	if from == StateApproved {
		return CodeApprovalExpired
	}
	return CodeProposalExpired
}
