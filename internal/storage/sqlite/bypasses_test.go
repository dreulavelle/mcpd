package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/operations"
)

func newBypassStore(t *testing.T, now time.Time) *BypassStore {
	t.Helper()
	return NewBypassStore(newTestDB(t), func() time.Time { return now })
}

var bypassAt = time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)

func TestBypassOpenAndRead(t *testing.T) {
	ctx := context.Background()
	s := newBypassStore(t, bypassAt)

	opened, err := s.Open(ctx, "user:someone", 60, "", operations.RiskMedium, "migrating switches")
	if err != nil {
		t.Fatal(err)
	}
	if !opened.ExpiresAt.Equal(bypassAt.Add(time.Hour)) {
		t.Errorf("expires at %v, want one hour on", opened.ExpiresAt)
	}

	open, err := s.Active(ctx)
	if err != nil || len(open) != 1 {
		t.Fatalf("active = %v, %v", open, err)
	}
	if open[0].ID != opened.ID || open[0].Reason != "migrating switches" {
		t.Errorf("active = %+v", open[0])
	}
}

// The defining property, enforced in the store as well as at the API: there is
// no way to open one that does not end.
func TestBypassRefusesAWindowPastTheCeiling(t *testing.T) {
	ctx := context.Background()
	s := newBypassStore(t, bypassAt)

	for _, minutes := range []int{0, -1, operations.MaxBypassMinutes + 1, 10000} {
		if _, err := s.Open(ctx, "user:someone", minutes, "", operations.RiskLow, "why"); !errors.Is(err, ErrBypassTooLong) {
			t.Errorf("%d minutes: got %v, want a refusal", minutes, err)
		}
	}
}

// A level an operator can opt out of is not a level. The rule set refuses a
// critical ceiling, and a window is a weaker authority than a rule.
func TestBypassRefusesACriticalCeiling(t *testing.T) {
	ctx := context.Background()
	s := newBypassStore(t, bypassAt)

	for _, ceiling := range []operations.RiskLevel{operations.RiskCritical, "", "nonsense"} {
		if _, err := s.Open(ctx, "user:someone", 60, "", ceiling, "why"); !errors.Is(err, ErrBypassCritical) {
			t.Errorf("ceiling %q: got %v, want a refusal", ceiling, err)
		}
	}
}

// A window that has run out is not active, and nothing has to sweep it.
func TestBypassExpiresWithoutBeingCleanedUp(t *testing.T) {
	ctx := context.Background()
	s := NewBypassStore(newTestDB(t), func() time.Time { return bypassAt })

	if _, err := s.Open(ctx, "user:someone", 15, "", operations.RiskLow, "why"); err != nil {
		t.Fatal(err)
	}

	later := NewBypassStore(s.db, func() time.Time { return bypassAt.Add(16 * time.Minute) })
	open, err := later.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 0 {
		t.Fatalf("an expired window is still active: %+v", open)
	}
}

// Two windows are two people saying "stop asking", and honouring only the
// narrower would make the second request appear to have done nothing.
func TestBypassActiveReturnsEveryWindowBroadestFirst(t *testing.T) {
	ctx := context.Background()
	s := newBypassStore(t, bypassAt)

	if _, err := s.Open(ctx, "user:a", 60, "graylog", operations.RiskHigh, "one plugin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open(ctx, "user:b", 60, "", operations.RiskLow, "everything"); err != nil {
		t.Fatal(err)
	}

	open, err := s.Active(ctx)
	if err != nil || len(open) != 2 {
		t.Fatalf("active = %v, %v; both windows are open", open, err)
	}
	// Both are returned, and the broadest leads so a caller showing one shows
	// the one worth warning about.
	if open[0].Plugin != "" {
		t.Errorf("first is %q; one covering every plugin authorises more", open[0].Plugin)
	}
}

// The question somebody asks in a hurry is "is anything unsupervised right
// now", and the answer they want is "no". Closing them one at a time leaves
// the possibility of missing one.
func TestBypassRevokeAllClosesEverything(t *testing.T) {
	ctx := context.Background()
	s := newBypassStore(t, bypassAt)

	for i := 0; i < 3; i++ {
		if _, err := s.Open(ctx, "user:someone", 60, "", operations.RiskLow, "why"); err != nil {
			t.Fatal(err)
		}
	}

	closed, err := s.RevokeAll(ctx, "user:admin")
	if err != nil {
		t.Fatal(err)
	}
	if closed != 3 {
		t.Errorf("closed %d, want 3", closed)
	}

	open, err := s.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 0 {
		t.Fatalf("a window survived a revoke-all: %+v", open)
	}

	// Again on nothing is not an error, and closes nothing.
	again, err := s.RevokeAll(ctx, "user:admin")
	if err != nil || again != 0 {
		t.Errorf("second revoke = %d, %v", again, err)
	}
}

// Switching the asking off is an administrative act with consequences somebody
// may have to account for later.
func TestBypassIsWrittenToTheTrail(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	s := NewBypassStore(db, func() time.Time { return bypassAt })
	audit := NewAuditStore(db)

	if _, err := s.Open(ctx, "user:someone", 60, "graylog", operations.RiskLow, "migrating"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RevokeAll(ctx, "user:admin"); err != nil {
		t.Fatal(err)
	}

	records, err := audit.Recent(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	var opened, revoked bool
	for _, r := range records {
		switch r.Entry.Kind {
		case "approval.bypass.opened":
			opened = true
		case "approval.bypass.revoked":
			revoked = true
		}
	}
	if !opened || !revoked {
		t.Errorf("trail has opened=%v revoked=%v; both are administrative acts", opened, revoked)
	}
}

// The count comes from the operations that record the window as their
// authority, so it cannot drift from them.
func TestBypassApprovedCountsFromTheOperations(t *testing.T) {
	ctx := context.Background()
	s := newBypassStore(t, bypassAt)

	counts, err := s.Approved(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(counts) != 0 {
		t.Errorf("counts = %v, want none before anything ran", counts)
	}
}
