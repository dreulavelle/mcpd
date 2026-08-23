package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/operations"
)

// The metrics endpoint reads these when a scrape arrives rather than keeping a
// tally in Go, because SQLite is the authority: a counter in this process would
// disagree with it after every restart and every prune, and would never mention
// the row that has been sitting in one state since Tuesday.
func TestStateCounts_GroupsByPluginActionAndState(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()

	proposeOp(t, s, "op_pending_1")
	proposeOp(t, s, "op_pending_2")
	proposeOp(t, s, "op_approved")
	if _, err := approve(t, s, "op_approved", "user:bob"); err != nil {
		t.Fatalf("approve: %v", err)
	}

	counts, err := s.StateCounts(ctx)
	if err != nil {
		t.Fatalf("state counts: %v", err)
	}

	got := map[string]int64{}
	rules := map[string]int64{}
	for _, c := range counts {
		key := c.Plugin + "/" + c.Action + "/" + c.State
		got[key] = c.Count
		rules[key] = c.AuthorizedByRule
	}

	const (
		pending  = "cnmaestro/device.set_radio_channel/pending_approval"
		approved = "cnmaestro/device.set_radio_channel/approved"
	)
	if got[pending] != 2 {
		t.Errorf("pending = %d, want 2 (%v)", got[pending], got)
	}
	if got[approved] != 1 {
		t.Errorf("approved = %d, want 1 (%v)", got[approved], got)
	}

	// Nobody was approved by a rule here, and a null column must read as zero
	// rather than as a comparison that quietly evaluated to null.
	for key, n := range rules {
		if n != 0 {
			t.Errorf("%s reports %d authorised by rule, want 0", key, n)
		}
	}
}

// An empty database reports nothing rather than failing, which is what a scrape
// of a host nobody has used yet has to do.
func TestStateCounts_EmptyDatabase(t *testing.T) {
	s, _ := newStore(t)
	counts, err := s.StateCounts(context.Background())
	if err != nil {
		t.Fatalf("state counts: %v", err)
	}
	if len(counts) != 0 {
		t.Errorf("got %d rows from an empty table", len(counts))
	}
}

// A rule-authorised approval is counted separately, because "how many changes
// happened with nobody being asked" is the question a standing rule creates and
// it is asked on its own.
func TestStateCounts_CountsWhatARuleAuthorised(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()

	proposeOp(t, s, "op_by_rule")
	if _, err := s.Transition(ctx, operations.TransitionRequest{
		OperationID: "op_by_rule",
		From:        operations.StatePendingApproval,
		To:          operations.StateApproved,
		Actor:       operations.PolicyActor,
		Approval: &operations.ApprovalFields{
			ApprovedBy:        operations.PolicyActor,
			ApprovedAt:        testClock,
			ApprovalExpiresAt: testClock.Add(time.Minute),
			AuthorizedByRule:  "routine-radio",
		},
		Audit: operations.AuditEntry{
			EventID: "aud-rule", Kind: "operation.approved", OperationID: "op_by_rule",
			Plugin: "cnmaestro", Action: "device.set_radio_channel",
			Actor: operations.PolicyActor, ToState: operations.StateApproved,
		},
		Event: operations.OutboxEvent{
			ID: "evt-rule", Subject: "mcp.operation.approved", OperationID: "op_by_rule",
		},
	}); err != nil {
		t.Fatalf("approve by rule: %v", err)
	}

	counts, err := s.StateCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var total, byRule int64
	for _, c := range counts {
		if c.State == string(operations.StateApproved) {
			total += c.Count
			byRule += c.AuthorizedByRule
		}
	}
	if total != 1 || byRule != 1 {
		t.Errorf("approved = %d of which %d by rule, want 1 and 1", total, byRule)
	}
}
