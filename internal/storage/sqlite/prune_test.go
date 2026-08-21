package sqlite

import (
	"context"
	"testing"
	"time"
)

// Retention and tamper-evidence pull against each other, and these are the
// properties that keep the resolution honest.

func TestPrune_RemovesOldEntriesAndRecordsThat(t *testing.T) {
	s, db := newStore(t)
	audit := NewAuditStore(db)
	ctx := context.Background()

	proposeOp(t, s, "op_old")
	if _, err := approve(t, s, "op_old", "user:bob"); err != nil {
		t.Fatal(err)
	}

	now := testClock.Add(time.Minute)
	removed, err := audit.Prune(ctx, "user:alice", now.Add(time.Hour), now)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if removed != 2 {
		t.Fatalf("removed %d entries, want 2", removed)
	}

	// The trail must say what happened to the part of itself that is gone.
	records, err := audit.Recent(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d entries after pruning, want the record of the prune", len(records))
	}
	if records[0].Entry.Kind != "audit.pruned" {
		t.Fatalf("kind = %q, want the prune to be recorded", records[0].Entry.Kind)
	}
	if records[0].Entry.Actor != "user:alice" {
		t.Fatalf("actor = %q, want whoever asked", records[0].Entry.Actor)
	}
}

// A pruned trail must not read as a tampered one, or the check is useless
// exactly where it matters.
func TestPrune_LeavesAVerifiableChain(t *testing.T) {
	s, db := newStore(t)
	audit := NewAuditStore(db)
	ctx := context.Background()

	proposeOp(t, s, "op_a")
	if _, err := approve(t, s, "op_a", "user:bob"); err != nil {
		t.Fatal(err)
	}
	now := testClock.Add(time.Minute)
	if _, err := audit.Prune(ctx, "user:alice", now.Add(time.Hour), now); err != nil {
		t.Fatal(err)
	}
	proposeOp(t, s, "op_b")

	broken, err := audit.VerifyChain(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if broken != 0 {
		t.Fatalf("chain reported broken at %d after a legitimate prune", broken)
	}
}

// Tampering after a prune must still be caught: re-anchoring is not the same
// as no longer checking.
func TestPrune_StillDetectsTampering(t *testing.T) {
	s, db := newStore(t)
	audit := NewAuditStore(db)
	ctx := context.Background()

	proposeOp(t, s, "op_a")
	now := testClock.Add(time.Minute)
	if _, err := audit.Prune(ctx, "user:alice", now.Add(time.Hour), now); err != nil {
		t.Fatal(err)
	}
	proposeOp(t, s, "op_b")
	proposeOp(t, s, "op_c")

	// Reach past the trigger the way someone with the database file would.
	if _, err := db.Writer().Exec(`INSERT INTO audit_prune_gate (id, opened_at) VALUES (1, 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().Exec(
		`DELETE FROM audit_events WHERE seq = (SELECT MAX(seq) - 1 FROM audit_events)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().Exec(`DELETE FROM audit_prune_gate`); err != nil {
		t.Fatal(err)
	}

	broken, err := audit.VerifyChain(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if broken == 0 {
		t.Fatal("removing an entry from the middle must break the chain")
	}
}

// The gate is the whole reason deletion is still safe. Without one open, the
// table has to refuse -- a bug, a migration, or a sqlite3 prompt included.
func TestAuditRemainsAppendOnlyWithoutTheGate(t *testing.T) {
	s, db := newStore(t)
	proposeOp(t, s, "op_a")

	if _, err := db.Writer().Exec(`DELETE FROM audit_events`); err == nil {
		t.Fatal("deleting without opening the gate must be refused")
	} else if !IsImmutabilityViolation(err) {
		t.Fatalf("expected an append-only violation, got %v", err)
	}
}

// Nothing older than the cutoff means nothing happens -- including no entry
// announcing that nothing happened, which would grow the trail it prunes.
func TestPrune_DoesNothingWhenNothingIsOldEnough(t *testing.T) {
	s, db := newStore(t)
	audit := NewAuditStore(db)
	ctx := context.Background()

	proposeOp(t, s, "op_a")
	before, err := audit.Recent(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}

	// Anchored to the store's clock, not the wall's. These entries are written
	// at testClock, so a cutoff derived from time.Now() moves further from
	// them every day the suite is not run -- and this test, whose cutoff is in
	// the past, began failing on its own the day real time passed
	// testClock + 24h.
	now := testClock.Add(time.Minute)
	removed, err := audit.Prune(ctx, "user:alice", now.Add(-24*time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Fatalf("removed %d, want 0", removed)
	}
	after, err := audit.Recent(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("history changed from %d to %d entries", len(before), len(after))
	}
}
