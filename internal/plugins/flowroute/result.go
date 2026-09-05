package flowroute

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spoked/mcpd/internal/plugins"
)

// maxResultBytes bounds the whole of one answer. The number is the host's, not
// this package's: see plugins.MaxResultBytes for the arithmetic.
var maxResultBytes = plugins.ResultBudget(1)

// The reasons a listing stopped short. Separate strings rather than a boolean
// because they call for different things from the caller: a count ceiling is
// narrowed past with a filter, and a size ceiling means the rows themselves
// are large and narrowing is the only option.
const (
	reasonCount    = "the account holds more than were returned; narrow with a filter or ask for fewer"
	reasonSize     = "the result reached the size one tool call may return; narrow it"
	reasonEncoding = "a record could not be encoded and the listing stops there"
)

// truncation is what every listing carries when it stops short.
//
// A field rather than a log line, because a model shown twenty of two hundred
// numbers and not told so will answer as though it saw them all.
type truncation struct {
	Truncated bool   `json:"truncated,omitempty"`
	Reason    string `json:"truncation_reason,omitempty"`
}

// bound trims rows to the byte ceiling and says so. The count ceiling is
// applied upstream by list, which stops fetching; this is the second ceiling,
// on how much those rows weigh.
func bound[T any](rows []T, alreadyCut bool) ([]T, truncation) {
	var cut truncation
	if alreadyCut {
		cut = truncation{Truncated: true, Reason: reasonCount}
	}
	total := 0
	for i, row := range rows {
		encoded, err := json.Marshal(row)
		if err != nil {
			return rows[:i], truncation{Truncated: true, Reason: reasonEncoding}
		}
		total += len(encoded)
		if total > maxResultBytes {
			return rows[:i], truncation{Truncated: true, Reason: reasonSize}
		}
	}
	return rows, cut
}

// e164 normalises a telephone number to the form Flowroute uses as an id:
// digits only, with the country code and no plus.
//
// Written for the North American plan on purpose. Flowroute sells US and
// Canadian numbers, every id seen from the API is eleven digits beginning 1,
// and a bare ten-digit number is what a person types. A number that is neither
// is passed through as its digits rather than guessed at, so a mistake reaches
// the API as a 404 naming the number instead of a lookup of some other
// country's subscriber.
func e164(in string) (string, error) {
	var digits strings.Builder
	for _, r := range in {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	d := digits.String()
	switch {
	case d == "":
		return "", fmt.Errorf("flowroute: %q has no digits in it; write a telephone "+
			"number such as +1 206 555 0100", in)
	case len(d) == 10:
		return "1" + d, nil
	case len(d) == 11 && strings.HasPrefix(d, "1"):
		return d, nil
	case len(d) < 10:
		return "", fmt.Errorf("flowroute: %q is only %d digits; a telephone number "+
			"needs at least ten", in, len(d))
	}
	return d, nil
}

// display writes a stored number back the way a person reads one.
func display(number string) string {
	if len(number) == 11 && strings.HasPrefix(number, "1") {
		return "+1 " + number[1:4] + " " + number[4:7] + " " + number[7:]
	}
	if number == "" {
		return ""
	}
	return "+" + number
}

// blank turns a JSON null into the empty string. Flowroute sends null for an
// alias, a note or an edge strategy that was never set, and a *string in every
// row would make each of them a pointer nobody wants to dereference.
//
// It accepts a number as well, for the same reason entityID does: this API
// sends an id as the number 1 in one place and as a string everywhere else,
// and a field that is a string in every response anybody has seen is not a
// promise about the response nobody has.
type blank string

// UnmarshalJSON accepts a string, a number, or null.
func (b *blank) UnmarshalJSON(data []byte) error {
	if strings.TrimSpace(string(data)) == "null" {
		*b = ""
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*b = blank(strings.TrimSpace(s))
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(data, &n); err != nil {
		return fmt.Errorf("flowroute: a field was neither a string nor a number: %s",
			strings.TrimSpace(string(data)))
	}
	*b = blank(n.String())
	return nil
}

// String renders the value.
func (b blank) String() string { return string(b) }
