package plugins_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/plugins"
)

// clock is a hand-wound clock, so a TTL can expire without the test sleeping.
type clock struct {
	mu sync.Mutex
	at time.Time
}

func newClock() *clock {
	return &clock{at: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}
func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

// observer records the metric events the cache reports.
type observer struct {
	mu     sync.Mutex
	events []string
}

func (o *observer) CacheEvent(plugin, kind, event string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, fmt.Sprintf("%s/%s/%s", plugin, kind, event))
}
func (o *observer) all() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.events...)
}

func counted(n *atomic.Int64, v any) func(context.Context) (any, error) {
	return func(context.Context) (any, error) {
		n.Add(1)
		return v, nil
	}
}

// The whole point: a second identical read inside the TTL costs no request.
func TestReadCache_ASecondIdenticalReadDoesNotReachUpstream(t *testing.T) {
	clk := newClock()
	obs := &observer{}
	c := plugins.NewReadCache("acme", 16, 1<<20, clk.now, obs)

	var calls atomic.Int64
	for range 3 {
		got, err := c.Do(context.Background(), "config", "k", time.Minute, counted(&calls, "v"))
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		if got != "v" {
			t.Fatalf("got %v, want v", got)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("upstream was reached %d times, want 1", calls.Load())
	}
	want := []string{"acme/config/miss", "acme/config/hit", "acme/config/hit"}
	if got := obs.all(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("events = %v, want %v", got, want)
	}
}

// Nothing stale is ever served: past the TTL the answer is fetched again.
func TestReadCache_DoesNotServeAnExpiredAnswer(t *testing.T) {
	clk := newClock()
	c := plugins.NewReadCache("acme", 16, 1<<20, clk.now, nil)

	var calls atomic.Int64
	ctx := context.Background()
	if _, err := c.Do(ctx, "config", "k", time.Minute, counted(&calls, "v")); err != nil {
		t.Fatalf("Do: %v", err)
	}
	clk.advance(2 * time.Minute)
	if _, err := c.Do(ctx, "config", "k", time.Minute, counted(&calls, "v")); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("upstream was reached %d times, want 2 -- a stale answer was served", calls.Load())
	}
}

// A ttl of zero means this endpoint is not cacheable at all. It must not be
// held, and it must not be served from a previous call either.
func TestReadCache_ATTLOfZeroIsNeverCached(t *testing.T) {
	c := plugins.NewReadCache("acme", 16, 1<<20, newClock().now, nil)

	var calls atomic.Int64
	ctx := context.Background()
	for range 3 {
		if _, err := c.Do(ctx, "live", "k", 0, counted(&calls, "v")); err != nil {
			t.Fatalf("Do: %v", err)
		}
	}
	if calls.Load() != 3 {
		t.Errorf("upstream was reached %d times, want 3", calls.Load())
	}
}

// Different keys are different questions.
func TestReadCache_DifferentKeysDoNotShareAnAnswer(t *testing.T) {
	c := plugins.NewReadCache("acme", 16, 1<<20, newClock().now, nil)
	ctx := context.Background()

	a, err := c.Do(ctx, "config", "one", time.Minute, func(context.Context) (any, error) { return "A", nil })
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	b, err := c.Do(ctx, "config", "two", time.Minute, func(context.Context) (any, error) { return "B", nil })
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if a != "A" || b != "B" {
		t.Errorf("got %v and %v, want A and B", a, b)
	}
}

// A failure is not an answer and is not held. An upstream that is down must be
// reported as down on every call, not remembered as an empty installation.
func TestReadCache_DoesNotHoldAFailure(t *testing.T) {
	c := plugins.NewReadCache("acme", 16, 1<<20, newClock().now, nil)
	ctx := context.Background()
	boom := errors.New("upstream is down")

	var calls atomic.Int64
	for range 2 {
		_, err := c.Do(ctx, "config", "k", time.Minute, func(context.Context) (any, error) {
			calls.Add(1)
			return nil, boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want the upstream's error", err)
		}
	}
	if calls.Load() != 2 {
		t.Errorf("upstream was reached %d times, want 2 -- a failure was held", calls.Load())
	}
	// And a later success is still cacheable.
	var ok atomic.Int64
	if _, err := c.Do(ctx, "config", "k", time.Minute, counted(&ok, "v")); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if _, err := c.Do(ctx, "config", "k", time.Minute, counted(&ok, "v")); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if ok.Load() != 1 {
		t.Errorf("the recovered answer was fetched %d times, want 1", ok.Load())
	}
}
