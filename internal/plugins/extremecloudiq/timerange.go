package extremecloudiq

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// auditWindowLimit is the widest window the audit log endpoint accepts. Thirty
// days, documented on the endpoint, enforced here so the refusal names the
// limit instead of arriving as a 400 about a parameter.
const auditWindowLimit = 30 * 24 * time.Hour

// timeArgs are the window fields every tool that reaches a windowed endpoint
// takes.
//
// Embedded rather than repeated so the tools cannot drift apart in what they
// accept, and so the rules below are written once.
//
// There is no phrase form here, unlike the Graylog integration. ExtremeCloud
// IQ has no parser for "yesterday" -- it takes two epoch timestamps and
// nothing else -- so a phrase would have to be resolved on this side, and a
// window this code resolved but presented as the API's would be a guess
// wearing the API's authority.
type timeArgs struct {
	RangeSeconds int    `json:"range_seconds,omitempty" jsonschema:"how far back to look, in seconds, ending now; the usual way to ask"`
	From         string `json:"from,omitempty" jsonschema:"start of an exact window, RFC3339 e.g. 2026-08-25T14:00:00Z; needs to"`
	To           string `json:"to,omitempty" jsonschema:"end of an exact window, RFC3339; needs from"`
}

// window is a resolved window, in the form the API takes.
type window struct {
	From time.Time
	To   time.Time
}

// apply writes the window onto a query as epoch milliseconds.
//
// Milliseconds is the whole reason this type exists. Every windowed endpoint
// here takes them, seconds is the unit everybody reaches for first, and a
// window a thousand times too narrow does not fail -- it returns nothing, from
// a moment in January 1970, which reads exactly like an estate with no alerts.
func (w window) apply(q url.Values) {
	q.Set("startTime", strconv.FormatInt(w.From.UnixMilli(), 10))
	q.Set("endTime", strconv.FormatInt(w.To.UnixMilli(), 10))
}

// describe renders the window for the note a tool result carries, so a model
// that did not name one can see what it was given.
//
// Reported on every call rather than only when it was defaulted: a count
// without the window it covers is a number with no unit, and a model that has
// to infer the window will infer the one in the question rather than the one
// in the answer.
func (w window) describe() string {
	return w.From.UTC().Format(time.RFC3339) + " to " + w.To.UTC().Format(time.RFC3339)
}

// span is how wide the window is.
func (w window) span() time.Duration { return w.To.Sub(w.From) }

// resolve turns what a caller asked for into the window that will be sent.
//
// Precedence is exactness: an explicit pair of timestamps beats a number of
// seconds. A caller who supplies both is refused rather than silently having
// one ignored -- the whole point of a window is that somebody knows which one
// they are looking at, and quietly picking for them is how an answer comes to
// describe a different day than the question did.
//
// A window is always produced. Every endpoint this reaches requires both ends,
// so "the caller named nothing" must never arrive as a zero; it becomes the
// configured default here.
func (c Config) resolve(in timeArgs, now time.Time) (window, error) {
	exact := in.From != "" || in.To != ""
	if exact && in.RangeSeconds > 0 {
		return window{}, fmt.Errorf("extremecloudiq: name the window one way -- " +
			"range_seconds, or from and to. Both were given and choosing " +
			"between them would mean answering about a different window than " +
			"the one asked about")
	}

	if exact {
		return c.absolute(in, now)
	}
	seconds := in.RangeSeconds
	if seconds <= 0 {
		seconds = c.DefaultRangeSeconds
	}
	if err := c.withinCeiling(seconds, "range_seconds"); err != nil {
		return window{}, err
	}
	return window{From: now.Add(-time.Duration(seconds) * time.Second), To: now}, nil
}

// absolute builds an exact window, and is strict about it.
//
// Both ends are required. A half-open window is not a smaller ask, it is an
// ambiguous one -- "from Tuesday" could mean until now or until the end of
// Tuesday, and the two differ by days.
func (c Config) absolute(in timeArgs, now time.Time) (window, error) {
	if in.From == "" || in.To == "" {
		return window{}, fmt.Errorf("extremecloudiq: an exact window needs both " +
			"from and to; one on its own does not say where the window ends")
	}
	from, err := parseInstant(in.From, "from")
	if err != nil {
		return window{}, err
	}
	to, err := parseInstant(in.To, "to")
	if err != nil {
		return window{}, err
	}
	if !to.After(from) {
		return window{}, fmt.Errorf("extremecloudiq: to (%s) is not after from "+
			"(%s), so the window is empty", in.To, in.From)
	}
	// The ceiling is on how far back a read reaches rather than on how wide it
	// is: an operator who caps this at seven days means "not older than seven
	// days", and a narrow window a year ago is exactly what they meant to
	// refuse.
	if err := c.withinCeiling(int(now.Sub(from)/time.Second), "from"); err != nil {
		return window{}, err
	}
	return window{From: from, To: to}, nil
}

// withinCeiling enforces max_range_seconds.
func (c Config) withinCeiling(seconds int, field string) error {
	if c.MaxRangeSeconds <= 0 || seconds <= c.MaxRangeSeconds {
		return nil
	}
	return fmt.Errorf("extremecloudiq: %s reaches back %s, and this host caps a "+
		"read at %s. Narrow the window",
		field, humanSeconds(seconds), humanSeconds(c.MaxRangeSeconds))
}

// withinAuditLimit refuses a window the audit endpoint will not accept.
//
// Checked here rather than left to the API because the API's refusal is a 400
// naming a parameter and a number of milliseconds, which reads as a malformed
// request rather than as a window to narrow.
func withinAuditLimit(w window) error {
	if w.span() <= auditWindowLimit {
		return nil
	}
	return fmt.Errorf("extremecloudiq: the audit log covers at most 30 days in "+
		"one request, and this window is %s wide. Ask for a narrower one, or "+
		"walk it a month at a time", humanSeconds(int(w.span()/time.Second)))
}

// parseInstant accepts the timestamp spellings somebody actually types.
//
// RFC3339 is the documented form and the one the schema asks for. The others
// are what a model produces when it drops the zone or the T, and refusing them
// would cost a round trip to be told about a colon.
func parseInstant(value, field string) (time.Time, error) {
	value = strings.TrimSpace(value)
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		// UTC where no zone was given. Guessing the host's local zone would
		// make the same call mean different hours on different machines.
		if t, err := time.Parse(layout, value); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("extremecloudiq: %s is %q, which is not a "+
		"timestamp this understands. Use RFC3339, e.g. 2026-08-25T14:00:00Z",
		field, value)
}

// humanSeconds renders a duration the way somebody would say it, for messages
// an operator reads.
func humanSeconds(seconds int) string {
	d := time.Duration(seconds) * time.Second
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	case d >= 2*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	case d >= 2*time.Minute:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	default:
		return fmt.Sprintf("%d seconds", seconds)
	}
}
