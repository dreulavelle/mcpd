package mcpservers

import (
	"encoding/json"
	"testing"
	"time"
)

var now = time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

// Due is judged on the attempt, not the success. A server that is refusing
// connections must be retried on the ordinary interval; judging on success
// would make it due on every pass and probe it as fast as the loop comes round.
func TestDueIsJudgedOnTheAttempt(t *testing.T) {
	failing := Discovery{
		LastAttempted: now.Add(-10 * time.Minute),
		LastSucceeded: now.Add(-90 * time.Hour),
		Error:         "connection refused",
	}
	if failing.Due(now, 24*time.Hour) {
		t.Fatal("a server tried ten minutes ago is due again, so a failing server would be hammered")
	}

	failing.LastAttempted = now.Add(-25 * time.Hour)
	if !failing.Due(now, 24*time.Hour) {
		t.Error("a server last tried 25 hours ago is not due on a 24 hour interval")
	}
}

func TestNeverCheckedIsDue(t *testing.T) {
	if !(Discovery{}).Due(now, 24*time.Hour) {
		t.Fatal("a server nothing has ever asked is not due")
	}
}

func TestDueAtExactlyTheInterval(t *testing.T) {
	// A tick lands on the boundary often enough that "> interval" would slip a
	// whole cycle. Due at the interval, not after it.
	d := Discovery{LastAttempted: now.Add(-24 * time.Hour)}
	if !d.Due(now, 24*time.Hour) {
		t.Error("a server last tried exactly one interval ago is not due")
	}
}

// Stale is judged on the success, because it answers a different question:
// how old is what the operator is looking at. A failing check does not make
// the tools on screen any fresher.
func TestStaleIsJudgedOnTheSuccess(t *testing.T) {
	d := Discovery{
		LastAttempted: now,
		LastSucceeded: now.Add(-90 * time.Hour),
		Error:         "connection refused",
	}
	if !d.Stale(now, 24*time.Hour) {
		t.Fatal("a list last confirmed 90 hours ago reads as fresh because something tried just now")
	}

	d.LastSucceeded = now.Add(-time.Hour)
	if d.Stale(now, 24*time.Hour) {
		t.Error("a list confirmed an hour ago reads as stale")
	}
}

func TestNeverConfirmedIsStale(t *testing.T) {
	if !(Discovery{LastAttempted: now}).Stale(now, 24*time.Hour) {
		t.Fatal("a server that has never been successfully checked does not read as stale")
	}
}

// The bug this exists for: `omitempty` does not omit a zero time.Time, so a
// server nothing had asked serialised as the year 1 and the dashboard read a
// present timestamp. "Never checked" has to be absent on the wire.
func TestNeverCheckedSerialisesAsAbsent(t *testing.T) {
	encoded, err := json.Marshal(Discovery{})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(encoded); got != "{}" {
		t.Fatalf("an unchecked server serialises as %s, want {}", got)
	}
}

func TestCheckedSerialisesItsTimestamps(t *testing.T) {
	encoded, err := json.Marshal(Discovery{
		LastAttempted: now,
		LastSucceeded: now.Add(-time.Hour),
		Error:         "connection refused",
	})
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"last_attempted", "last_succeeded", "error"} {
		if _, ok := back[key]; !ok {
			t.Errorf("%s is missing from %s", key, encoded)
		}
	}
}

// A failing server that has never once succeeded: the attempt is on the wire,
// the success is not, and the page must not read the absence as "fresh".
func TestAttemptedButNeverSucceeded(t *testing.T) {
	encoded, err := json.Marshal(Discovery{LastAttempted: now, Error: "no route to host"})
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatal(err)
	}
	if _, ok := back["last_succeeded"]; ok {
		t.Errorf("a server that never succeeded reports a success time: %s", encoded)
	}
	if _, ok := back["last_attempted"]; !ok {
		t.Errorf("the attempt is missing: %s", encoded)
	}
}
