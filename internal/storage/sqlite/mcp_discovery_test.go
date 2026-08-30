package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// A success moves both timestamps and clears the error.
func TestRecordDiscovery_Success(t *testing.T) {
	ctx := context.Background()
	s, _ := newMCPStore(t)
	importFixture(t, s)

	at := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	if err := s.RecordDiscovery(ctx, "weather", at, nil); err != nil {
		t.Fatal(err)
	}

	srv, ok, err := s.Get(ctx, "weather")
	if err != nil || !ok {
		t.Fatalf("get: %v %v", ok, err)
	}
	if !srv.Discovery.LastAttempted.Equal(at) {
		t.Errorf("attempted = %v, want %v", srv.Discovery.LastAttempted, at)
	}
	if !srv.Discovery.LastSucceeded.Equal(at) {
		t.Errorf("succeeded = %v, want %v", srv.Discovery.LastSucceeded, at)
	}
	if srv.Discovery.Error != "" {
		t.Errorf("error = %q, want empty", srv.Discovery.Error)
	}
}

// The bug this exists for. A failure must move the attempt and leave the
// success alone: the tools on screen were confirmed when they were confirmed,
// and advancing that timestamp on a failed check would present stale data as
// freshly verified.
func TestRecordDiscovery_FailureDoesNotAgeTheData(t *testing.T) {
	ctx := context.Background()
	s, _ := newMCPStore(t)
	importFixture(t, s)

	good := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	if err := s.RecordDiscovery(ctx, "weather", good, nil); err != nil {
		t.Fatal(err)
	}

	bad := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	if err := s.RecordDiscovery(ctx, "weather", bad, errors.New("connection refused")); err != nil {
		t.Fatal(err)
	}

	srv, _, err := s.Get(ctx, "weather")
	if err != nil {
		t.Fatal(err)
	}
	if !srv.Discovery.LastAttempted.Equal(bad) {
		t.Errorf("attempted = %v, want the failed attempt at %v", srv.Discovery.LastAttempted, bad)
	}
	if !srv.Discovery.LastSucceeded.Equal(good) {
		t.Errorf("succeeded = %v, want it to stay at %v", srv.Discovery.LastSucceeded, good)
	}
	if srv.Discovery.Error != "connection refused" {
		t.Errorf("error = %q", srv.Discovery.Error)
	}
}

// A server that recovers stops reporting an error, or the page would keep
// showing a failure that has been fixed.
func TestRecordDiscovery_SuccessClearsAnEarlierFailure(t *testing.T) {
	ctx := context.Background()
	s, _ := newMCPStore(t)
	importFixture(t, s)

	at := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	if err := s.RecordDiscovery(ctx, "weather", at, errors.New("connection refused")); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordDiscovery(ctx, "weather", at.Add(time.Hour), nil); err != nil {
		t.Fatal(err)
	}

	srv, _, err := s.Get(ctx, "weather")
	if err != nil {
		t.Fatal(err)
	}
	if srv.Discovery.Error != "" {
		t.Errorf("error = %q after a successful check", srv.Discovery.Error)
	}
}

// Written from whatever the far end said. An upstream answering with a page of
// HTML must not put a page of HTML in the database.
func TestRecordDiscovery_BoundsTheMessage(t *testing.T) {
	ctx := context.Background()
	s, _ := newMCPStore(t)
	importFixture(t, s)

	long := errors.New(strings.Repeat("x", 4000))
	at := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	if err := s.RecordDiscovery(ctx, "weather", at, long); err != nil {
		t.Fatal(err)
	}

	srv, _, err := s.Get(ctx, "weather")
	if err != nil {
		t.Fatal(err)
	}
	if len(srv.Discovery.Error) > 600 {
		t.Errorf("stored %d bytes of an upstream's error message", len(srv.Discovery.Error))
	}
	if srv.Discovery.Error == "" {
		t.Error("the message was dropped entirely rather than truncated")
	}
}

// A server removed while its discovery was in flight is an ordinary race, and
// the removal is the outcome that should win. Recording must not resurrect it
// or fail the caller.
func TestRecordDiscovery_ForAServerThatIsGone(t *testing.T) {
	ctx := context.Background()
	s, _ := newMCPStore(t)

	at := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	if err := s.RecordDiscovery(ctx, "never-existed", at, nil); err != nil {
		t.Fatalf("recording against a removed server failed: %v", err)
	}
	if _, ok, _ := s.Get(ctx, "never-existed"); ok {
		t.Error("recording a discovery created a server row")
	}
}

// Every row predates the schedule, and backfilling would be inventing an
// attempt that never happened. "Not checked" has to be representable.
func TestImportedServerReadsAsNeverChecked(t *testing.T) {
	ctx := context.Background()
	s, _ := newMCPStore(t)
	importFixture(t, s)

	srv, _, err := s.Get(ctx, "weather")
	if err != nil {
		t.Fatal(err)
	}
	if !srv.Discovery.LastAttempted.IsZero() || !srv.Discovery.LastSucceeded.IsZero() {
		t.Errorf("a freshly imported server claims to have been checked: %+v", srv.Discovery)
	}
}
