package supportinfo

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

/*
The event log, which is the best thing in the bundle.

Everything else here is read out of text logs with regular expressions and hope.
This one arrives already structured — one row per event, with a source, a
numeric id and a timestamp — because it is a database table 3CX keeps for its
own console. Fifty thousand rows of it covering a fortnight.

There is one trap, and it is a bad one: the severity column cannot be believed.
"A backup of your PBX has been successfully completed" is filed as an Error, and
so is every provisioning file the system has ever generated. A tool that took
that column at face value would open on a wall of red, most of it routine, and a
technician would learn within a day to ignore the whole screen — which is worse
than not having built it, because the twenty events that did matter are in there
too.

So severity is decided here, by event id, from the catalogue below. Ids nobody
has catalogued keep the file's own severity knocked down a step, on the grounds
that an unknown event is worth mentioning and not worth alarming anybody about.
*/

// Events is the log grouped into the handful of distinct things that happened.
//
// Fifty thousand rows is not readable and is not the point: they collapse into
// perhaps thirty kinds of event, and the count against each is what tells you
// which one is the fault.
type Events struct {
	Total  int          `json:"total"`
	From   time.Time    `json:"from,omitempty"`
	To     time.Time    `json:"to,omitempty"`
	Groups []EventGroup `json:"groups,omitempty"`
}

// EventGroup is one kind of event and how often it happened.
type EventGroup struct {
	Source string `json:"source"`
	ID     string `json:"id"`
	// Label is what this event is, in our words rather than 3CX's. Empty when
	// the id is not in the catalogue, in which case Sample has to speak for it.
	Label    string    `json:"label,omitempty"`
	Severity Severity  `json:"severity"`
	Count    int       `json:"count"`
	First    time.Time `json:"first,omitempty"`
	Last     time.Time `json:"last,omitempty"`
	Sample   string    `json:"sample,omitempty"`
	// Says is 3CX's own severity, kept so the reinterpretation above is
	// visible rather than silent.
	Says string `json:"says,omitempty"`
}

/*
event is what we know about one event id.

base is what a single occurrence means. many is what it means once there are
manyAt of them, because a trunk that dropped once at three in the morning is
routine and a trunk that dropped six thousand times is the reason the phones do
not work. Where many is empty the count does not change the reading.
*/
type event struct {
	label  string
	base   Severity
	many   Severity
	manyAt int
	// detail explains the event in the terms a technician would use to a
	// customer, and is what ends up in a finding.
	detail string
	// quiet marks the events that are 3CX telling us it did its job. They are
	// still counted and still listed; they never become findings.
	quiet bool
}

/*
catalogue is the event ids seen in real bundles, judged.

Deliberately short. Every entry here was read off an actual customer's system
and checked against what 3CX's own console says about it; guessing at ids from
documentation would produce a table that looks authoritative and mislabels
things nobody would catch. Unknown ids are handled gracefully, so the cost of
leaving one out is small and the cost of getting one wrong is not.
*/
var catalogue = map[string]event{
	"4102": {
		label: "Trunk or SBC changed state", base: Note, many: Critical, manyAt: 20,
		detail: "A trunk or SBC has been going up and down. While it is down nothing " +
			"can be dialled through it and inbound calls fail at the carrier, so a " +
			"trunk that flaps takes the phones with it. Usually the network path to " +
			"the device rather than the device itself.",
	},
	"30051": {
		label: "Unidentified incoming call", base: Note, many: Warning, manyAt: 25,
		detail: "Calls are arriving that the phone system cannot match to a trunk, so " +
			"it does not know where to send them. The caller hears nothing useful. " +
			"Fixed by adding the sending address to the trunk's source identification.",
	},
	"12294": {
		label: "Call or registration failed", base: Warning, many: Critical, manyAt: 25,
		detail: "The carrier refused calls or registrations, and the reply code in the " +
			"message says why — authentication, a number it will not accept, or a " +
			"trunk that is not registered.",
	},
	"30053": {
		label: "Call loop detected", base: Warning,
		detail: "A call was routed back to somewhere it had already been and was cut to " +
			"stop it going round forever. Nearly always a forwarding rule pointing at " +
			"something that points back.",
	},
	"10025": {
		label: "Logs deleted to free disk space", base: Warning, many: Critical, manyAt: 5,
		detail: "The phone system ran short of disk and deleted its own logs to cope. " +
			"That is a machine under real pressure, and the next thing it runs out of " +
			"space for is call recordings or the database.",
	},
	"50020": {
		label: "Push notification failed", base: Note, many: Warning, manyAt: 10,
		detail: "Mobile app users were not woken for calls, so their phone did not ring " +
			"until they opened the app.",
	},
	"105": {
		label: "Caller abandoned a queue", base: Note, many: Warning, manyAt: 20,
		detail: "Callers hung up while waiting in a queue. Worth reading as a staffing " +
			"number rather than a fault unless it is sudden.",
	},
	"10034": {label: "Call quality report", base: Note, quiet: true},
	"10029": {label: "Phone provisioning file served", base: Note, quiet: true},
	"10031": {label: "Backup completed", base: Note, quiet: true},
	"10027": {label: "Database maintenance completed", base: Note, quiet: true},
}

var (
	// The trunk or device a 4102 is about, so the finding can name it.
	trunkNamed = regexp.MustCompile(`'([^']{1,60})'`)
	// The carrier's refusal in a 12294, which is the whole diagnosis.
	sipReply = regexp.MustCompile(`replied\s+(\d{3}[^.\n]{0,40})`)
)

// eventTotals accumulates the log as it streams past.
type eventTotals struct {
	total  int
	from   time.Time
	to     time.Time
	groups map[string]*EventGroup
	// named collects the distinct things a group was about — which trunks
	// flapped, which reply codes came back — because "6000 failures" is not
	// actionable and "6000 failures, all of them DellSBC" is.
	named  map[string]map[string]int
	source string
}

func newEventTotals() *eventTotals {
	return &eventTotals{groups: map[string]*EventGroup{}, named: map[string]map[string]int{}}
}

// readEvent takes one row of eventlog.csv.
func (e *eventTotals) readEvent(source, id, severity, message string, at time.Time) {
	if id == "" {
		return
	}
	e.total++
	if !at.IsZero() {
		if e.from.IsZero() || at.Before(e.from) {
			e.from = at
		}
		if at.After(e.to) {
			e.to = at
		}
	}

	key := source + "/" + id
	g, ok := e.groups[key]
	if !ok {
		known := catalogue[id]
		g = &EventGroup{
			Source: source, ID: id, Label: known.label,
			Severity: judgeEvent(id, severity, 1), Says: severity,
			Sample: firstLine(message),
		}
		e.groups[key] = g
	}
	g.Count++
	g.Severity = judgeEvent(id, severity, g.Count)
	if !at.IsZero() {
		if g.First.IsZero() || at.Before(g.First) {
			g.First = at
		}
		if at.After(g.Last) {
			g.Last = at
		}
	}

	switch id {
	case "4102":
		e.note(key, trunkNamed.FindStringSubmatch(message))
	case "12294":
		e.note(key, sipReply.FindStringSubmatch(message))
	}
}

func (e *eventTotals) note(key string, m []string) {
	if m == nil {
		return
	}
	if e.named[key] == nil {
		e.named[key] = map[string]int{}
	}
	// Bounded: a group whose detail is unique per event (a caller's number,
	// say) would otherwise grow a map the size of the log.
	if len(e.named[key]) < 12 {
		e.named[key][strings.TrimSpace(m[1])]++
	}
}

/*
judgeEvent decides what an event means, given how many of it there are.

For a catalogued id this is our reading rather than 3CX's. For an unknown one
it is 3CX's, moved down a step: the column over-reports, and an unrecognised
event should be visible without being alarming.
*/
func judgeEvent(id, says string, count int) Severity {
	if known, ok := catalogue[id]; ok {
		if known.many != "" && count >= known.manyAt {
			return known.many
		}
		if known.base != "" {
			return known.base
		}
		return Note
	}
	switch strings.ToLower(strings.TrimSpace(says)) {
	case "error":
		return Warning
	default:
		return Note
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// result is the grouped log, worst and busiest first.
func (e *eventTotals) result() Events {
	out := Events{Total: e.total, From: e.from, To: e.to}
	for _, g := range e.groups {
		out.Groups = append(out.Groups, *g)
	}
	rank := map[Severity]int{Critical: 0, Warning: 1, Note: 2}
	sort.SliceStable(out.Groups, func(i, j int) bool {
		if rank[out.Groups[i].Severity] != rank[out.Groups[j].Severity] {
			return rank[out.Groups[i].Severity] < rank[out.Groups[j].Severity]
		}
		return out.Groups[i].Count > out.Groups[j].Count
	})
	return out
}

// findings turns the groups that matter into sentences.
func (e *eventTotals) findings() []Finding {
	var out []Finding
	for key, g := range e.groups {
		known, ok := catalogue[g.ID]
		if ok && known.quiet {
			continue
		}
		if g.Severity == Note {
			continue
		}

		detail := known.detail
		if detail == "" {
			detail = "The phone system logged this " + plural(g.Count, "time", "times") +
				". It is not one of the events this reader recognises, so this is 3CX's own wording for it."
		}
		if names := e.named[key]; len(names) > 0 {
			detail += " Mostly: " + strings.Join(topNames(names, 3), ", ") + "."
		}

		title := g.Label
		if title == "" {
			title = g.Source + " event " + g.ID
		}
		if g.Count > 1 {
			title = fmt.Sprintf("%s, %s", title, plural(g.Count, "time", "times"))
		}

		out = append(out, Finding{
			Severity:    g.Severity,
			Title:       title,
			Detail:      detail,
			Evidence:    []string{g.Sample},
			Occurrences: g.Count,
			Source:      e.source,
		})
	}
	return out
}

// topNames is the most common few, biggest first.
func topNames(counts map[string]int, limit int) []string {
	type pair struct {
		name string
		n    int
	}
	pairs := make([]pair, 0, len(counts))
	for name, n := range counts {
		pairs = append(pairs, pair{name, n})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].n != pairs[j].n {
			return pairs[i].n > pairs[j].n
		}
		return pairs[i].name < pairs[j].name
	})
	out := make([]string, 0, limit)
	for i, p := range pairs {
		if i == limit {
			break
		}
		out = append(out, fmt.Sprintf("%s (%d)", p.name, p.n))
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
