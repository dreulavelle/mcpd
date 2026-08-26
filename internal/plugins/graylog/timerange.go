package graylog

import (
	"fmt"
	"strings"
	"time"
)

// timeArgs are the window fields every searching tool takes.
//
// Embedded rather than repeated so that the three tools which search cannot
// drift apart in what they accept, and so the rules below are written once.
type timeArgs struct {
	RangeSeconds int    `json:"range_seconds,omitempty" jsonschema:"how far back to search, in seconds, ending now; the usual way to ask"`
	From         string `json:"from,omitempty" jsonschema:"start of an exact window, RFC3339 e.g. 2026-08-25T14:00:00Z; needs to"`
	To           string `json:"to,omitempty" jsonschema:"end of an exact window, RFC3339; needs from"`
	Keyword      string `json:"keyword,omitempty" jsonschema:"a window in words, e.g. yesterday or last 2 hours; Graylog parses it, so prefer range_seconds when you know the number"`
}

// timeRange is the JSON Graylog takes. One struct for all three kinds because
// the API discriminates on the type field rather than by shape.
type timeRange struct {
	Type    string `json:"type"`
	Range   int    `json:"range,omitempty"`
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
	Keyword string `json:"keyword,omitempty"`
}

// describe renders the window for the note a tool result carries, so a model
// that did not name one can see what it was given.
func (t timeRange) describe() string {
	switch t.Type {
	case "absolute":
		return t.From + " to " + t.To
	case "keyword":
		return fmt.Sprintf("%q, as Graylog parsed it", t.Keyword)
	default:
		return "the last " + humanSeconds(t.Range)
	}
}

// resolve turns what a caller asked for into the window that will be sent.
//
// Precedence is exactness first: an explicit pair of timestamps beats a number
// of seconds, which beats a phrase. A caller who supplies more than one is
// refused rather than silently having one ignored -- the whole point of a time
// range is that somebody knows which window they are looking at, and quietly
// picking for them is how an answer comes to describe a different hour than
// the question did.
//
// A range is always produced. Graylog treats a relative range of zero as every
// message it holds, so "the caller named nothing" must never reach the API as
// a zero; it becomes the configured default here.
func (c Config) resolve(in timeArgs, now time.Time) (timeRange, error) {
	named := 0
	if in.From != "" || in.To != "" {
		named++
	}
	if in.RangeSeconds > 0 {
		named++
	}
	if strings.TrimSpace(in.Keyword) != "" {
		named++
	}
	if named > 1 {
		return timeRange{}, fmt.Errorf("graylog: name the window one way -- " +
			"range_seconds, or from and to, or keyword. More than one was " +
			"given and choosing between them would mean searching a different " +
			"window than the one asked about")
	}

	switch {
	case in.From != "" || in.To != "":
		return c.absolute(in, now)
	case strings.TrimSpace(in.Keyword) != "":
		return c.keyword(in.Keyword)
	case in.RangeSeconds > 0:
		return c.relative(in.RangeSeconds)
	default:
		return timeRange{Type: "relative", Range: c.DefaultRangeSeconds}, nil
	}
}

// relative builds a "last N seconds" window.
func (c Config) relative(seconds int) (timeRange, error) {
	if err := c.withinCeiling(seconds, "range_seconds"); err != nil {
		return timeRange{}, err
	}
	return timeRange{Type: "relative", Range: seconds}, nil
}

// absolute builds an exact window, and is strict about it.
//
// Both ends are required. A half-open window is not a smaller ask, it is an
// ambiguous one -- "from Tuesday" could mean until now or until the end of
// Tuesday, and the two differ by days of indices on a busy cluster.
func (c Config) absolute(in timeArgs, now time.Time) (timeRange, error) {
	if in.From == "" || in.To == "" {
		return timeRange{}, fmt.Errorf("graylog: an exact window needs both " +
			"from and to; one on its own does not say where the window ends")
	}
	from, err := parseInstant(in.From, "from")
	if err != nil {
		return timeRange{}, err
	}
	to, err := parseInstant(in.To, "to")
	if err != nil {
		return timeRange{}, err
	}
	if !to.After(from) {
		return timeRange{}, fmt.Errorf("graylog: to (%s) is not after from (%s), "+
			"so the window is empty", in.To, in.From)
	}
	// The ceiling is on how far back a search reaches rather than on how wide
	// it is: an operator who caps searches at seven days means "not older than
	// seven days", and a narrow window a year ago is exactly what they meant
	// to refuse.
	if err := c.withinCeiling(int(now.Sub(from)/time.Second), "from"); err != nil {
		return timeRange{}, err
	}
	return timeRange{
		Type: "absolute",
		From: from.UTC().Format(time.RFC3339Nano),
		To:   to.UTC().Format(time.RFC3339Nano),
	}, nil
}

// keyword passes a phrase to Graylog's own parser.
//
// Refused outright when a ceiling is configured. Graylog resolves the phrase
// on its side, so "last year" is a window this code cannot measure and
// therefore cannot hold to a limit -- and a ceiling with one way around it is
// not a ceiling. Saying so beats enforcing it everywhere except here.
func (c Config) keyword(phrase string) (timeRange, error) {
	if c.MaxRangeSeconds > 0 {
		return timeRange{}, fmt.Errorf("graylog: this installation caps how far "+
			"back a search may reach (%s), and a keyword window is resolved by "+
			"Graylog rather than here, so it cannot be checked against the cap. "+
			"Use range_seconds, or from and to",
			humanSeconds(c.MaxRangeSeconds))
	}
	return timeRange{Type: "keyword", Keyword: strings.TrimSpace(phrase)}, nil
}

// withinCeiling enforces max_range_seconds.
func (c Config) withinCeiling(seconds int, field string) error {
	if c.MaxRangeSeconds <= 0 || seconds <= c.MaxRangeSeconds {
		return nil
	}
	return fmt.Errorf("graylog: %s reaches back %s, and this installation caps "+
		"a search at %s. Narrow the window",
		field, humanSeconds(seconds), humanSeconds(c.MaxRangeSeconds))
}

// parseInstant accepts the timestamp spellings somebody actually types.
//
// RFC3339 is the documented form and the one the schema asks for. The other
// two are what a model produces when it drops the zone or the T, and refusing
// them would cost a round trip to be told about a colon.
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
		// make the same call mean different hours on different machines, and
		// Graylog stores in UTC anyway.
		if t, err := time.Parse(layout, value); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("graylog: %s is %q, which is not a timestamp "+
		"this understands. Use RFC3339, e.g. 2026-08-25T14:00:00Z", field, value)
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
