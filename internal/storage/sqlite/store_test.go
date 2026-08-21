package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/operations"
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
	got, err := s.Propose(context.Background(), operations.RepoProposeRequest{
		Operation:   op,
		RequestHash: hash,
		Audit: operations.AuditEntry{
			EventID: "aud-" + id, Kind: "operation.proposed", OperationID: id,
			Plugin: op.Plugin, Action: op.Action, Actor: op.RequestedBy,
			ToState: op.State, Risk: op.Risk, CorrelationID: op.CorrelationID,
		},
		Event: operations.OutboxEvent{
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
	return s.Transition(context.Background(), operations.TransitionRequest{
		OperationID: id,
		From:        operations.StatePendingApproval,
		To:          operations.StateApproved,
		Actor:       approver,
		Approval: &operations.ApprovalFields{
			ApprovedBy:        approver,
			ApprovedAt:        testClock,
			ApprovalExpiresAt: testClock.Add(15 * time.Minute),
		},
		Audit: operations.AuditEntry{
			EventID: "aud-approve-" + approver + "-" + id, Kind: "operation.approved",
			OperationID: id, Actor: approver, FromState: operations.StatePendingApproval,
			ToState: operations.StateApproved, CorrelationID: "corr-" + id,
		},
		Event: operations.OutboxEvent{
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
			case errors.Is(err, operations.ErrStateConflict):
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
			_, err := s.Claim(context.Background(), operations.ClaimRequest{
				OperationID:    "op_claim",
				ExpectedHash:   op.PayloadHash,
				InstanceID:     "mcpd-" + id,
				LeaseExpiresAt: testClock.Add(time.Minute),
				AttemptID:      "attempt-" + id,
				Audit: operations.AuditEntry{
					EventID: "aud-claim-" + id, Kind: "operation.executing",
					OperationID: "op_claim", Actor: "mcpd-" + id,
					CorrelationID: "corr-op_claim",
				},
				Event: operations.OutboxEvent{
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

	_, err := s.Claim(context.Background(), operations.ClaimRequest{
		OperationID:    "op_tamper",
		ExpectedHash:   "not-the-stored-hash",
		InstanceID:     "mcpd-01",
		LeaseExpiresAt: testClock.Add(time.Minute),
		AttemptID:      "attempt-1",
		Audit:          operations.AuditEntry{EventID: "a1", Kind: "x", Actor: "sys", CorrelationID: "c"},
		Event:          operations.OutboxEvent{ID: "e1", Subject: "s", Payload: json.RawMessage(`{}`)},
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

	_, err := s.Claim(context.Background(), operations.ClaimRequest{
		OperationID:    "op_stale",
		ExpectedHash:   op.PayloadHash,
		InstanceID:     "mcpd-01",
		LeaseExpiresAt: clock.Add(time.Minute),
		AttemptID:      "attempt-1",
		Audit:          operations.AuditEntry{EventID: "a1", Kind: "x", Actor: "sys", CorrelationID: "c"},
		Event:          operations.OutboxEvent{ID: "e1", Subject: "s", Payload: json.RawMessage(`{}`)},
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
	got, err := s.Propose(context.Background(), operations.RepoProposeRequest{
		Operation:   &replay,
		RequestHash: first.PayloadHash,
		Audit:       operations.AuditEntry{EventID: "a-r", Kind: "x", Actor: "u", CorrelationID: "c"},
		Event:       operations.OutboxEvent{ID: "e-r", Subject: "s", Payload: json.RawMessage(`{}`)},
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
	_, err = s.Propose(context.Background(), operations.RepoProposeRequest{
		Operation:   &conflict,
		RequestHash: "a-different-request-hash",
		Audit:       operations.AuditEntry{EventID: "a-c", Kind: "x", Actor: "u", CorrelationID: "c"},
		Event:       operations.OutboxEvent{ID: "e-c", Subject: "s", Payload: json.RawMessage(`{}`)},
	})
	if !errors.Is(err, operations.ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
}

func TestSettle_IndeterminateIsNotTerminal(t *testing.T) {
	s, _ := newStore(t)
	op := proposeOp(t, s, "op_indet")
	if _, err := approve(t, s, "op_indet", "user:bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(context.Background(), operations.ClaimRequest{
		OperationID: "op_indet", ExpectedHash: op.PayloadHash,
		InstanceID: "mcpd-01", LeaseExpiresAt: testClock.Add(time.Minute),
		AttemptID: "att-1",
		Audit:     operations.AuditEntry{EventID: "a-cl", Kind: "x", Actor: "sys", CorrelationID: "c"},
		Event:     operations.OutboxEvent{ID: "e-cl", Subject: "s", Payload: json.RawMessage(`{}`)},
	}); err != nil {
		t.Fatal(err)
	}

	settled, err := s.Settle(context.Background(), operations.SettleRequest{
		OperationID: "op_indet", AttemptID: "att-1",
		To: operations.StateIndeterminate, Actor: "system:executor",
		ErrorCode: operations.CodeIndeterminate,
		Audit:     operations.AuditEntry{EventID: "a-st", Kind: "x", Actor: "sys", CorrelationID: "c"},
		Event:     operations.OutboxEvent{ID: "e-st", Subject: "s", Payload: json.RawMessage(`{}`)},
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

// A file copy is not a safe backup under WAL: committed state is split
// between the database and the -wal, so a plain copy can capture a torn
// snapshot. VACUUM INTO takes a consistent view without blocking writers.
func TestBackup_ProducesAConsistentRestorableCopy(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()

	for _, id := range []string{"op_a", "op_b", "op_c"} {
		proposeOp(t, s, id)
	}
	if _, err := approve(t, s, "op_b", "user:bob"); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(t.TempDir(), "backup.db")
	if err := db.Backup(ctx, dest); err != nil {
		t.Fatalf("backup: %v", err)
	}

	// The copy must open, pass an integrity check, and hold the same data.
	restored, err := Open(ctx, Options{Path: dest, RelaxedDurability: true})
	if err != nil {
		t.Fatalf("the backup could not be opened: %v", err)
	}
	defer restored.Close()

	if err := restored.Integrity(ctx); err != nil {
		t.Fatalf("the backup failed its integrity check: %v", err)
	}

	rs := NewOperationStore(restored, func() time.Time { return testClock })
	for _, id := range []string{"op_a", "op_b", "op_c"} {
		if _, err := rs.Get(ctx, id); err != nil {
			t.Errorf("%s is missing from the backup: %v", id, err)
		}
	}
	op, err := rs.Get(ctx, "op_b")
	if err != nil {
		t.Fatal(err)
	}
	if op.State != operations.StateApproved || op.ApprovedBy != "user:bob" {
		t.Fatalf("approval state did not survive the backup: %+v", op.State)
	}

	// The audit chain must still verify, which is the real test that the
	// snapshot is coherent rather than merely readable.
	audit := NewAuditStore(restored)
	brokenAt, err := audit.VerifyChain(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if brokenAt != 0 {
		t.Fatalf("the audit chain in the backup breaks at %d", brokenAt)
	}
}

// Overwriting a backup silently would destroy the copy someone is relying on.
func TestBackup_RefusesToOverwrite(t *testing.T) {
	_, db := newStore(t)
	ctx := context.Background()

	dest := filepath.Join(t.TempDir(), "backup.db")
	if err := db.Backup(ctx, dest); err != nil {
		t.Fatal(err)
	}
	if err := db.Backup(ctx, dest); err == nil {
		t.Fatal("a second backup to the same path must fail rather than overwrite")
	}
}

// Backups run while the process is serving, so they must not need exclusive
// access.
func TestBackup_RunsAlongsideWrites(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()
	proposeOp(t, s, "op_live")

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 20 {
			proposeOp(t, s, fmt.Sprintf("op_bg_%d", i))
		}
	}()

	dest := filepath.Join(t.TempDir(), "live.db")
	if err := db.Backup(ctx, dest); err != nil {
		t.Fatalf("backup during concurrent writes: %v", err)
	}
	<-done

	restored, err := Open(ctx, Options{Path: dest, RelaxedDurability: true})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if err := restored.Integrity(ctx); err != nil {
		t.Fatalf("a backup taken during writes failed its integrity check: %v", err)
	}
}

func TestIntegrity_PassesOnAHealthyDatabase(t *testing.T) {
	_, db := newStore(t)
	if err := db.Integrity(context.Background()); err != nil {
		t.Fatalf("a fresh database should pass: %v", err)
	}
}

// A settled intent can be proposed again. This is the case ChatGPT hit: an
// earlier label.set proposal expired unapproved, and every later attempt to
// propose the same change was refused with an idempotency conflict that
// misdescribed itself as a payload mismatch.
//
// The two mechanisms disagreed about time. idempotency_records carries a TTL
// and the pre-check in Propose honours it, so once the record aged out the
// proposal was waved through -- straight into a permanent unique index that
// had no notion of the first operation being long dead.
func TestIdempotency_SettledIntentCanBeProposedAgain(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	clock := testClock
	s := NewOperationStore(db, func() time.Time { return clock })

	first := proposeOp(t, s, "op_settle_first")

	// The proposal expires unapproved, exactly as the reaper would leave it.
	if _, err := s.Transition(ctx, operations.TransitionRequest{
		OperationID: first.ID,
		From:        operations.StatePendingApproval,
		To:          operations.StateExpired,
		Actor:       "system:reaper",
		Reason:      "proposal deadline passed",
		Terminal:    true,
		Audit:       operations.AuditEntry{EventID: "a-x", Kind: "x", Actor: "sys", CorrelationID: "c"},
		Event:       operations.OutboxEvent{ID: "e-x", Subject: "s", Payload: json.RawMessage(`{}`)},
	}); err != nil {
		t.Fatalf("expire the first proposal: %v", err)
	}

	// Past the idempotency record's TTL, so the pre-check no longer collapses
	// the retry onto the original. This is the state the reported bug was
	// found in.
	clock = testClock.Add(25 * time.Hour)

	retry := *first
	retry.ID = "op_settle_retry"
	retry.RequestedAt = clock
	retry.ExpiresAt = clock.Add(time.Hour)
	got, err := s.Propose(ctx, operations.RepoProposeRequest{
		Operation:   &retry,
		RequestHash: first.PayloadHash,
		Audit:       operations.AuditEntry{EventID: "a-2", Kind: "x", Actor: "u", CorrelationID: "c2"},
		Event:       operations.OutboxEvent{ID: "e-2", Subject: "s", Payload: json.RawMessage(`{}`)},
	})
	if err != nil {
		t.Fatalf("re-proposing a settled intent must succeed: %v", err)
	}
	if got.ID != retry.ID {
		t.Fatalf("got operation %s, want the new one %s", got.ID, retry.ID)
	}
	if got.State != operations.StatePendingApproval {
		t.Fatalf("state = %s, want pending_approval", got.State)
	}
}

// A live intent still collapses. The index went from permanent to
// state-scoped, and the half that was load-bearing has to survive that: two
// proposals of the same change, neither settled, must not both be queued.
func TestIdempotency_LiveIntentIsStillRefused(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	clock := testClock
	s := NewOperationStore(db, func() time.Time { return clock })

	first := proposeOp(t, s, "op_live_first")

	// Past the record's TTL, so only the index stands between the two. The
	// first operation is still pending approval.
	clock = testClock.Add(25 * time.Hour)

	second := *first
	second.ID = "op_live_second"
	_, err := s.Propose(ctx, operations.RepoProposeRequest{
		Operation:   &second,
		RequestHash: first.PayloadHash,
		Audit:       operations.AuditEntry{EventID: "a-3", Kind: "x", Actor: "u", CorrelationID: "c3"},
		Event:       operations.OutboxEvent{ID: "e-3", Subject: "s", Payload: json.RawMessage(`{}`)},
	})
	if !errors.Is(err, operations.ErrIdempotencyConflict) {
		t.Fatalf("a second live proposal must be refused, got %v", err)
	}
}

// Indeterminate is not settled. IsTerminal excludes it because it is
// resolvable by observation, and re-proposing an intent that may already have
// taken effect is the one retry that must stay blocked.
func TestIdempotency_IndeterminateBlocksRetry(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	clock := testClock
	s := NewOperationStore(db, func() time.Time { return clock })

	first := proposeOp(t, s, "op_indet_retry")
	if _, err := approve(t, s, first.ID, "user:bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(ctx, operations.ClaimRequest{
		OperationID: first.ID, ExpectedHash: first.PayloadHash,
		InstanceID: "mcpd-01", LeaseExpiresAt: clock.Add(time.Minute),
		AttemptID: "att-i",
		Audit:     operations.AuditEntry{EventID: "a-ci", Kind: "x", Actor: "sys", CorrelationID: "c"},
		Event:     operations.OutboxEvent{ID: "e-ci", Subject: "s", Payload: json.RawMessage(`{}`)},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Settle(ctx, operations.SettleRequest{
		OperationID: first.ID, AttemptID: "att-i",
		To:    operations.StateIndeterminate,
		Audit: operations.AuditEntry{EventID: "a-si", Kind: "x", Actor: "sys", CorrelationID: "c"},
		Event: operations.OutboxEvent{ID: "e-si", Subject: "s", Payload: json.RawMessage(`{}`)},
	}); err != nil {
		t.Fatal(err)
	}

	clock = testClock.Add(25 * time.Hour)

	retry := *first
	retry.ID = "op_indet_retry_2"
	_, err := s.Propose(ctx, operations.RepoProposeRequest{
		Operation:   &retry,
		RequestHash: first.PayloadHash,
		Audit:       operations.AuditEntry{EventID: "a-4", Kind: "x", Actor: "u", CorrelationID: "c4"},
		Event:       operations.OutboxEvent{ID: "e-4", Subject: "s", Payload: json.RawMessage(`{}`)},
	})
	if !errors.Is(err, operations.ErrIdempotencyConflict) {
		t.Fatalf("an indeterminate outcome must block a retry, got %v", err)
	}
}
