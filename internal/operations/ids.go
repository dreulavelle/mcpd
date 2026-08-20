package operations

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/binary"
	"strings"
	"sync"
	"time"
)

// ULIDGenerator produces sortable, unique identifiers.
//
// Sortability matters more than it might seem: operation IDs are a primary key
// in SQLite, and monotonically increasing keys keep B-tree inserts appending
// rather than splitting pages across the index.
type ULIDGenerator struct {
	now func() time.Time

	mu       sync.Mutex
	lastMS   int64
	lastRand [10]byte
}

// NewULIDGenerator returns a generator.
func NewULIDGenerator(now func() time.Time) *ULIDGenerator {
	if now == nil {
		now = time.Now
	}
	return &ULIDGenerator{now: now}
}

var crockford = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

// OperationID returns an operation identifier.
func (g *ULIDGenerator) OperationID() string { return "op_" + g.ulid() }

// EventID returns an event identifier. It becomes the deduplication key on the
// bus, so it must be unique per logical event.
func (g *ULIDGenerator) EventID() string { return "evt_" + g.ulid() }

// AttemptID returns an execution attempt identifier.
func (g *ULIDGenerator) AttemptID() string { return "att_" + g.ulid() }

// ulid builds a 26-character Crockford-base32 identifier: 48 bits of
// millisecond timestamp followed by 80 bits of randomness.
func (g *ULIDGenerator) ulid() string {
	ms := g.now().UnixMilli()

	g.mu.Lock()
	if ms == g.lastMS {
		// Within the same millisecond, increment the random component rather
		// than redrawing it. That preserves ordering between IDs generated in
		// the same tick, which redrawing would not.
		incrementBytes(&g.lastRand)
	} else {
		g.lastMS = ms
		if _, err := rand.Read(g.lastRand[:]); err != nil {
			// Falling back to a timestamp-derived value keeps IDs unique
			// within a process even if system entropy is momentarily
			// unavailable. These identifiers are not secrets.
			binary.BigEndian.PutUint64(g.lastRand[:8], uint64(ms))
		}
	}
	entropy := g.lastRand
	g.mu.Unlock()

	var buf [16]byte
	buf[0] = byte(ms >> 40)
	buf[1] = byte(ms >> 32)
	buf[2] = byte(ms >> 24)
	buf[3] = byte(ms >> 16)
	buf[4] = byte(ms >> 8)
	buf[5] = byte(ms)
	copy(buf[6:], entropy[:])

	return strings.ToLower(crockford.EncodeToString(buf[:]))
}

// incrementBytes adds one to a big-endian byte counter.
func incrementBytes(b *[10]byte) {
	for i := len(b) - 1; i >= 0; i-- {
		b[i]++
		if b[i] != 0 {
			return
		}
	}
}
