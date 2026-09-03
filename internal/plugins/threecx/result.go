package threecx

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

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
	reasonCount    = "the phone system holds more than were returned; narrow with a query or a filter"
	reasonSize     = "the result reached the size one tool call may return; narrow it"
	reasonEncoding = "a record could not be encoded and the listing stops there"
)

// truncation is what every listing carries when it stops short.
//
// A field rather than a log line, because a model shown twenty of two hundred
// extensions and not told so will answer as though it saw them all.
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

// odataString escapes a value for an OData string literal, where the quote is
// doubled. Without it a name containing an apostrophe would end the literal and
// the rest would be read as filter syntax.
func odataString(v string) string {
	return "'" + strings.ReplaceAll(v, "'", "''") + "'"
}

// parseTime reads a caller's timestamp. Parsed rather than passed through,
// because a $filter is code and the value came from a model. A date without a
// time is accepted as midnight UTC. An empty value is no bound at all.
func parseTime(field, value string) (time.Time, bool, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC(), true, nil
		}
	}
	return time.Time{}, false, fmt.Errorf("%s %q is not a timestamp; write it as 2026-09-01T14:00:00Z or 2026-09-01", field, value)
}

// odataTime renders a caller's timestamp as the literal OData takes for a
// DateTimeOffset: unquoted RFC 3339 in UTC.
func odataTime(field, value string) (string, error) {
	t, ok, err := parseTime(field, value)
	if err != nil || !ok {
		return "", err
	}
	return t.Format(time.RFC3339), nil
}

// isoDuration turns 3CX's ISO 8601 duration into something a person reads.
// The PBX answers PT2M13.5S for a call; nobody wants to see that on a record.
func isoDuration(iso string) string {
	iso = strings.TrimSpace(iso)
	if iso == "" || iso == "PT0S" {
		return "0s"
	}
	out := strings.TrimPrefix(iso, "P")
	out = strings.ReplaceAll(out, "T", "")
	out = strings.ToLower(out)
	if i := strings.Index(out, "."); i >= 0 {
		out = out[:i] + "s"
	}
	return out
}

// clock reads a duration-from-midnight as a time of day: PT13H30M is 13:30.
// Empty comes back empty rather than as 00:00 -- a closure covering whole days
// has no time, and midnight is a real answer that would be indistinguishable
// from the absence of one.
func clock(duration string) string {
	rest, found := strings.CutPrefix(strings.TrimSpace(duration), "PT")
	if !found {
		return ""
	}
	var hours, minutes int
	if before, after, ok := strings.Cut(rest, "H"); ok {
		fmt.Sscanf(before, "%d", &hours)
		rest = after
	}
	if before, _, ok := strings.Cut(rest, "M"); ok {
		fmt.Sscanf(before, "%d", &minutes)
	}
	if hours == 0 && minutes == 0 {
		return ""
	}
	return fmt.Sprintf("%02d:%02d", hours, minutes)
}

// hhmm drops the seconds the PBX keeps on an office-hours time.
func hhmm(t string) string {
	if len(t) >= 5 {
		return t[:5]
	}
	return t
}

// dateText writes a holiday's date the way it means it: 2026-12-25 for one
// that happens once, --12-25 for one that repeats every year -- the shape RFC
// 3339 already has for a recurring date, and one that sorts within a year the
// way a person expects.
func dateText(year, month, day int) string {
	if month == 0 || day == 0 {
		return ""
	}
	if year == 0 {
		return fmt.Sprintf("--%02d-%02d", month, day)
	}
	return fmt.Sprintf("%04d-%02d-%02d", year, month, day)
}

// destination is where 3CX sends a call: a complex type it uses for forwarding
// rules, inbound rules, queue and ring group overflow and IVR timeouts alike.
type destination struct {
	To       string `json:"To"`
	Number   string `json:"Number"`
	Name     string `json:"Name"`
	External string `json:"External"`
}

// text renders a destination as one phrase: "Extension 101 (Alice Adams)",
// "VoiceMail of 101", "External +15551234567", or "None".
//
// One string rather than four fields, because it appears many times per row
// and a reader wants to know where the call goes, not which of the four fields
// happened to carry it.
func (d *destination) text() string {
	if d == nil || d.To == "" || d.To == "None" {
		return "None"
	}
	switch d.To {
	case "External":
		if d.External != "" {
			return "External " + d.External
		}
	case "VoiceMail", "VoiceMailOfDestination":
		if d.Number != "" {
			return "VoiceMail of " + d.Number
		}
		return "VoiceMail"
	}
	out := d.To
	if d.Number != "" {
		out += " " + d.Number
	}
	if d.Name != "" && d.Name != d.Number {
		out += " (" + d.Name + ")"
	}
	return out
}

// firstNonBlank returns the first value with something in it.
func firstNonBlank(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// matches reports whether any of fields contains the caller's query, folding
// case. For narrowing an answer the PBX cannot narrow itself.
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
