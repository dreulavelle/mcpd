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
			if !strings.Contains(err.Error(), "cannot be changed") {
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
