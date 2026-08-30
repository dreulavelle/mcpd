package sqlite

import (
	"context"
	"testing"
	"time"
)

// us is a pointer to a measured duration, for the tests that care.
func us(v int64) *int64 { return &v }

func newCallStore(t *testing.T) *ToolCallStore {
	t.Helper()
	return NewToolCallStore(newTestDB(t), time.Now)
}

var base = time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

func record(t *testing.T, s *ToolCallStore, c ToolCall) {
	t.Helper()
	if err := s.Record(context.Background(), c); err != nil {
		t.Fatalf("record: %v", err)
	}
}

// The question the counters cannot answer.
func TestCalls_RecordsWhoCalled(t *testing.T) {
	ctx := context.Background()
	s := newCallStore(t)

	record(t, s, ToolCall{
		At: base, Principal: "key:abc", Role: "user", Plugin: "graylog",
		Tool: "search_messages", Outcome: "ok", DurationUS: us(42_000),
		CorrelationID: "corr-1",
	})

	calls, err := s.Calls(ctx, ToolCallFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(calls))
	}
	c := calls[0]
	if c.Principal != "key:abc" || c.Plugin != "graylog" || c.Tool != "search_messages" {
		t.Errorf("recorded %+v", c)
	}
	if c.CorrelationID != "corr-1" {
		t.Errorf("correlation id = %q; it is the only thing a caller can quote back", c.CorrelationID)
	}
	if !c.At.Equal(base) {
		t.Errorf("at = %v, want %v", c.At, base)
	}
}

// A refusal is as much a fact about who reached for what as a success is.
// Dropping them would hide exactly the calls worth reading about.
func TestCalls_KeepsRefusals(t *testing.T) {
	ctx := context.Background()
	s := newCallStore(t)

	for _, outcome := range []string{"ok", "error", "denied", "rate_limited"} {
		record(t, s, ToolCall{
			At: base, Principal: "key:abc", Plugin: "graylog", Tool: "search_messages",
			Outcome: outcome,
		})
	}

	for _, outcome := range []string{"denied", "rate_limited"} {
		calls, err := s.Calls(ctx, ToolCallFilter{Outcome: outcome})
		if err != nil {
			t.Fatal(err)
		}
		if len(calls) != 1 {
			t.Errorf("%s: got %d, want 1", outcome, len(calls))
		}
	}
}

func TestCalls_Filters(t *testing.T) {
	ctx := context.Background()
	s := newCallStore(t)

	record(t, s, ToolCall{At: base, Principal: "key:a", Plugin: "graylog", Tool: "search_messages", Outcome: "ok"})
	record(t, s, ToolCall{At: base, Principal: "key:b", Plugin: "graylog", Tool: "search_messages", Outcome: "error"})
	record(t, s, ToolCall{At: base, Principal: "key:a", Plugin: "observium", Tool: "list_devices", Outcome: "ok"})

	for _, tc := range []struct {
		name   string
		filter ToolCallFilter
		want   int
	}{
		{"by principal", ToolCallFilter{Principal: "key:a"}, 2},
		{"by plugin", ToolCallFilter{Plugin: "graylog"}, 2},
		{"by tool", ToolCallFilter{Tool: "list_devices"}, 1},
		{"by outcome", ToolCallFilter{Outcome: "error"}, 1},
		{"combined", ToolCallFilter{Principal: "key:a", Plugin: "graylog"}, 1},
		{"nothing matches", ToolCallFilter{Principal: "key:nobody"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, err := s.Calls(ctx, tc.filter)
			if err != nil {
				t.Fatal(err)
			}
			if len(calls) != tc.want {
				t.Errorf("got %d, want %d", len(calls), tc.want)
			}
		})
	}
}

// The bug paging by id exists for. Bursts of calls land inside one
// millisecond, and a timestamp cursor would leave the database free to order
// them differently between pages -- so a reader would see a row twice, or miss
// one entirely.
func TestCalls_PagesWithoutRepeatingWithinAMillisecond(t *testing.T) {
	ctx := context.Background()
	s := newCallStore(t)

	const total = 30
	for i := 0; i < total; i++ {
		// Every call in the same millisecond, deliberately.
		record(t, s, ToolCall{
			At: base, Principal: "key:a", Plugin: "graylog",
			Tool: "search_messages", Outcome: "ok",
		})
	}

	seen := map[int64]bool{}
	var before int64
	for page := 0; page < 10; page++ {
		calls, err := s.Calls(ctx, ToolCallFilter{Limit: 10, Before: before})
		if err != nil {
			t.Fatal(err)
		}
		if len(calls) == 0 {
			break
		}
		for _, c := range calls {
			if seen[c.ID] {
				t.Fatalf("call %d came back on a second page", c.ID)
			}
			seen[c.ID] = true
		}
		before = calls[len(calls)-1].ID
	}
	if len(seen) != total {
		t.Errorf("paged through %d calls, want %d", len(seen), total)
	}
}

func TestCalls_NewestFirst(t *testing.T) {
	ctx := context.Background()
	s := newCallStore(t)

	record(t, s, ToolCall{At: base, Principal: "key:a", Plugin: "p", Tool: "old", Outcome: "ok"})
	record(t, s, ToolCall{At: base.Add(time.Hour), Principal: "key:a", Plugin: "p", Tool: "new", Outcome: "ok"})

	calls, err := s.Calls(ctx, ToolCallFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0].Tool != "new" {
		t.Fatalf("got %+v; the newest call must lead", calls)
	}
}

func TestCalls_Since(t *testing.T) {
	ctx := context.Background()
	s := newCallStore(t)

	record(t, s, ToolCall{At: base.Add(-48 * time.Hour), Principal: "key:a", Plugin: "p", Tool: "old", Outcome: "ok"})
	record(t, s, ToolCall{At: base, Principal: "key:a", Plugin: "p", Tool: "recent", Outcome: "ok"})

	calls, err := s.Calls(ctx, ToolCallFilter{Since: base.Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Tool != "recent" {
		t.Fatalf("got %+v, want only the recent call", calls)
	}
}

// What a caller reached is not what it is permitted to reach, and the summary
// reports the first. The gap between the two is the interesting part of a
// grant review.
func TestCallers_SummarisesWhatWasActuallyReached(t *testing.T) {
	ctx := context.Background()
	s := newCallStore(t)

	record(t, s, ToolCall{At: base, Principal: "key:a", Role: "user", Plugin: "graylog", Tool: "search_messages", Outcome: "ok"})
	record(t, s, ToolCall{At: base.Add(time.Minute), Principal: "key:a", Role: "user", Plugin: "graylog", Tool: "search_messages", Outcome: "error"})
	record(t, s, ToolCall{At: base.Add(2 * time.Minute), Principal: "key:a", Role: "user", Plugin: "observium", Tool: "list_devices", Outcome: "denied"})
	record(t, s, ToolCall{At: base, Principal: "svc:chatgpt", Role: "user", Plugin: "echo", Tool: "get_echo", Outcome: "ok"})

	callers, err := s.Callers(ctx, base.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(callers) != 2 {
		t.Fatalf("got %d callers, want 2", len(callers))
	}

	// Busiest first.
	top := callers[0]
	if top.Principal != "key:a" || top.Calls != 3 {
		t.Fatalf("top caller is %+v", top)
	}
	if top.Errors != 1 || top.Denied != 1 {
		t.Errorf("errors=%d denied=%d, want 1 and 1", top.Errors, top.Denied)
	}
	if len(top.Plugins) != 2 {
		t.Errorf("reached %v, want two plugins", top.Plugins)
	}
	if !top.LastSeen.After(top.FirstSeen) {
		t.Errorf("first seen %v is not before last seen %v", top.FirstSeen, top.LastSeen)
	}
}

func TestCallers_IgnoresOlderThanTheWindow(t *testing.T) {
	ctx := context.Background()
	s := newCallStore(t)

	record(t, s, ToolCall{At: base.Add(-72 * time.Hour), Principal: "key:old", Plugin: "p", Tool: "t", Outcome: "ok"})
	record(t, s, ToolCall{At: base, Principal: "key:now", Plugin: "p", Tool: "t", Outcome: "ok"})

	callers, err := s.Callers(ctx, base.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(callers) != 1 || callers[0].Principal != "key:now" {
		t.Fatalf("got %+v, want only the recent caller", callers)
	}
}

// One row per read is the highest-volume thing this host writes, so a data
// directory must not grow without bound because somebody left an agent running.
func TestPruneToolCalls(t *testing.T) {
	ctx := context.Background()
	s := newCallStore(t)

	record(t, s, ToolCall{At: base.Add(-48 * time.Hour), Principal: "key:a", Plugin: "p", Tool: "old", Outcome: "ok"})
	record(t, s, ToolCall{At: base, Principal: "key:a", Plugin: "p", Tool: "recent", Outcome: "ok"})

	removed, err := s.PruneToolCalls(ctx, base.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("removed %d, want 1", removed)
	}

	calls, err := s.Calls(ctx, ToolCallFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Tool != "recent" {
		t.Fatalf("left %+v, want only the recent call", calls)
	}
}

// A limit somebody typed into a URL must not let one request read the whole
// table into memory.
func TestCalls_BoundsTheLimit(t *testing.T) {
	ctx := context.Background()
	s := newCallStore(t)
	record(t, s, ToolCall{At: base, Principal: "key:a", Plugin: "p", Tool: "t", Outcome: "ok"})

	for _, limit := range []int{0, -1, 100000} {
		if _, err := s.Calls(ctx, ToolCallFilter{Limit: limit}); err != nil {
			t.Errorf("limit %d: %v", limit, err)
		}
	}
}
