// Package cachestore is the memory a cache is built on: a bounded set of timed
// entries, and a way to make one fetch serve every caller waiting on it.
//
// Deliberately not a cache. It holds no policy about when to serve something
// stale, whether a validator is worth sending, or what a missing answer means
// -- those differ between the things that use it, and a store that tried to
// decide them would be one of them wearing a general name. What is shared is
// the boring half: a map with a bound on it, an entry that knows how old it
// is, and the rule that six callers asking the same question at the same
// moment should cost one answer.
//
// It exists because there were about to be two copies of that half. The
// catalogue cache had one; a plugin's read cache needed the same thing with a
// different policy on top.
package cachestore

import (
	"context"
	"sync"
	"time"
)

// DefaultLimit is how many entries a store holds when it is given no bound.
const DefaultLimit = 256

// State is how usable a held entry is.
type State int

const (
	// Fresh is inside its TTL.
	Fresh State = iota
	// Stale is past its TTL but inside the window it was granted afterwards.
	// Whether that window means anything is the caller's policy, not this
	// package's: for a catalogue listing it means "serve this and refresh
	// behind it", and for a device's state it means nothing at all.
	Stale
	// Expired is past both.
	Expired
)

// Entry is one held answer.
//
// Immutable once stored. A renewal replaces it rather than editing it, so a
// reader holding a pointer can never see half an update -- which is what makes
// Get safe to call without holding the store's lock afterwards.
type Entry struct {
	// Value is whatever was cached. The store does not look at it.
	Value any
	// Err is a remembered refusal, for a cache that holds one. What may be
	// remembered, and for how long, is the caller's decision.
	Err error
	// Meta carries whatever the caller needs alongside the value -- HTTP
	// validators, most likely. Also never looked at.
	Meta any

	FetchedAt  time.Time
	TTL        time.Duration
	StaleWhile time.Duration
}

// State reports how usable this entry is at now.
func (e *Entry) State(now time.Time) State {
	age := now.Sub(e.FetchedAt)
	switch {
	case age < e.TTL:
		return Fresh
	case age < e.TTL+e.StaleWhile:
		return Stale
	default:
		return Expired
	}
}

// Store is a bounded set of entries.
//
// One store can sit behind several caches, which is the point: the thing being
// bounded is a process's memory, and it does not care which of them filled it.
// A cap that each cache gets its own copy of is a cap the next cache silently
// doubles.
type Store struct {
	limit int

	mu      sync.Mutex
	entries map[string]*Entry
}

// New builds a store. A limit of zero takes DefaultLimit.
func New(limit int) *Store {
	if limit <= 0 {
		limit = DefaultLimit
	}
	return &Store{limit: limit, entries: make(map[string]*Entry)}
}

// Get returns the held entry, or nil.
func (s *Store) Get(key string) *Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.entries[key]
}

// Put stores an entry, evicting the least recently fetched one if the store is
// full and this is a new key.
func (s *Store) Put(key string, entry *Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, replacing := s.entries[key]; !replacing && len(s.entries) >= s.limit {
		s.evictOldestLocked()
	}
	s.entries[key] = entry
}

// Len reports how many entries are held.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// Keys returns the held keys, for a test with something to say about how they
// are built. Order is undefined.
func (s *Store) Keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.entries))
	for k := range s.entries {
		out = append(out, k)
	}
	return out
}

// evictOldestLocked drops the least recently fetched entry. A linear scan over
// a few hundred entries, on the miss path only, is cheaper than maintaining an
// order that nothing else needs.
func (s *Store) evictOldestLocked() {
	var oldestKey string
	var oldest time.Time
	for k, v := range s.entries {
		if oldestKey == "" || v.FetchedAt.Before(oldest) {
			oldestKey, oldest = k, v.FetchedAt
		}
	}
	if oldestKey != "" {
		delete(s.entries, oldestKey)
	}
}

// Group makes one in-flight fetch serve every caller waiting on the same key.
//
// The case it exists for is a model fanning out: six tool calls that all need
// the device list arrive together, all miss the cache, and without this all six
// walk the upstream's pagination. Five of those walks produce an answer
// identical to the first and cost the upstream five times the quota.
type Group struct {
	mu    sync.Mutex
	calls map[string]*call
}

type call struct {
	done  chan struct{}
	value any
	err   error
}

// Do runs fn for key, or waits for the call already running it.
//
// shared reports that this caller joined an existing fetch rather than starting
// one, which is worth counting separately: it is the half of a cache's value
// that a hit rate does not show.
//
// fn is given a context detached from any one caller's, because it is not any
// one caller's work: the first caller giving up must not cancel the fetch the
// other five are waiting on. It is bounded by timeout instead, and every waiter
// still gives up on its own context.
func (g *Group) Do(ctx context.Context, key string, timeout time.Duration, fn func(context.Context) (any, error)) (value any, shared bool, err error) {
	g.mu.Lock()
	if g.calls == nil {
		g.calls = make(map[string]*call)
	}
	if existing, running := g.calls[key]; running {
		g.mu.Unlock()
		select {
		case <-existing.done:
			return existing.value, true, existing.err
		case <-ctx.Done():
			return nil, true, ctx.Err()
		}
	}
	c := &call{done: make(chan struct{})}
	g.calls[key] = c
	g.mu.Unlock()

	go func() {
		defer func() {
			g.mu.Lock()
			delete(g.calls, key)
			g.mu.Unlock()
			close(c.done)
		}()
		// Detached, then bounded. Detached because the fetch belongs to
		// whoever is still waiting rather than to whoever started it; bounded
		// because a detached fetch with no deadline is a goroutine that
		// outlives its reason.
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
		defer cancel()
		c.value, c.err = fn(fetchCtx)
	}()

	select {
	case <-c.done:
		return c.value, false, c.err
	case <-ctx.Done():
		// This caller gave up. The fetch runs on for whoever else is waiting,
		// and the entry it writes is there for whoever asks next.
		return nil, false, ctx.Err()
	}
}

// InFlight reports how many fetches are running, for a test with something to
// say about sharing.
func (g *Group) InFlight() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.calls)
}
