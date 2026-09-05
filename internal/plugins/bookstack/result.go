package bookstack

import (
	"encoding/json"
	"strings"

	"github.com/spoked/mcpd/internal/plugins"
)

// maxResultBytes bounds the whole of one answer. The number is the host's, not
// this package's: see plugins.MaxResultBytes for the arithmetic.
var maxResultBytes = plugins.ResultBudget(1)

// The reasons a listing stopped short.
const (
	reasonCount    = "the knowledge base holds more than were returned; narrow with a filter or ask for fewer"
	reasonSize     = "the result reached the size one tool call may return; narrow it"
	reasonEncoding = "a record could not be encoded and the listing stops there"
)

// truncation is what every listing carries when it stops short.
//
// A field rather than a log line, because a model shown twenty of three
// hundred pages and not told so will answer as though it saw them all.
type truncation struct {
	Truncated bool   `json:"truncated,omitempty"`
	Reason    string `json:"truncation_reason,omitempty"`
	// Total is how many exist upstream, which turns "twenty" into "twenty of
	// 322" -- the difference between an answer and a misleading one.
	Total int `json:"total,omitempty"`
}

// bound trims rows to the byte ceiling and says so. The count ceiling is
// applied upstream by list, which stops fetching; this is the second ceiling,
// on how much those rows weigh.
func bound[T any](rows []T, p page) ([]T, truncation) {
	cut := truncation{Total: p.total}
	if p.more || (p.total > 0 && p.total > len(rows)) {
		cut.Truncated = true
		cut.Reason = reasonCount
	}
	total := 0
	for i, row := range rows {
		encoded, err := json.Marshal(row)
		if err != nil {
			return rows[:i], truncation{Truncated: true, Reason: reasonEncoding, Total: p.total}
		}
		total += len(encoded)
		if total > maxResultBytes {
			return rows[:i], truncation{Truncated: true, Reason: reasonSize, Total: p.total}
		}
	}
	return rows, cut
}

// maxContentBytes bounds the body of one page returned by get_page.
//
// A quarter of the result budget, so a long runbook comes back with room left
// for the fields around it. Content that hits this is cut at a rune boundary
// and says so, because half a procedure presented as a whole one is worse
// than an answer that admits it stopped.
var maxContentBytes = maxResultBytes / 4

// clip shortens page content to the ceiling and reports whether it did.
func clip(s string) (string, bool) {
	if len(s) <= maxContentBytes {
		return s, false
	}
	cut := s[:maxContentBytes]
	// Back off to the last complete line, so the cut lands somewhere a reader
	// can see rather than mid-sentence.
	if i := strings.LastIndexByte(cut, '\n'); i > maxContentBytes/2 {
		cut = cut[:i]
	}
	return strings.ToValidUTF8(cut, ""), true
}

// userRef is how BookStack names a person on a record it returns in full.
type userRef struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug,omitempty"`
}

// tag is one label on an item. BookStack stores them as name/value pairs, so
// "customer" / "Acme" is one tag rather than two.
type tag struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
	Order int    `json:"order,omitempty"`
}

// narrowing is what a listing says when it stopped short.
//
// The truncation fields already report that it happened; this says what to do
// about it, which is the part a model acts on. Only when it happened, so a
// complete answer carries no advice it does not need.
func narrowing(cut truncation) []string {
	if !cut.Truncated {
		return nil
	}
	return []string{
		"This is not all of them — narrow with a filter, raise limit, or use " +
			"search_content to go straight to what you are looking for.",
	}
}
