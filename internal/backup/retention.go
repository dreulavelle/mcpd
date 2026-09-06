package backup

import (
	"fmt"
	"sort"
	"time"
)

// dated is one of mcpd's own archives, paired with the instant its name
// carries. Package level rather than local to Retain so the abort conditions
// can be read beside the rules they guard.
type dated struct {
	obj Object
	at  time.Time
}

// Deciding what to delete is the most dangerous thing in this package, so it is
// the part with no I/O in it.
//
// Retain is a pure function over a listing. Everything that could make it wrong
// -- a destination whose clock is off, a server answering a listing with half
// the truth, a shared folder holding somebody else's files -- is a property of
// its input, and can be given to it in a test.

// Retained is what retention decided, and why it decided nothing when it did.
type Retained struct {
	// Remove is what may be deleted, oldest first.
	Remove []Object
	// Kept is how many of mcpd's own archives are staying.
	Kept int
	// Seen is how many of them were in the listing at all, which is what the
	// next run compares against.
	Seen int
	// Held is empty when retention ran, and otherwise says in one sentence why
	// it did not. It is recorded against the run rather than failing it: a
	// backup that uploaded and did not prune is a good night's work.
	Held string
}

// Retain decides which of a destination's archives may go.
//
// uploaded is the name this run has just written, and seenBefore how many
// archives the last successful run found here.
//
// Four rules, OR-ed, each keeping the newest thing in its period: the last N
// whatever their dates, and the newest in each of the last so many days, weeks
// and months. Anything kept by any rule is kept.
func Retain(objects []Object, p Policy, uploaded string, seenBefore int, loc *time.Location) Retained {
	if loc == nil {
		loc = time.UTC
	}

	// Only mcpd's own, and only ones whose name carries a time. A destination
	// is usually a shared folder; a file this cannot read the date out of is
	// not one of ours and is invisible from here on.
	ours := make([]dated, 0, len(objects))
	for _, o := range objects {
		if at, ok := TimeFromName(o.Name); ok {
			ours = append(ours, dated{obj: o, at: at})
		}
	}

	out := Retained{Seen: len(ours)}
	switch {
	case len(ours) == 0:
		// Not "nothing to do": a listing that came back empty moments after an
		// upload is a server that answered wrongly.
		out.Held = "Nothing was deleted here. The listing came back empty, which " +
			"cannot be true of a place mcpd has just written a backup to."
		return out

	case uploaded != "" && !containsName(ours, uploaded):
		out.Held = "Nothing was deleted here. The backup that was just uploaded " +
			"is not in the listing, so mcpd cannot tell what else is."
		return out

	case seenBefore >= 4 && len(ours) < seenBefore/2:
		// A destination that has genuinely lost half its archives has a
		// problem retention should not compound by deleting more.
		out.Held = "Nothing was deleted here. Far fewer backups are listed than " +
			"there were last time, so mcpd is not treating this listing as the " +
			"whole picture."
		return out
	}

	// Newest first. Every rule below reads in this order, and the newest object
	// is therefore always index zero.
	sort.Slice(ours, func(i, j int) bool {
		if ours[i].at.Equal(ours[j].at) {
			return ours[i].obj.Name > ours[j].obj.Name
		}
		return ours[i].at.After(ours[j].at)
	})

	keep := make(map[string]bool, len(ours))
	// The newest, always, whatever the policy says. A policy is a number
	// somebody typed, and no number should be able to mean "delete the only
	// backup there is".
	keep[ours[0].obj.Name] = true

	for i, d := range ours {
		if i < p.KeepLast {
			keep[d.obj.Name] = true
		}
	}

	periodic := func(count int, period func(time.Time) string) {
		if count <= 0 {
			return
		}
		seen := map[string]bool{}
		for _, d := range ours {
			bucket := period(d.at.In(loc))
			if seen[bucket] {
				continue
			}
			seen[bucket] = true
			if len(seen) > count {
				return
			}
			keep[d.obj.Name] = true
		}
	}
	periodic(p.KeepDaily, func(t time.Time) string { return t.Format("2006-01-02") })
	periodic(p.KeepWeekly, func(t time.Time) string {
		year, week := t.ISOWeek()
		return fmt.Sprintf("%d-W%02d", year, week)
	})
	periodic(p.KeepMonthly, func(t time.Time) string { return t.Format("2006-01") })

	// Oldest first, so a destination that refuses one delete still loses the
	// oldest of what it agreed to.
	for i := len(ours) - 1; i >= 0; i-- {
		if keep[ours[i].obj.Name] {
			out.Kept++
			continue
		}
		out.Remove = append(out.Remove, ours[i].obj)
	}
	return out
}

func containsName(ours []dated, name string) bool {
	for _, d := range ours {
		if d.obj.Name == name {
			return true
		}
	}
	return false
}
