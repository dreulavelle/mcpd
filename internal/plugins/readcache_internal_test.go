package plugins

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// flightObserver counts the events the cache reports, by name.
type flightObserver struct {
	mu sync.Mutex
	n  map[string]int
}

func (o *flightObserver) CacheEvent(_, _, event string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.n == nil {
		o.n = map[string]int{}
	}
	o.n[event]++
}
func (o *flightObserver) count(event string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.n[event]
}

// Callers asking the same question at the same time share one fetch, so a
// dashboard opening six panels is one request upstream rather than six.
//
// An internal test because it waits on the group's own in-flight count rather
// than on a sleep. cachestore.Group covers the collapsing itself; what is this
// package's own is reporting the waiters as "shared" and the winner as "miss",
// which is what an operator reads off the cache metric.
func TestReadCache_ConcurrentCallersShareOneFetchAndAreReportedShared(t *testing.T) {
	obs := &flightObserver{}
	c := NewReadCache("acme", 16, 1<<20, time.Now, obs)

	release := make(chan struct{})
	var calls atomic.Int64
	var wg sync.WaitGroup
	const callers = 8

	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.Do(context.Background(), "config", "k", time.Minute,
				func(context.Context) (any, error) {
					calls.Add(1)
					<-release // hold the flight open so the others pile up behind it
					return "v", nil
				})
		}()
	}

	// Wait for the fetch to be registered, then let every other caller arrive
	// before it finishes. The same shape as TestGroup_CollapsesConcurrentFetches
	// in cachestore: without the second wait a straggler can reach Do after the
	// answer is already stored and be an ordinary hit instead of a waiter.
	for c.group.InFlight() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Errorf("upstream was reached %d times for %d concurrent callers, want 1", got, callers)
	}
	// Exactly one caller did the fetching and is reported as the miss.
	if miss := obs.count(CacheMiss); miss != 1 {
		t.Errorf("miss = %d, want exactly one caller to have done the fetching", miss)
	}
	// The rest waited on it and are reported as sharing, which is the label this
	// package is responsible for.
	if shared := obs.count(CacheShared); shared != callers-1 {
		t.Errorf("shared = %d, want %d -- the waiters were not reported as sharing",
			shared, callers-1)
	}
	// And every caller is accounted for exactly once.
	if total := obs.count(CacheMiss) + obs.count(CacheShared) + obs.count(CacheHit); total != callers {
		t.Errorf("miss+shared+hit = %d, want %d -- a caller went unreported", total, callers)
	}
}
