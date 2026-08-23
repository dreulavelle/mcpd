package mcpremote

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestDrop_OnlyForgetsTheSessionTheCallerUsed is the ABA.
//
// Two callers share a session. The first fails and drops it; a third caller
// dials a replacement; the second caller -- still holding the original handle,
// and still failing on it -- must not take the replacement down with it. Under
// sustained concurrent failure the old behaviour destroyed healthy sessions as
// fast as they were made, and the symptom was a server that reconnected
// endlessly and never settled.
func TestDrop_OnlyForgetsTheSessionTheCallerUsed(t *testing.T) {
	fs := newFixtureServer(t)
	c, err := newConn(fs.URL, nil, testImpl())
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	t.Cleanup(func() { _ = c.close() })

	ctx := context.Background()

	first, err := c.session(ctx)
	if err != nil {
		t.Fatalf("first dial: %v", err)
	}

	// The first caller fails. Its session goes.
	c.drop(first)

	replacement, err := c.session(ctx)
	if err != nil {
		t.Fatalf("second dial: %v", err)
	}
	if replacement == first {
		t.Fatal("the fixture did not produce a distinct replacement session")
	}

	// Now the straggler: a second caller that was using the original session
	// and is only now discovering that it broke.
	c.drop(first)

	c.mu.Lock()
	current := c.current
	c.mu.Unlock()
	if current != replacement {
		t.Fatal("a stale failure discarded the replacement session")
	}

	// And it is not merely remembered -- it still works.
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := replacement.Ping(pingCtx, nil); err != nil {
		t.Errorf("a stale failure closed the replacement session: %v", err)
	}
}

// TestDrop_ClosesTheFailingSessionEvenWhenItIsNotCurrent: the handle the
// caller had is broken either way, so it is always closed. Only whether
// c.current is cleared depends on identity.
func TestDrop_ClosesTheFailingSessionEvenWhenItIsNotCurrent(t *testing.T) {
	fs := newFixtureServer(t)
	c, err := newConn(fs.URL, nil, testImpl())
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	t.Cleanup(func() { _ = c.close() })

	ctx := context.Background()
	stale, err := c.session(ctx)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c.drop(stale)
	if _, err := c.session(ctx); err != nil {
		t.Fatalf("redial: %v", err)
	}

	// Dropping the stale handle a second time must not panic or block, and
	// the session must be closed rather than left open.
	c.drop(stale)

	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := stale.Ping(pingCtx, nil); err == nil {
		t.Error("the failing session should have been closed")
	}
}

func TestDrop_IgnoresANilSession(t *testing.T) {
	fs := newFixtureServer(t)
	c, err := newConn(fs.URL, nil, testImpl())
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	t.Cleanup(func() { _ = c.close() })

	if _, err := c.session(context.Background()); err != nil {
		t.Fatalf("dial: %v", err)
	}
	c.drop(nil)

	c.mu.Lock()
	current := c.current
	c.mu.Unlock()
	if current == nil {
		t.Error("dropping nothing must not discard the live session")
	}
}

// TestConn_ConcurrentDialDropClose exercises the interleavings the reasoning
// above depends on, under the race detector.
//
// Not a proof of any one ordering -- it cannot schedule the goroutines -- but
// it is what would surface an unsynchronised access, a double close, or a
// panic on a nil session, none of which a single-threaded test would reach.
func TestConn_ConcurrentDialDropClose(t *testing.T) {
	fs := newFixtureServer(t)

	for range 50 {
		c, err := newConn(fs.URL, nil, testImpl())
		if err != nil {
			t.Fatalf("conn: %v", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var wg sync.WaitGroup

		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				s, err := c.session(ctx)
				if err != nil {
					// A refusal is fine -- shutdown is racing this on purpose.
					// A session with no error is not, if it is nil.
					return
				}
				if s == nil {
					t.Error("session() returned no session and no error")
					return
				}
				c.drop(s)
			}()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.close()
		}()

		wg.Wait()
		cancel()
		_ = c.close()
	}
}
