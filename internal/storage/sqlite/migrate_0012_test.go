package sqlite

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/operations"
)

// A column added by ALTER and a trigger created beside it are two different
// shapes of change, and only one of them lands in the table definition. A
// database that upgraded into 0012 has to be indistinguishable from one that
// started there, or two deployments on the same version number disagree about
// what the schema is.
func TestMigrate0012_UpgradingMatchesAFreshDatabase(t *testing.T) {
	ctx := context.Background()

	fresh := openDBAt(t, "fresh12.db")
	if _, err := Migrate(ctx, fresh); err != nil {
		t.Fatalf("fresh migrate: %v", err)
	}

	upgraded := openDBAt(t, "upgraded12.db")
	applyThrough(t, upgraded, 11)
	seedOperation(t, upgraded, "op_before_0012")
	if _, err := Migrate(ctx, upgraded); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	if got, want := schemaOf(t, upgraded), schemaOf(t, fresh); got != want {
		t.Errorf("an upgraded database does not match a fresh one\n--- upgraded ---\n%s\n--- fresh ---\n%s",
			got, want)
	}
}

// An operation that existed before the column did was approved by a person, so
// the column answering "which rule" is correctly empty for it. Reading it back
// must not turn that absence into a rule nobody wrote.
func TestMigrate0012_ExistingOperationsCarryNoRule(t *testing.T) {
	ctx := context.Background()
	db := openDBAt(t, "carried.db")
	applyThrough(t, db, 11)
	seedOperation(t, db, "op_before_0012")
	if _, err := Migrate(ctx, db); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	op, err := NewOperationStore(db, time.Now).Get(ctx, "op_before_0012")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if op.AuthorizedByRule != "" {
		t.Errorf("authorized_by_rule = %q; an operation from before this column "+
			"was approved by a person", op.AuthorizedByRule)
	}
	if op.AutoApproved() {
		t.Error("an operation from before this column must not read as auto-approved")
	}
}

// The rule is written in the same guarded statement as the approval, and it
// survives the round trip. Both halves matter: an operation approved with no
// account of what authorised it is the thing the column exists to prevent.
func TestOperationStore_RecordsTheAuthorisingRuleWithTheApproval(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)

	op := proposeOp(t, store, "op_rule")
	now := testClock

	approved, err := store.Transition(ctx, operations.TransitionRequest{
		OperationID: op.ID,
		From:        operations.StatePendingApproval,
		To:          operations.StateApproved,
		Actor:       operations.PolicyActor,
		Reason:      "rule routine-radio authorises this",
		Approval: &operations.ApprovalFields{
			ApprovedBy:        operations.PolicyActor,
			ApprovedAt:        now,
			ApprovalExpiresAt: now.Add(15 * time.Minute),
			AuthorizedByRule:  "routine-radio",
		},
		Audit: operations.AuditEntry{
			EventID: "aud-rule-" + op.ID, Kind: "operation.approved",
			OperationID: op.ID, Actor: operations.PolicyActor,
			FromState: operations.StatePendingApproval,
			ToState:   operations.StateApproved,
		},
		Event: operations.OutboxEvent{
			ID: "evt-rule-" + op.ID, Subject: "mcp.operation.approved",
			OperationID: op.ID, Payload: json.RawMessage(`{}`),
		},
	})
	if err != nil {
		t.Fatalf("transition: %v", err)
	}
	if approved.AuthorizedByRule != "routine-radio" {
		t.Fatalf("authorized_by_rule = %q, want routine-radio", approved.AuthorizedByRule)
	}
	if !approved.AutoApproved() {
		t.Error("an operation carrying a rule is one nobody was asked about")
	}
}

// Fabricating provenance was easier than editing it.
//
// The first version of the trigger guarded only a row that already carried a
// rule, so a human-approved operation -- which holds NULL -- could be handed
// one by a bare UPDATE. It then read as auto-approved, nothing recorded the
// change, and the hash chain still verified, because the column is outside it.
//
// The property is not "it cannot be changed once set". It is "it can only be
// set by the approval it belongs to".
func TestOperationStore_TheAuthorisingRuleCannotBeFabricated(t *testing.T) {
	ctx := context.Background()
	store, db := newStore(t)
	audit := NewAuditStore(db)

	op := proposeOp(t, store, "op_human")
	now := testClock
	if _, err := store.Transition(ctx, operations.TransitionRequest{
		OperationID: op.ID,
		From:        operations.StatePendingApproval,
		To:          operations.StateApproved,
		Actor:       "user:bob",
		Approval: &operations.ApprovalFields{
			ApprovedBy:        "user:bob",
			ApprovedAt:        now,
			ApprovalExpiresAt: now.Add(15 * time.Minute),
		},
		Audit: operations.AuditEntry{
			EventID: "aud-human-" + op.ID, Kind: "operation.approved",
			OperationID: op.ID, Actor: "user:bob",
			FromState: operations.StatePendingApproval,
			ToState:   operations.StateApproved,
		},
		Event: operations.OutboxEvent{
			ID: "evt-human-" + op.ID, Subject: "mcp.operation.approved",
			OperationID: op.ID, Payload: json.RawMessage(`{}`),
		},
	}); err != nil {
		t.Fatalf("approve: %v", err)
	}

	if _, err := db.Writer().ExecContext(ctx,
		`UPDATE operations SET authorized_by_rule = ? WHERE id = ?`,
		"fabricated-rule", op.ID); err == nil {
		t.Fatal("a human-approved operation must not be handed a rule after the fact")
	} else if !strings.Contains(err.Error(), "only be set by the approval") {
		t.Errorf("refused for the wrong reason: %v", err)
	}

	after, err := store.Get(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.AutoApproved() {
		t.Fatalf("the operation now reads as authorised by %q; a person approved it",
			after.AuthorizedByRule)
	}
	if after.ApprovedBy != "user:bob" {
		t.Errorf("approved_by = %q, want user:bob", after.ApprovedBy)
	}

	// The chain is what proves the approval, and it must still be intact --
	// the point is that the copy cannot drift from it, not that a broken
	// chain is how the drift is noticed.
	if broken, err := audit.VerifyChain(ctx); err != nil || broken != 0 {
		t.Errorf("chain broken at %d: %v", broken, err)
	}
}

// A pending operation cannot be handed a rule either. The column may only move
// in the statement that performs the approval, so a write that leaves the
// state alone is refused whatever the state happens to be.
func TestOperationStore_TheAuthorisingRuleCannotBeSetWithoutApproving(t *testing.T) {
	ctx := context.Background()
	store, db := newStore(t)
	op := proposeOp(t, store, "op_pending")

	if _, err := db.Writer().ExecContext(ctx,
		`UPDATE operations SET authorized_by_rule = ? WHERE id = ?`,
		"invented", op.ID); err == nil {
		t.Fatal("a pending operation must not carry a rule; nothing has authorised it")
	}

	after, _ := store.Get(ctx, op.ID)
	if after.AuthorizedByRule != "" {
		t.Fatalf("authorized_by_rule = %q, want empty", after.AuthorizedByRule)
	}
}

// The record of what authorised a change is history. A value that could be
// rewritten afterwards would let that account be edited after the fact, which
// is the same as not having it.
func TestOperationStore_TheAuthorisingRuleCannotBeRewritten(t *testing.T) {
	ctx := context.Background()
	store, db := newStore(t)

	op := proposeOp(t, store, "op_frozen")
	now := testClock
	if _, err := store.Transition(ctx, operations.TransitionRequest{
		OperationID: op.ID,
		From:        operations.StatePendingApproval,
		To:          operations.StateApproved,
		Actor:       operations.PolicyActor,
		Approval: &operations.ApprovalFields{
			ApprovedBy:        operations.PolicyActor,
			ApprovedAt:        now,
			ApprovalExpiresAt: now.Add(15 * time.Minute),
			AuthorizedByRule:  "routine-radio",
		},
		Audit: operations.AuditEntry{
			EventID: "aud-frozen-" + op.ID, Kind: "operation.approved",
			OperationID: op.ID, Actor: operations.PolicyActor,
			FromState: operations.StatePendingApproval,
			ToState:   operations.StateApproved,
		},
		Event: operations.OutboxEvent{
			ID: "evt-frozen-" + op.ID, Subject: "mcp.operation.approved",
			OperationID: op.ID, Payload: json.RawMessage(`{}`),
		},
	}); err != nil {
		t.Fatalf("transition: %v", err)
	}

	for _, attempt := range []struct {
		name  string
		value any
	}{
		{"renaming the rule", "something-else"},
		{"clearing it", nil},
	} {
		t.Run(attempt.name, func(t *testing.T) {
			_, err := db.Writer().ExecContext(ctx,
				`UPDATE operations SET authorized_by_rule = ? WHERE id = ?`,
				attempt.value, op.ID)
			if err == nil {
				t.Fatal("the database must refuse this")
			}
			if !strings.Contains(err.Error(), "only be set by the approval") {
				t.Errorf("refused for the wrong reason: %v", err)
			}
		})
	}
}

// seedOperation writes a minimal operations row directly, so a pre-0012
// database has something in the table when the column is added.
func seedOperation(t *testing.T, db *DB, id string) {
	t.Helper()
	now := time.Now().UnixMilli()
	if _, err := db.Writer().ExecContext(context.Background(), `
		INSERT INTO operations (
			id, plugin, action, state, risk,
			target_json, params_json, payload_hash,
			impact, requested_by, requested_at, expires_at,
			correlation_id, idempotency_key
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, "cnmaestro", "device.set_radio_channel", "pending_approval", "low",
		`{"mac":"AA:BB"}`, `{"channel":"149"}`, "hash-"+id,
		"changes a channel", "user:alice", now, now+900000,
		"corr-"+id, "idem-"+id); err != nil {
		t.Fatalf("seed operation: %v", err)
	}
}
