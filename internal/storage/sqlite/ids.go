package sqlite

import (
	"crypto/rand"
	"encoding/hex"
)

// newEventID returns a random identifier for an outbox event. It becomes the
// deduplication key on the bus, so it must be unique per logical event and
// stable across republication attempts.
func newEventID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("sqlite: system entropy unavailable: " + err.Error())
	}
	return "evt_" + hex.EncodeToString(b[:])
}
