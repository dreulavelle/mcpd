package users

import (
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/auth"
)

/*
Two clocks, and the shorter one wins.

Before there was an idle clock a session expired only by the absolute one, so
somebody working steadily was signed out mid sentence at the ceiling and a
browser left open on a signed-in console stayed signed in until that same
moment whether anybody was there or not.
*/
func TestSession_IdleWindowEndsASessionBeforeItsCeiling(t *testing.T) {
	s, setClock := newStore(t)
	ctx := t.Context()
	u := mustCreate(t, s, "someone@example.com", auth.RoleAdministrator)
	at := time.Now()
	setClock(at)
	advance := func(d time.Duration) { at = at.Add(d); setClock(at) }

	token, sess, err := s.NewSession(ctx, u.ID, 168*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if sess.LastSeenAt.IsZero() {
		t.Fatal("a new session was never seen")
	}

	// Still inside the idle window: resolves, and reports the nearer deadline.
	got, resolved, err := s.ResolveSession(ctx, token, 8*time.Hour)
	if err != nil {
		t.Fatalf("resolve inside the window: %v", err)
	}
	if got.ID != u.ID {
		t.Fatalf("resolved to %s", got.ID)
	}
	if !resolved.EndsAt().Equal(resolved.IdleDeadline) {
		t.Errorf("EndsAt = %v, want the idle deadline %v, which is nearer than the ceiling %v",
			resolved.EndsAt(), resolved.IdleDeadline, resolved.ExpiresAt)
	}

	// Nine hours of nobody doing anything ends it, even though the ceiling is
	// still six days away.
	advance(9 * time.Hour)
	if _, _, err := s.ResolveSession(ctx, token, 8*time.Hour); err == nil {
		t.Error("a session idle past its window still resolved")
	}
}

// Zero is the behaviour from before there were two clocks: the ceiling alone.
func TestSession_ZeroIdleLeavesOnlyTheCeiling(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	u := mustCreate(t, s, "someone@example.com", auth.RoleAdministrator)

	token, _, err := s.NewSession(ctx, u.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, resolved, err := s.ResolveSession(ctx, token, 0); err != nil {
		t.Fatalf("resolve: %v", err)
	} else if !resolved.IdleDeadline.IsZero() {
		t.Errorf("idle deadline = %v, want none when the window is off", resolved.IdleDeadline)
	}
}

/*
The write is throttled.

A person clicking around generates far more requests than a clock needs, and
the console polls several endpoints on top of that. Without the throttle every
request is a write to the session row.
*/
func TestSession_TouchIsThrottled(t *testing.T) {
	s, setClock := newStore(t)
	ctx := t.Context()
	u := mustCreate(t, s, "someone@example.com", auth.RoleAdministrator)
	at := time.Now()
	setClock(at)
	advance := func(d time.Duration) { at = at.Add(d); setClock(at) }

	token, _, err := s.NewSession(ctx, u.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	// Immediately after signing in, the row is already fresh.
	moved, err := s.TouchSession(ctx, token, time.Minute, 8*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if moved {
		t.Error("a session seen a moment ago was written again")
	}

	advance(2 * time.Minute)
	moved, err = s.TouchSession(ctx, token, time.Minute, 8*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !moved {
		t.Error("a session not seen for two minutes was not written")
	}
}

/*
Touching keeps a session alive across a span longer than the idle window, which
is the whole point: somebody working through the afternoon is not idle.
*/
func TestSession_ActivityKeepsASessionAlive(t *testing.T) {
	s, setClock := newStore(t)
	ctx := t.Context()
	u := mustCreate(t, s, "someone@example.com", auth.RoleAdministrator)
	at := time.Now()
	setClock(at)
	advance := func(d time.Duration) { at = at.Add(d); setClock(at) }

	token, _, err := s.NewSession(ctx, u.ID, 168*time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	const idle = time.Hour
	// Six hours of steady work, in steps shorter than the window.
	for range 12 {
		advance(30 * time.Minute)
		if _, err := s.TouchSession(ctx, token, time.Minute, 8*time.Hour); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.ResolveSession(ctx, token, idle); err != nil {
			t.Fatalf("signed out while still working: %v", err)
		}
	}

	// Then they walk away for longer than the window.
	advance(idle + time.Minute)
	if _, _, err := s.ResolveSession(ctx, token, idle); err == nil {
		t.Error("a session idle past its window still resolved")
	}
}

// A session past its ceiling cannot be revived by activity: the guard is in
// the statement, so a touch arriving after expiry matches nothing.
func TestSession_TouchCannotReviveAnExpiredSession(t *testing.T) {
	s, setClock := newStore(t)
	ctx := t.Context()
	u := mustCreate(t, s, "someone@example.com", auth.RoleAdministrator)
	at := time.Now()
	setClock(at)
	advance := func(d time.Duration) { at = at.Add(d); setClock(at) }

	token, _, err := s.NewSession(ctx, u.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	advance(2 * time.Hour)
	moved, err := s.TouchSession(ctx, token, time.Minute, 8*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if moved {
		t.Error("an expired session was moved by a touch")
	}
	if _, _, err := s.ResolveSession(ctx, token, 8*time.Hour); err == nil {
		t.Error("an expired session resolved after being touched")
	}
}

/*
The guard is in the statement, not in the caller.

What stops a touch reviving a session that has already gone idle used to be
that its only caller resolved first and skipped the touch on failure. A second
caller, or a reordering, would have silently resurrected sessions the idle
window had ended.
*/
func TestSession_TouchCannotReviveAnIdleSession(t *testing.T) {
	s, setClock := newStore(t)
	ctx := t.Context()
	u := mustCreate(t, s, "someone@example.com", auth.RoleAdministrator)
	at := time.Now()
	setClock(at)

	token, _, err := s.NewSession(ctx, u.ID, 168*time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	// Nine hours of nothing, with the ceiling still six days away.
	at = at.Add(9 * time.Hour)
	setClock(at)

	moved, err := s.TouchSession(ctx, token, time.Minute, 8*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if moved {
		t.Error("a session already past its idle window was moved by a touch")
	}
	if _, _, err := s.ResolveSession(ctx, token, 8*time.Hour); err == nil {
		t.Error("an idle session resolved after being touched")
	}
}

// Housekeeping takes out rows that can never be used again, rather than
// leaving a user id and a CSRF token sitting there until the ceiling.
func TestSession_PurgeRemovesIdleSessionsToo(t *testing.T) {
	s, setClock := newStore(t)
	ctx := t.Context()
	u := mustCreate(t, s, "someone@example.com", auth.RoleAdministrator)
	at := time.Now()
	setClock(at)

	token, _, err := s.NewSession(ctx, u.ID, 168*time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	at = at.Add(9 * time.Hour)
	setClock(at)
	if err := s.PurgeExpiredSessions(ctx, 8*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ResolveSession(ctx, token, 0); err == nil {
		t.Error("an idle session survived the purge even with the idle check off")
	}
}
