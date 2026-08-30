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

// newAccountID returns a random identifier for a ChatGPT account.
//
// Generated rather than derived from the name: an account can be renamed, and
// a tunnel assignment pointing at it must survive that. A name-derived id
// would break every assignment the moment somebody fixed a typo.
func newAccountID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("sqlite: system entropy unavailable: " + err.Error())
	}
	return "acct_" + hex.EncodeToString(b[:])
}

// newBypassID returns a random identifier for an approval bypass.
//
// Prefixed like the others, and short: it appears in the audit trail as the
// authority for every change the window let through, so it is read far more
// often than it is typed.
func newBypassID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("sqlite: system entropy unavailable: " + err.Error())
	}
	return "byp_" + hex.EncodeToString(b[:])
}
