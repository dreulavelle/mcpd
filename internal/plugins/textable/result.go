package textable

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/plugins"
)

// maxResultBytes bounds the whole of one answer.
//
// The number is the host's, not this package's: a result goes over the wire
// twice, once as structured content and once serialized into text, against a
// client that stops reading at 25,000 tokens. See plugins.MaxResultBytes for
// the arithmetic.
//
// It matters more here than in most integrations because of one endpoint.
// /api/contacts takes no page or limit parameter, so the ceiling on a contact
// listing is entirely on this side: a tenant with forty thousand contacts sends
// forty thousand, and without this the client would cut the result mid-JSON
// with nothing saying what went missing.
var maxResultBytes = plugins.ResultBudget(1)

// maxFieldChars bounds one value. A contact's note or a drip campaign's message
// body can be long on its own, and one of those should not fill a conversation.
// Cut rather than dropped: the first part is the part somebody wants, and the
// result says a value was shortened.
const maxFieldChars = 1000

// bound trims rows to a count and a byte ceiling, and says which one bit.
//
// Generic over the row type because every listing here needs the same two
// ceilings and none of them needs anything else. The encoded size is measured
// row by row rather than by encoding the whole slice repeatedly: a listing that
// fits costs one marshal per row and one comparison, which is nothing beside the
// round trip that produced it.
func bound[T any](rows []T, limit int) (kept []T, cut truncation) {
	if limit > 0 && len(rows) > limit {
		// Recorded before the byte walk so that a listing stopped by both
		// ceilings reports the count first, which is the one a caller can do
		// something about.
		cut = truncation{Truncated: true, Reason: reasonCount}
		rows = rows[:limit]
	}

	total := 0
	for i, row := range rows {
		encoded, err := json.Marshal(row)
		if err != nil {
			// A row that will not encode cannot be returned, and the tool's own
			// result would fail to encode later with a worse message. Stopping
			// here reports what was collected rather than nothing.
			return rows[:i], truncation{Truncated: true, Reason: reasonEncoding}
		}
		total += len(encoded)
		if total > maxResultBytes {
			return rows[:i], truncation{Truncated: true, Reason: reasonSize}
		}
	}
	return rows, cut
}

// The reasons a listing stopped short. Separate strings rather than a boolean
// because they call for different things from the caller: a count ceiling is
// raised or paged past, and a size ceiling means the rows themselves are large
// and narrowing is the only option.
const (
	reasonCount    = "the requested limit was reached; ask for more with a higher limit"
	reasonSize     = "the result reached the size one tool call may return; narrow it with a query"
	reasonEncoding = "a record could not be encoded and the listing stops there"
)

// truncation is what every listing carries when it stops short.
//
// A field rather than a log line, because a model shown twenty of two thousand
// records and not told so will answer as though it saw them all.
type truncation struct {
	Truncated bool   `json:"truncated,omitempty"`
	Reason    string `json:"truncation_reason,omitempty"`
}

// shorten cuts one value to maxFieldChars, reporting whether it did.
func shorten(s string) (string, bool) {
	if len(s) <= maxFieldChars {
		return s, false
	}
	// Cut on a rune boundary: a value sliced mid-sequence is invalid UTF-8 and
	// fails to encode, which turns a long note into a failed tool call.
	cut := s[:maxFieldChars]
	for len(cut) > 0 && !json.Valid([]byte(`"`+strings.ToValidUTF8(cut, "")+`"`)) {
		cut = cut[:len(cut)-1]
	}
	return strings.ToValidUTF8(cut, "") + "…", true
}

// matches reports whether any of fields contains the caller's query, folding
// case.
//
// Filtering here rather than upstream because none of these endpoints takes a
// query parameter -- /api/users, /api/organizations and /api/contacts all
// return everything the key can see and nothing else. So the filter narrows
// what is *returned* rather than what is fetched: it does not make the call
// cheaper, and it does make the answer readable, which is the thing that was
// actually in the way.
func matches(query string, fields ...string) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return true
	}
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), q) {
			return true
		}
	}
	return false
}

// countShorten shortens a value and adds to a running count of how many were
// shortened, so a result can report the total in one field rather than marking
// each value.
func countShorten(s string, count int) (string, int) {
	out, cut := shorten(s)
	if cut {
		count++
	}
	return out, count
}

// orElse returns the first value if it is set, otherwise the fallback.
//
// It exists for the id on a detail read: Textable sometimes omits the id from a
// document fetched *by* that id, and a result whose id field is empty is one a
// model cannot use for the next call.
func orElse(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

// millisToRFC3339 renders a millisecond epoch as a timestamp, and an absent one
// as nothing at all.
//
// Textable reports times as milliseconds since the epoch, and zero means the
// event never happened rather than 1970. Both halves matter: a model handed
// 1670372913091 will not read it, and one handed "1970-01-01T00:00:00Z" for a
// contact nobody has messaged will read it and be wrong.
func millisToRFC3339(ms float64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(int64(ms)).UTC().Format(time.RFC3339)
}
