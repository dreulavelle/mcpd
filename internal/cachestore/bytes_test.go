package cachestore

import (
	"fmt"
	"testing"
	"time"
)

func entry(at time.Time, bytes int) *Entry {
	return &Entry{Value: "v", FetchedAt: at, TTL: time.Hour, Bytes: bytes}
}

// TestASizeBoundEvictsTheOldest is the bound a count could not give.
//
// One held answer is a whole paginated walk, so a fixed number of entries is a
// few megabytes on a small estate and a hundred on a large one. Bounding the
// count alone left the memory a host actually uses decided by how big somebody
// else's network is.
func TestASizeBoundEvictsTheOldest(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	s := NewBounded(100, 1000)

	for i := range 5 {
		s.Put(fmt.Sprintf("k%d", i), entry(base.Add(time.Duration(i)*time.Minute), 300))
	}

	if got := s.Bytes(); got > 1000 {
		t.Fatalf("holding %d bytes, over the 1000 bound", got)
	}
	// Three of 300 fit; the oldest two went.
	if s.Len() != 3 {
		t.Fatalf("held %d entries, want 3", s.Len())
	}
	if s.Get("k0") != nil || s.Get("k1") != nil {
		t.Error("the oldest entries survived while newer ones were dropped")
	}
	if s.Get("k4") == nil {
		t.Error("the entry just stored was evicted")
	}
}

// TestAnOversizedEntryIsStillHeld prefers one large answer to none.
//
// Evicting the entry Put has just stored would mean a Put that stored nothing,
// and the caller would find its own answer missing the instant it asked for it
// back -- which reads as a cache that does not work rather than one that is
// full.
func TestAnOversizedEntryIsStillHeld(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	s := NewBounded(100, 1000)
	s.Put("small", entry(base, 100))
	s.Put("huge", entry(base.Add(time.Minute), 50_000))

	if s.Get("huge") == nil {
		t.Fatal("an entry larger than the bound was refused")
	}
	// Everything else went to try to make room, which is right.
	if s.Get("small") != nil {
		t.Error("room was not made for it")
	}
	if s.Len() != 1 {
		t.Fatalf("held %d entries, want just the large one", s.Len())
	}
}

// TestReplacingAnEntryDoesNotLeakItsBytes is the accounting bug this would
// otherwise have.
//
// A running total that only ever adds drifts upwards until the store believes
// it is full and evicts everything, on a workload that re-reads the same key --
// which is the workload a cache exists for.
func TestReplacingAnEntryDoesNotLeakItsBytes(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	s := NewBounded(100, 10_000)

	for i := range 20 {
		s.Put("same", entry(base.Add(time.Duration(i)*time.Minute), 400))
	}
	if got := s.Bytes(); got != 400 {
		t.Fatalf("one entry of 400 bytes is accounted as %d", got)
	}
	if s.Len() != 1 {
		t.Fatalf("held %d entries after 20 writes to one key", s.Len())
	}
}

// TestAnEntryThatDoesNotSayIsNotCounted keeps the old callers working.
//
// A store cannot weigh an `any` without encoding it, so an entry with no size
// is held and not counted rather than guessed at.
func TestAnEntryThatDoesNotSayIsNotCounted(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	s := NewBounded(100, 1000)
	for i := range 10 {
		s.Put(fmt.Sprintf("k%d", i), entry(base.Add(time.Duration(i)*time.Minute), 0))
	}
	if s.Bytes() != 0 {
		t.Errorf("unsized entries were counted as %d bytes", s.Bytes())
	}
	if s.Len() != 10 {
		t.Errorf("held %d unsized entries, want all 10 -- the count bound is 100", s.Len())
	}
}

// TestNewHasNoSizeBound covers every existing caller.
func TestNewHasNoSizeBound(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	s := New(4)
	for i := range 4 {
		s.Put(fmt.Sprintf("k%d", i), entry(base.Add(time.Duration(i)*time.Minute), 1<<20))
	}
	if s.Len() != 4 {
		t.Fatalf("held %d of 4 entries; a size bound was applied where none was asked for", s.Len())
	}
}
