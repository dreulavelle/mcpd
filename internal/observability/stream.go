package observability

import (
	"sync"
)

// StreamCapacity is how many recent lines are kept for somebody who has just
// opened the page.
//
// Enough to show what happened during the last start-up, which is what an
// operator opening this at all is usually looking for, and small enough that
// the cost of holding it is not worth thinking about.
const StreamCapacity = 500

// subscriberBuffer is how far behind a watcher may fall before lines are
// dropped for them.
//
// Dropped, never waited for. A browser on a slow connection must not be able
// to stall the process that is logging: the alternative to dropping is a
// goroutine blocked inside Handle, holding the writer's lock, with every other
// goroutine that wants to log queued behind it.
const subscriberBuffer = 256

// LogStream is the recent log, and a fan-out to whoever is watching it.
//
// It holds lines already rendered as JSON, by the same handler and the same
// redaction the file gets, so a value withheld from one is withheld from the
// other. Rendering here rather than passing records on means the format the
// dashboard reads does not change when an operator switches the log format on
// the Settings page -- the two are different audiences and only one of them
// can be asked to cope.
type LogStream struct {
	mu   sync.Mutex
	ring [][]byte
	// next is where the following line goes, modulo capacity.
	next int
	// full records that the ring has wrapped, so the backlog is read in the
	// right order rather than starting from a hole.
	full bool

	subs   map[int]*subscriber
	nextID int
}

type subscriber struct {
	lines chan []byte
	// dropped counts lines this watcher never saw. Reported to them rather
	// than swallowed: a gap in a log with nothing marking it is worse than no
	// log at all, because it reads as "nothing happened".
	dropped int
}

// NewLogStream returns an empty stream.
func NewLogStream() *LogStream {
	return &LogStream{
		ring: make([][]byte, StreamCapacity),
		subs: map[int]*subscriber{},
	}
}

// Write takes one rendered line. It satisfies io.Writer so it can be handed to
// a slog handler, which is what puts redaction and level filtering in front of
// it for free.
func (s *LogStream) Write(p []byte) (int, error) {
	// Copied because slog reuses its buffer, and what is kept here outlives
	// the call by as long as the ring does.
	line := make([]byte, len(p))
	copy(line, p)

	s.mu.Lock()
	defer s.mu.Unlock()

	s.ring[s.next] = line
	s.next = (s.next + 1) % len(s.ring)
	if s.next == 0 {
		s.full = true
	}

	for _, sub := range s.subs {
		select {
		case sub.lines <- line:
		default:
			sub.dropped++
		}
	}
	return len(p), nil
}

// Recent returns the lines held, oldest first.
func (s *LogStream) Recent() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot()
}

// snapshot reads the ring in order. The caller holds the lock.
func (s *LogStream) snapshot() [][]byte {
	if !s.full {
		out := make([][]byte, s.next)
		copy(out, s.ring[:s.next])
		return out
	}
	out := make([][]byte, 0, len(s.ring))
	out = append(out, s.ring[s.next:]...)
	out = append(out, s.ring[:s.next]...)
	return out
}

// Watch is one open view of the log.
type Watch struct {
	// Backlog is what was already held when the watch began.
	Backlog [][]byte
	// Lines carries what has been logged since. Closed by Close.
	Lines <-chan []byte

	stream *LogStream
	id     int
	sub    *subscriber
}

// Watch begins one, returning the lines held and a channel of what follows
// with no gap between them.
//
// Both are taken under one lock for that reason. Reading the backlog and then
// subscribing would lose whatever was logged in between, which is exactly the
// moment somebody opening this page during an incident cares about.
func (s *LogStream) Watch() *Watch {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := s.nextID
	s.nextID++
	sub := &subscriber{lines: make(chan []byte, subscriberBuffer)}
	s.subs[id] = sub

	return &Watch{
		Backlog: s.snapshot(),
		Lines:   sub.lines,
		stream:  s,
		id:      id,
		sub:     sub,
	}
}

// Dropped reports how many lines this watch has missed, and forgets them.
//
// Read between sends so a gap can be described where it happened. A gap in a
// log with nothing marking it is worse than no log at all, because it reads as
// "nothing happened".
func (w *Watch) Dropped() int {
	w.stream.mu.Lock()
	defer w.stream.mu.Unlock()
	n := w.sub.dropped
	w.sub.dropped = 0
	return n
}

// Close ends the watch. Safe to call twice.
func (w *Watch) Close() {
	w.stream.mu.Lock()
	defer w.stream.mu.Unlock()
	if held, ok := w.stream.subs[w.id]; ok {
		delete(w.stream.subs, w.id)
		close(held.lines)
	}
}
