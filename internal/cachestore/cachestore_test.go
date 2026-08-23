package cachestore

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestEntry_State(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	e := &Entry{FetchedAt: base, TTL: time.Minute, StaleWhile: 2 * time.Minute}

	tests := []struct {
		at   time.Duration
		want State
	}{
		{0, Fresh},
		{59 * time.Second, Fresh},
		{time.Minute, Stale},
		{2*time.Minute + 59*time.Second, Stale},
		{3 * time.Minute, Expired},
	}
	for _, tc := range tests {
		if got := e.State(base.Add(tc.at)); got != tc.want {
			t.Errorf("state at +%s = %v, want %v", tc.at, got, tc.want)
		}
	}
}

// An entry with no stale window goes straight from fresh to expired, which is
// what a cache that must never serve something out of date relies on.
func TestEntry_NoStaleWindowExpiresImmediately(t *testing.T) {
	base := time.Now()
	e := &Entry{FetchedAt: base, TTL: time.Second}
	if got := e.State(base.Add(time.Second)); got != Expired {
		t.Errorf("state = %v, want expired", got)
	}
}

func TestStore_EvictsTheOldestOnceFull(t *testing.T) {
	s := New(3)
	base := time.Now()
	for i := range 3 {
		s.Put(strconv.Itoa(i), &Entry{FetchedAt: base.Add(time.Duration(i) * time.Second)})
	}
	s.Put("3", &Entry{FetchedAt: base.Add(3 * time.Second)})

	if s.Len() != 3 {
		t.Errorf("holding %d entries, want 3", s.Len())
	}
	if s.Get("0") != nil {
		t.Error("the oldest entry should have been evicted")
	}
	if s.Get("3") == nil {
		t.Error("the new entry should be held")
	}
}

// Replacing a key must not evict, or a cache refreshing one hot entry would
// slowly empty itself.
func TestStore_ReplacingDoesNotEvict(t *testing.T) {
	s := New(2)
	base := time.Now()
	s.Put("a", &Entry{FetchedAt: base})
	s.Put("b", &Entry{FetchedAt: base.Add(time.Second)})
	s.Put("a", &Entry{FetchedAt: base.Add(2 * time.Second)})

	if s.Get("b") == nil {
		t.Error("replacing an existing key evicted another")
	}
	if s.Len() != 2 {
		t.Errorf("holding %d entries, want 2", s.Len())
	}
}

// The case Group exists for: a model fans out, six calls need the same answer,
// and the upstream should see one request.
func TestGroup_CollapsesConcurrentFetches(t *testing.T) {
	var g Group
	var calls atomic.Int32
	release := make(chan struct{})

	const callers = 6
	var wg sync.WaitGroup
	shared := make([]bool, callers)
	values := make([]any, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, s, err := g.Do(context.Background(), "k", time.Minute,
				func(context.Context) (any, error) {
					calls.Add(1)
					<-release
					return "answer", nil
				})
			if err != nil {
				t.Errorf("caller %d: %v", i, err)
			}
			values[i], shared[i] = v, s
		}()
	}

	// Let every caller arrive before the fetch finishes.
	for g.InFlight() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()

	if n := calls.Load(); n != 1 {
		t.Errorf("the upstream saw %d fetches, want 1", n)
	}
	sharedCount := 0
	for i := range callers {
		if values[i] != "answer" {
			t.Errorf("caller %d got %v", i, values[i])
		}
		if shared[i] {
			sharedCount++
		}
	}
	if sharedCount != callers-1 {
		t.Errorf("%d callers reported sharing, want %d", sharedCount, callers-1)
	}
}

// The first caller giving up must not cancel the fetch the others are waiting
// on. That is the whole reason the fetch runs on a detached context.
func TestGroup_TheFirstCallerGivingUpDoesNotCancelTheFetch(t *testing.T) {
	var g Group
	started := make(chan struct{})
	var finished atomic.Bool

	first, cancelFirst := context.WithCancel(context.Background())
	second := make(chan error, 1)

	go func() {
		_, _, err := g.Do(first, "k", time.Minute, func(ctx context.Context) (any, error) {
			close(started)
			time.Sleep(100 * time.Millisecond)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			finished.Store(true)
			return "answer", nil
		})
		_ = err
	}()

	<-started
	// A second caller joins, then the first walks away.
	go func() {
		_, _, err := g.Do(context.Background(), "k", time.Minute,
			func(context.Context) (any, error) { return "should not run", nil })
		second <- err
	}()
	time.Sleep(10 * time.Millisecond)
	cancelFirst()

	if err := <-second; err != nil {
		t.Fatalf("the remaining caller was failed by somebody else giving up: %v", err)
	}
	if !finished.Load() {
		t.Error("the fetch was cancelled when its first caller left")
	}
}

// A caller's own context still bounds its wait, so a slow shared fetch does not
// hold somebody past their deadline.
func TestGroup_AWaiterRespectsItsOwnDeadline(t *testing.T) {
	var g Group
	release := make(chan struct{})
	defer close(release)

	go func() {
		_, _, _ = g.Do(context.Background(), "k", time.Minute,
			func(context.Context) (any, error) {
				<-release
				return "answer", nil
			})
	}()
	for g.InFlight() == 0 {
		time.Sleep(time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := g.Do(ctx, "k", time.Minute, func(context.Context) (any, error) {
		return nil, errors.New("should not run")
	})
	if err == nil {
		t.Fatal("the waiter should have given up on its own deadline")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("the waiter took %s to give up", elapsed)
	}
}

// A failed fetch is that call's failure, not a permanent one: the key is free
// again immediately.
func TestGroup_AFailedFetchDoesNotPoisonTheKey(t *testing.T) {
	var g Group
	boom := errors.New("boom")

	if _, _, err := g.Do(context.Background(), "k", time.Minute,
		func(context.Context) (any, error) { return nil, boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	v, shared, err := g.Do(context.Background(), "k", time.Minute,
		func(context.Context) (any, error) { return "fine", nil })
	if err != nil || v != "fine" {
		t.Fatalf("v = %v, err = %v", v, err)
	}
	if shared {
		t.Error("a call after the failed one should not be sharing anything")
	}
	if g.InFlight() != 0 {
		t.Errorf("%d fetches still registered", g.InFlight())
	}
}
