package plugins

import "encoding/json"

// Bounding what one tool call returns.
//
// A tool result is charged twice, and neither charge is obvious from the code
// that builds it.
//
// **It may go over the wire twice.** The MCP specification says a tool
// returning structured content SHOULD also return the serialized JSON in a
// text block, so pre-SEP-2106 clients can recover the payload; the Go SDK does
// exactly that whenever a handler leaves Content nil.
//
// This host no longer leaves it nil for a client that negotiated 2025-06-18 or
// later -- see sendOnce in registry.go -- so a modern connector is charged for
// the answer once. An older one is still charged twice, and the budget below is
// therefore still computed against the worse case: it is the ceiling that has
// to hold for every caller, not the one that happens to be connected.
//
// The saving is deliberately banked rather than spent. Doubling the budget
// would hand the freed context straight back to larger answers; leaving it
// where it is means a modern client pays half of what it used to for the same
// result, which is the win that is actually felt.
//
// **The client has its own ceiling.** Claude Code caps a tool response at
// 25,000 tokens by default, and other clients have their own. A response past
// the cap is cut by the client, mid-JSON, with no note saying what went
// missing -- which is the worst way for a result to be shortened. A model is
// then reasoning about a truncated object it does not know is truncated.
//
// So the arithmetic, once, here:
//
//	25,000 tokens          the client's ceiling
//	  ~3.5 chars per token dense JSON, conservatively
//	= ~87,500 characters   what the whole response may be
//	÷ 2                    because it is sent twice
//	= ~43,750 characters   what a plugin may build
//	→  40,000              rounded down, leaving room for the envelope
//
// A plugin that bounds itself here is one whose results are cut *by it*, in a
// place it chooses, with a note saying so and what to narrow. That is the
// whole point: truncation is going to happen on a large estate either way, and
// the only question is whether the thing doing it can explain itself.
//
// This is not a setting. An operator who wants smaller answers has each
// plugin's own item limit, which is the knob that means what they mean; this
// is a property of the protocol and the clients, and an operator raising it
// would only move where the cutting happens.
const MaxResultBytes = 40_000

// ResultBudget returns the byte ceiling for one tool result.
//
// A function rather than the bare constant so that a tool returning several
// collections can divide it honestly. A composite result -- storage, memory
// and processors in one answer -- is one tool result, and three collections
// each bounded at the whole budget is a result three times past it.
//
// collections is the number of independent collections the result carries; a
// tool returning one passes 1 and gets the whole budget.
func ResultBudget(collections int) int {
	if collections < 1 {
		collections = 1
	}
	return MaxResultBytes / collections
}

// Truncation says whether a listing was shortened, and why.
//
// The reason is a sentence for whoever reads the result, not a code: a model
// deciding whether to narrow a query and an operator reading the same answer
// both need to know that what they are looking at is not all of it.
type Truncation struct {
	Truncated bool   `json:"truncated,omitempty"`
	Reason    string `json:"truncation_reason,omitempty"`
}

// BoundBytes cuts rows to the result budget and says what happened.
//
// cut is what the caller already decided about the *count* -- there were more
// than were returned -- and is carried through untouched when everything fits.
// The two reasons are the caller's own words, because "the account holds more"
// and "the phone system holds more" are the same event in different rooms.
//
// A row that will not encode stops the listing there rather than failing the
// whole call: the tool's own result would fail to encode later with a worse
// message, and what was collected is worth more than nothing.
func BoundBytes[T any](rows []T, cut Truncation, sizeReason, encodingReason string) ([]T, Truncation) {
	total := 0
	for i, row := range rows {
		encoded, err := json.Marshal(row)
		if err != nil {
			return rows[:i], Truncation{Truncated: true, Reason: encodingReason}
		}
		total += len(encoded)
		if total > ResultBudget(1) {
			return rows[:i], Truncation{Truncated: true, Reason: sizeReason}
		}
	}
	return rows, cut
}
