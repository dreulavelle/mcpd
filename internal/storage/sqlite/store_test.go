package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/operations"
	"github.com/spoked/mcpd/internal/storage"
)

var testClock = time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

func newTestDB(t *testing.T) *DB {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, Options{
		Path: filepath.Join(t.TempDir(), "test.db"),
		// Durability is verified separately; test fixtures do not need an
		// fsync per commit.
		RelaxedDurability: true,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func newStore(t *testing.T) (*OperationStore, *DB) {
	db := newTestDB(t)
	return NewOperationStore(db, func() time.Time { return testClock }), db
}

func proposeOp(t *testing.T, s *OperationStore, id string) *operations.Operation {
	t.Helper()
	target := json.RawMessage(`{"mac":"AA:BB:CC:DD:EE:FF","radio_id":2}`)
	params := json.RawMessage(`{"channel":"149"}`)
	hash, err := operations.PayloadHash("cnmaestro", "device.set_radio_channel", target, params)
	if err != nil {
		t.Fatal(err)
	}
	op := &operations.Operation{
		ID:             id,
		Plugin:         "cnmaestro",
		Action:         "device.set_radio_channel",
		State:          operations.StatePendingApproval,
		Risk:           operations.RiskMedium,
		Target:         target,
		Params:         params,
		PayloadHash:    hash,
		Before:         json.RawMessage(`{"channel":"36"}`),
		Desired:        json.RawMessage(`{"channel":"149"}`),
		Preconditions:  json.RawMessage(`{"channel":"36"}`),
		Changes:        []operations.Change{{Field: "channel", From: "36", To: "149"}},
		Impact:         "Clients may briefly disconnect while the radio changes channel.",
		RequestedBy:    "user:alice",
		RequestedAt:    testClock,
		ExpiresAt:      testClock.Add(time.Hour),
		CorrelationID:  "corr-" + id,
		IdempotencyKey: "idem-" + id,
	}
	got, err := s.Propose(context.Background(), storage.ProposeRequest{
		Operation:   op,
		RequestHash: hash,
		Audit: storage.AuditEntry{
			EventID: "aud-" + id, Kind: "operation.proposed", OperationID: id,
			Plugin: op.Plugin, Action: op.Action, Actor: op.RequestedBy,
			ToState: op.State, Risk: op.Risk, CorrelationID: op.CorrelationID,
		},
		Event: storage.Event{
			ID: "evt-" + id, Subject: "mcp.operation.proposed",
			OperationID: id, CorrelationID: op.CorrelationID,
			Payload: json.RawMessage(`{"operation_id":"` + id + `"}`),
		},
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	return got
}

func approve(t *testing.T, s *OperationStore, id, approver string) (*operations.Operation, error) {
	t.Helper()
	return s.Transition(context.Background(), storage.TransitionRequest{
		OperationID: id,
		From:        operations.StatePendingApproval,
		To:          operations.StateApproved,
		Actor:       approver,
		Approval: &storage.ApprovalFields{
			ApprovedBy:        approver,
			ApprovedAt:        testClock,
			ApprovalExpiresAt: testClock.Add(15 * time.Minute),
		},
		Audit: storage.AuditEntry{
			EventID: "aud-approve-" + approver + "-" + id, Kind: "operation.approved",
			OperationID: id, Actor: approver, FromState: operations.StatePendingApproval,
			ToState: operations.StateApproved, CorrelationID: "corr-" + id,
		},
		Event: storage.Event{
			ID: "evt-approve-" + approver + "-" + id, Subject: "mcp.operation.approved",
			OperationID: id, CorrelationID: "corr-" + id,
			Payload: json.RawMessage(`{}`),
		},
	})
}

func TestPropose_WritesOperationTransitionAuditAndOutboxAtomically(t *testing.T) {
	s, db := newStore(t)
	op := proposeOp(t, s, "op_1")

	if op.State != operations.StatePendingApproval {
		t.Fatalf("state = %s, want pending_approval", op.State)
	}
	if len(op.Changes) != 1 || op.Changes[0].Field != "channel" {
		t.Fatalf("changes did not round-trip: %+v", op.Changes)
	}

	for _, c := range []struct {
		name  string
		query string
	}{
		{"transition", `SELECT COUNT(*) FROM operation_transitions WHERE operation_id='op_1'`},
		{"audit", `SELECT COUNT(*) FROM audit_events WHERE operation_id='op_1'`},
		{"outbox", `SELECT COUNT(*) FROM outbox_events WHERE operation_id='op_1'`},
	} {
		var n int
		if err := db.Reader().QueryRow(c.query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%s rows = %d, want 1", c.name, n)
		}
	}
}

// Two approvers racing must produce exactly one approval. The loser sees a
// state conflict rather than a second successful transition.
func TestTransition_ConcurrentApprovalYieldsExactlyOneWinner(t *testing.T) {
	s, db := newStore(t)
	proposeOp(t, s, "op_race")

	const approvers = 8
	var wins, conflicts atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := range approvers {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			_, err := approve(t, s, "op_race", "user:approver"+string(rune('a'+n)))
			switch {
			case err == nil:
				wins.Add(1)
			case errors.Is(err, storage.ErrStateConflict):
				conflicts.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if wins.Load() != 1 {
		t.Fatalf("approvals that succeeded = %d, want exactly 1", wins.Load())
	}
	if conflicts.Load() != approvers-1 {
		t.Fatalf("state conflicts = %d, want %d", conflicts.Load(), approvers-1)
	}

	// Exactly one transition row into approved must exist.
	var n int
	if err := db.Reader().QueryRow(
		`SELECT COUNT(*) FROM operation_transitions WHERE operation_id='op_race' AND to_state='approved'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("approved transition rows = %d, want 1", n)
	}
}

// An approved operation must execute at most once even under concurrent
// claims. This is the invariant that prevents a double-applied mutation.
func TestClaim_ConcurrentClaimsYieldExactlyOneExecutor(t *testing.T) {
	s, db := newStore(t)
	op := proposeOp(t, s, "op_claim")
	if _, err := approve(t, s, "op_claim", "user:bob"); err != nil {
		t.Fatal(err)
	}

	const workers = 8
	var claimed, lost atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := range workers {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := string(rune('a' + n))
			<-start
			_, err := s.Claim(context.Background(), storage.ClaimRequest{
				OperationID:    "op_claim",
				ExpectedHash:   op.PayloadHash,
				InstanceID:     "mcpd-" + id,
				LeaseExpiresAt: testClock.Add(time.Minute),
				AttemptID:      "attempt-" + id,
				Audit: storage.AuditEntry{
					EventID: "aud-claim-" + id, Kind: "operation.executing",
					OperationID: "op_claim", Actor: "mcpd-" + id,
					CorrelationID: "corr-op_claim",
				},
				Event: storage.Event{
					ID: "evt-claim-" + id, Subject: "mcp.operation.executing",
					OperationID: "op_claim", Payload: json.RawMessage(`{}`),
				},
			})
			switch {
			case err == nil:
				claimed.Add(1)
			case errors.Is(err, operations.ErrClaimLost):
				lost.Add(1)
			default:
				t.Errorf("unexpected claim error: %v", err)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if claimed.Load() != 1 {
		t.Fatalf("successful claims = %d, want exactly 1", claimed.Load())
	}
	if lost.Load() != workers-1 {
		t.Fatalf("lost claims = %d, want %d", lost.Load(), workers-1)
	}

	// attempt_count must reflect the single successful claim, not the races.
	final, err := s.Get(context.Background(), "op_claim")
	if err != nil {
		t.Fatal(err)
	}
	if final.AttemptCount != 1 {
		t.Fatalf("attempt_count = %d, want 1 (a lost claim must not inflate it)", final.AttemptCount)
	}
	var attempts int
	if err := db.Reader().QueryRow(
		`SELECT COUNT(*) FROM execution_attempts WHERE operation_id='op_claim'`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("execution_attempts rows = %d, want 1", attempts)
	}
}

func TestClaim_RefusesTamperedPayloadHash(t *testing.T) {
	s, _ := newStore(t)
	proposeOp(t, s, "op_tamper")
	if _, err := approve(t, s, "op_tamper", "user:bob"); err != nil {
		t.Fatal(err)
	}

	_, err := s.Claim(context.Background(), storage.ClaimRequest{
		OperationID:    "op_tamper",
		ExpectedHash:   "not-the-stored-hash",
		InstanceID:     "mcpd-01",
		LeaseExpiresAt: testClock.Add(time.Minute),
		AttemptID:      "attempt-1",
		Audit:          storage.AuditEntry{EventID: "a1", Kind: "x", Actor: "sys", CorrelationID: "c"},
		Event:          storage.Event{ID: "e1", Subject: "s", Payload: json.RawMessage(`{}`)},
	})
	if !errors.Is(err, operations.ErrClaimLost) {
		t.Fatalf("expected claim to be refused, got %v", err)
	}
}

func TestClaim_RefusesExpiredApproval(t *testing.T) {
	db := newTestDB(t)
	clock := testClock
	s := NewOperationStore(db, func() time.Time { return clock })

	op := proposeOp(t, s, "op_stale")
	if _, err := approve(t, s, "op_stale", "user:bob"); err != nil {
		t.Fatal(err)
	}

	// Advance past the 15-minute approval window.
	clock = testClock.Add(time.Hour)

	_, err := s.Claim(context.Background(), storage.ClaimRequest{
		OperationID:    "op_stale",
		ExpectedHash:   op.PayloadHash,
		InstanceID:     "mcpd-01",
		LeaseExpiresAt: clock.Add(time.Minute),
		AttemptID:      "attempt-1",
		Audit:          storage.AuditEntry{EventID: "a1", Kind: "x", Actor: "sys", CorrelationID: "c"},
		Event:          storage.Event{ID: "e1", Subject: "s", Payload: json.RawMessage(`{}`)},
	})
	if !errors.Is(err, operations.ErrClaimLost) {
		t.Fatalf("an approval past its execute-by deadline must not be claimable, got %v", err)
	}
}

// The payload must be unwritable after submission regardless of what calling
// code attempts, which is why the guarantee lives in a trigger.
func TestPayloadImmutability_EnforcedByDatabase(t *testing.T) {
	s, db := newStore(t)
	proposeOp(t, s, "op_frozen")

	_, err := db.Writer().Exec(
		`UPDATE operations SET params_json = '{"channel":"36"}' WHERE id = 'op_frozen'`)
	if err == nil {
		t.Fatal("expected the immutability trigger to refuse the update")
	}
	if !IsImmutabilityViolation(err) {
		t.Fatalf("expected an immutability violation, got %v", err)
	}
}

func TestAuditTrail_IsAppendOnlyAndChained(t *testing.T) {
	s, db := newStore(t)
	proposeOp(t, s, "op_audit")
	if _, err := approve(t, s, "op_audit", "user:bob"); err != nil {
		t.Fatal(err)
	}

	for _, q := range []string{
		`UPDATE audit_events SET actor = 'mallory' WHERE seq = 1`,
		`DELETE FROM audit_events WHERE seq = 1`,
	} {
		if _, err := db.Writer().Exec(q); err == nil {
			t.Fatalf("expected %q to be refused", q)
		} else if !IsImmutabilityViolation(err) {
			t.Fatalf("expected append-only violation for %q, got %v", q, err)
		}
	}

	// The chain must link: each entry's prev_hash is its predecessor's hash,
	// and the first entry seeds from the genesis constant.
	rows, err := db.Reader().Query(`SELECT seq, prev_hash, entry_hash FROM audit_events ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	prev := genesisHash
	n := 0
	for rows.Next() {
		var seq int64
		var gotPrev, entry string
		if err := rows.Scan(&seq, &gotPrev, &entry); err != nil {
			t.Fatal(err)
		}
		if gotPrev != prev {
			t.Fatalf("chain broken at seq %d: prev_hash=%s, expected %s", seq, gotPrev, prev)
		}
		prev = entry
		n++
	}
	if n != 2 {
		t.Fatalf("audit entries = %d, want 2", n)
	}
}

func TestIdempotency_ReplayReturnsOriginalAndConflictIsRefused(t *testing.T) {
	s, _ := newStore(t)
	first := proposeOp(t, s, "op_idem")

	// Same key, same payload: returns the original rather than creating a
	// duplicate operation.
	replay := *first
	replay.ID = "op_idem_replay"
	got, err := s.Propose(context.Background(), storage.ProposeRequest{
		Operation:   &replay,
		RequestHash: first.PayloadHash,
		Audit:       storage.AuditEntry{EventID: "a-r", Kind: "x", Actor: "u", CorrelationID: "c"},
		Event:       storage.Event{ID: "e-r", Subject: "s", Payload: json.RawMessage(`{}`)},
	})
	if err != nil {
		t.Fatalf("replay should succeed: %v", err)
	}
	if got.ID != first.ID {
		t.Fatalf("replay returned %s, want the original %s", got.ID, first.ID)
	}

	// Same key, different payload: refused. Returning the first operation
	// would execute something the caller did not ask for.
	conflict := *first
	conflict.ID = "op_idem_conflict"
	_, err = s.Propose(context.Background(), storage.ProposeRequest{
		Operation:   &conflict,
		RequestHash: "a-different-request-hash",
		Audit:       storage.AuditEntry{EventID: "a-c", Kind: "x", Actor: "u", CorrelationID: "c"},
		Event:       storage.Event{ID: "e-c", Subject: "s", Payload: json.RawMessage(`{}`)},
	})
	if !errors.Is(err, storage.ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
}

func TestSettle_IndeterminateIsNotTerminal(t *testing.T) {
	s, _ := newStore(t)
	op := proposeOp(t, s, "op_indet")
	if _, err := approve(t, s, "op_indet", "user:bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(context.Background(), storage.ClaimRequest{
		OperationID: "op_indet", ExpectedHash: op.PayloadHash,
		InstanceID: "mcpd-01", LeaseExpiresAt: testClock.Add(time.Minute),
		AttemptID: "att-1",
		Audit:     storage.AuditEntry{EventID: "a-cl", Kind: "x", Actor: "sys", CorrelationID: "c"},
		Event:     storage.Event{ID: "e-cl", Subject: "s", Payload: json.RawMessage(`{}`)},
	}); err != nil {
		t.Fatal(err)
	}

	settled, err := s.Settle(context.Background(), storage.SettleRequest{
		OperationID: "op_indet", AttemptID: "att-1",
		To: operations.StateIndeterminate, Actor: "system:executor",
		ErrorCode: operations.CodeIndeterminate,
		Audit:     storage.AuditEntry{EventID: "a-st", Kind: "x", Actor: "sys", CorrelationID: "c"},
		Event:     storage.Event{ID: "e-st", Subject: "s", Payload: json.RawMessage(`{}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if settled.State != operations.StateIndeterminate {
		t.Fatalf("state = %s, want indeterminate", settled.State)
	}
	if settled.TerminalAt != nil {
		t.Fatal("indeterminate must not stamp terminal_at; it awaits reconciliation")
	}
	if settled.LeaseOwner != "" {
		t.Fatal("settling must release the lease")
	}
}

func TestOutbox_DrainAndAck(t *testing.T) {
	s, db := newStore(t)
	out := NewOutboxStore(db, func() time.Time { return testClock })
	proposeOp(t, s, "op_out")

	pending, err := out.Pending(context.Background(), testClock, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending = %d, want 1", len(pending))
	}
	if pending[0].Subject != "mcp.operation.proposed" {
		t.Fatalf("subject = %s", pending[0].Subject)
	}

	if err := out.MarkPublished(context.Background(), pending[0].EventID, testClock); err != nil {
		t.Fatal(err)
	}
	n, err := out.PendingCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("pending after ack = %d, want 0", n)
	}

	// A repeated ack is harmless — the publisher is restart-safe and may
	// re-ack an event it already acked.
	if err := out.MarkPublished(context.Background(), pending[0].EventID, testClock); err != nil {
		t.Fatalf("re-ack should be a no-op, got %v", err)
	}
}

func TestMigrate_IsIdempotent(t *testing.T) {
	db := newTestDB(t)
	applied, err := Migrate(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if applied != 0 {
		t.Fatalf("second migrate applied %d migrations, want 0", applied)
	}
	v, err := SchemaVersion(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if v < 1 {
		t.Fatalf("schema version = %d, want at least 1", v)
	}
}

func TestOpen_RefusesInMemoryDatabase(t *testing.T) {
	if _, err := Open(context.Background(), Options{Path: ":memory:"}); err == nil {
		t.Fatal("in-memory databases cannot provide durable approvals and must be refused")
	}
}
