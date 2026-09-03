package supportinfo

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

/*
The audit log, which answers the question that comes before every other one.

"It was working last week" is how most of these tickets start, and the useful
reply is not a metric — it is a list of what somebody changed. 3CX keeps one:
every setting edited, by whom, from which address, with the value before and
the value after.

It is also the largest thing in the bundle by a distance. Half a million rows
and a hundred and twenty megabytes on a busy system, which is more than
everything else put together, and it is why this file streams rather than reads.

Almost all of it is noise. Ninety-eight per cent of those rows are somebody
opening the web client, recorded as a numeric action against a numeric object
type with no before and no after. The rows that matter are the ones carrying a
value that changed, and on a real system there are perhaps fifty of them in a
year — which is a page somebody can actually read.

The action and object columns are enums 3CX has not published, so they are not
guessed at. What is shown instead is the part that needs no key: the object's
name, and the JSON of what it was and what it became.
*/

// Changes is the configuration history, distilled.
type Changes struct {
	// Rows is how many audit entries were read, so the handful of edits below
	// are visibly a filtered view of something much larger.
	Rows int `json:"rows"`
	// Edits are the entries that actually changed a setting.
	Edits []Edit `json:"edits,omitempty"`
	// Signins is who has been using this phone system, and Addresses is where
	// from. Both are counted rather than listed.
	Signins   []Counted `json:"signins,omitempty"`
	Addresses []Counted `json:"addresses,omitempty"`
	// From and To bound the history.
	From time.Time `json:"from,omitempty"`
	To   time.Time `json:"to,omitempty"`
}

// Edit is one setting somebody changed.
type Edit struct {
	At     time.Time `json:"at"`
	User   string    `json:"user,omitempty"`
	IP     string    `json:"ip,omitempty"`
	Object string    `json:"object,omitempty"`
	Before string    `json:"before,omitempty"`
	After  string    `json:"after,omitempty"`
}

// maxEdits bounds what is kept. Fifty is more history than anybody reads in
// one sitting and small enough to store with the rest of the report.
const maxEdits = 50

// auditTotals accumulates the audit log as it streams past.
type auditTotals struct {
	rows      int
	edits     []Edit
	users     map[string]int
	addresses map[string]int
	from      time.Time
	to        time.Time
}

func newAuditTotals() *auditTotals {
	return &auditTotals{users: map[string]int{}, addresses: map[string]int{}}
}

// readAudit takes one row of audit_log.csv.
func (a *auditTotals) readAudit(at time.Time, user, ip, object, before, after string) {
	a.rows++
	if !at.IsZero() {
		if a.from.IsZero() || at.Before(a.from) {
			a.from = at
		}
		if at.After(a.to) {
			a.to = at
		}
	}
	if user = strings.TrimSpace(user); user != "" && len(a.users) < 200 {
		a.users[user]++
	}
	if ip = strings.TrimSpace(ip); ip != "" && len(a.addresses) < 500 {
		a.addresses[ip]++
	}

	before, after = strings.TrimSpace(before), strings.TrimSpace(after)
	if before == "" && after == "" {
		return
	}
	a.edits = append(a.edits, Edit{
		At: at, User: user, IP: ip,
		Object: strings.TrimSpace(object),
		Before: shorten(before), After: shorten(after),
	})
	// Kept newest-last while streaming and trimmed from the front, so a system
	// with thousands of edits keeps the recent ones rather than the oldest.
	if len(a.edits) > maxEdits*2 {
		a.edits = a.edits[len(a.edits)-maxEdits:]
	}
}

// shorten bounds one side of a diff. Some objects serialise their whole state.
func shorten(s string) string {
	const limit = 300
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}

func (a *auditTotals) result() *Changes {
	if a.rows == 0 {
		return nil
	}
	sort.SliceStable(a.edits, func(i, j int) bool { return a.edits[i].At.After(a.edits[j].At) })
	if len(a.edits) > maxEdits {
		a.edits = a.edits[:maxEdits]
	}
	return &Changes{
		Rows:      a.rows,
		Edits:     a.edits,
		Signins:   counted(a.users, 8),
		Addresses: counted(a.addresses, 8),
		From:      a.from,
		To:        a.to,
	}
}

/*
findings reports configuration changed close to the capture.

Deliberately a note rather than a warning. A change is not a fault, and calling
it one would train people to ignore the section. What it is is the first thing
worth checking when something worked last week, which is why it is here at all.
*/
func (a *auditTotals) findings(source string) []Finding {
	result := a.result()
	if result == nil || len(result.Edits) == 0 {
		return nil
	}

	// Recent means within the week the metrics cover, so this lines up with
	// the charts either side of it.
	cutoff := result.To.AddDate(0, 0, -7)
	var recent []Edit
	for _, e := range result.Edits {
		if e.At.After(cutoff) {
			recent = append(recent, e)
		}
	}
	if len(recent) == 0 {
		return nil
	}

	evidence := make([]string, 0, maxEvidence)
	for i, e := range recent {
		if i == maxEvidence {
			break
		}
		evidence = append(evidence, fmt.Sprintf("%s — %s changed %s to %s",
			e.At.Format("2 Jan 15:04"), e.User, orUnnamed(e.Object), e.After))
	}

	return []Finding{{
		Severity: Note,
		Title:    fmt.Sprintf("%s changed in the last week", plural(len(recent), "setting was", "settings were")),
		Detail: "Not a fault in itself, but it is the first thing to check when something worked before " +
			"and does not now. The full before-and-after for each is below.",
		Evidence:    evidence,
		Occurrences: len(recent),
		Source:      source,
	}}
}

func orUnnamed(s string) string {
	if s == "" {
		return "a setting"
	}
	return s
}
