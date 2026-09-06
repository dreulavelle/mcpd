package backup

import (
	"testing"
	"time"
)

// Retention is the only thing in this package that deletes, so these tests are
// mostly about what it refuses to do.

// obj builds one of this instance's archives at an instant, with a modified
// time deliberately unrelated to it -- because nothing here may read ModTime.
func obj(stamp string, modTime time.Time) Object {
	return named("nas", stamp, modTime)
}

// named builds an archive belonging to a given instance.
func named(slug, stamp string, modTime time.Time) Object {
	return Object{
		Name: "mcpd-" + slug + "-" + stamp + ".mcpdbak", Size: 1024, ModTime: modTime,
	}
}

func names(objs []Object) []string {
	out := make([]string, 0, len(objs))
	for _, o := range objs {
		out = append(out, o.Name)
	}
	return out
}

func equal(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// The rules, one table.
//
// Every case names the newest archive as the one just uploaded, because that is
// what a real run does and because an upload missing from the listing is its
// own abort condition, tested separately below.
func TestRetainAppliesTheRulesItWasGiven(t *testing.T) {
	// Six weekly archives, newest first: 8 February back to 4 January 2026.
	weekly := []Object{
		obj("20260208T040000Z", time.Time{}),
		obj("20260201T040000Z", time.Time{}),
		obj("20260125T040000Z", time.Time{}),
		obj("20260118T040000Z", time.Time{}),
		obj("20260111T040000Z", time.Time{}),
		obj("20260104T040000Z", time.Time{}),
	}
	uploaded := weekly[0].Name

	cases := []struct {
		name    string
		objects []Object
		policy  Policy
		// removed is oldest first, which is the order Retain returns.
		removed []string
	}{
		{
			name:    "keep-last three removes the three oldest",
			objects: weekly,
			policy:  Policy{KeepLast: 3},
			removed: []string{
				"mcpd-nas-20260104T040000Z.mcpdbak",
				"mcpd-nas-20260111T040000Z.mcpdbak",
				"mcpd-nas-20260118T040000Z.mcpdbak",
			},
		},
		{
			name:    "keep-last larger than the listing removes nothing",
			objects: weekly,
			policy:  Policy{KeepLast: 20},
			removed: nil,
		},
		{
			name:    "keep-last one still keeps the newest and nothing else",
			objects: weekly,
			policy:  Policy{KeepLast: 1},
			removed: []string{
				"mcpd-nas-20260104T040000Z.mcpdbak",
				"mcpd-nas-20260111T040000Z.mcpdbak",
				"mcpd-nas-20260118T040000Z.mcpdbak",
				"mcpd-nas-20260125T040000Z.mcpdbak",
				"mcpd-nas-20260201T040000Z.mcpdbak",
			},
		},
		{
			name: "keep-daily keeps the newest in each of the last two days",
			objects: []Object{
				obj("20260208T040000Z", time.Time{}),
				obj("20260208T010000Z", time.Time{}),
				obj("20260207T040000Z", time.Time{}),
				obj("20260207T010000Z", time.Time{}),
				obj("20260206T040000Z", time.Time{}),
			},
			policy: Policy{KeepLast: 1, KeepDaily: 2},
			removed: []string{
				"mcpd-nas-20260206T040000Z.mcpdbak",
				"mcpd-nas-20260207T010000Z.mcpdbak",
				"mcpd-nas-20260208T010000Z.mcpdbak",
			},
		},
		{
			name:    "the rules are OR-ed, not intersected",
			objects: weekly,
			// Keep-last two would drop everything before 1 February; keep-weekly
			// four rescues the next two weeks back.
			policy: Policy{KeepLast: 2, KeepWeekly: 4},
			removed: []string{
				"mcpd-nas-20260104T040000Z.mcpdbak",
				"mcpd-nas-20260111T040000Z.mcpdbak",
			},
		},
		{
			name: "a file that is not ours is invisible",
			objects: append([]Object{
				{Name: "somebody-elses-backup.tar.gz"},
				{Name: "mcpd-notes.txt"},
			}, weekly...),
			policy: Policy{KeepLast: 6},
			// Nothing removed, and nothing belonging to anybody else touched.
			removed: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Retain(tc.objects, tc.policy, uploaded, 0, time.UTC)
			if got.Held != "" {
				t.Fatalf("retention was held back: %s", got.Held)
			}
			if !equal(names(got.Remove), tc.removed) {
				t.Errorf("removed %v, want %v", names(got.Remove), tc.removed)
			}
		})
	}
}

// The newest archive is never deleted, whatever arithmetic says.
//
// A policy is a number somebody typed into a form, and no number should be able
// to mean "delete the only backup there is".
func TestRetainNeverDeletesTheNewest(t *testing.T) {
	only := []Object{obj("20260208T040000Z", time.Time{})}
	got := Retain(only, Policy{KeepLast: 0}, only[0].Name, 0, time.UTC)
	if len(got.Remove) != 0 {
		t.Errorf("removed %v from a destination holding one backup", names(got.Remove))
	}
	if got.Kept != 1 {
		t.Errorf("kept %d, want the one archive there is", got.Kept)
	}
}

// The timestamp comes from the name, never from the file.
//
// A NAS with a wrong clock, an S3 copy, and a restore from the destination's
// own snapshot all rewrite the modified time. Ordering by it would have
// retention delete whichever archives the destination happened to touch last.
func TestRetentionKeepsTheNewestWhenTheDestinationClockIsWrong(t *testing.T) {
	future := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	past := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	objects := []Object{
		// The oldest archive, stamped by the destination as the newest file.
		obj("20260104T040000Z", future),
		obj("20260201T040000Z", past),
		// The newest archive, stamped as the oldest file.
		obj("20260208T040000Z", past),
	}

	got := Retain(objects, Policy{KeepLast: 1}, "mcpd-nas-20260208T040000Z.mcpdbak", 0, time.UTC)
	if got.Held != "" {
		t.Fatalf("retention was held back: %s", got.Held)
	}
	want := []string{
		"mcpd-nas-20260104T040000Z.mcpdbak",
		"mcpd-nas-20260201T040000Z.mcpdbak",
	}
	if !equal(names(got.Remove), want) {
		t.Errorf("removed %v, want %v -- the modified times must not be read", names(got.Remove), want)
	}
}

// A listing that does not hold the archive just uploaded is an answer to
// distrust, not a licence to prune.
func TestRetentionDeletesNothingWhenTheUploadIsMissingFromTheListing(t *testing.T) {
	objects := []Object{
		obj("20260201T040000Z", time.Time{}),
		obj("20260125T040000Z", time.Time{}),
		obj("20260118T040000Z", time.Time{}),
	}
	got := Retain(objects, Policy{KeepLast: 1}, "mcpd-nas-20260208T040000Z.mcpdbak", 0, time.UTC)
	if len(got.Remove) != 0 {
		t.Errorf("removed %v after a listing that did not hold the new backup", names(got.Remove))
	}
	if got.Held == "" {
		t.Error("nothing was deleted and nothing said why")
	}
}

// An empty listing moments after an upload is a server answering wrongly.
func TestRetentionDeletesNothingWhenTheListingIsEmpty(t *testing.T) {
	got := Retain(nil, Policy{KeepLast: 1}, "mcpd-nas-20260208T040000Z.mcpdbak", 6, time.UTC)
	if len(got.Remove) != 0 {
		t.Errorf("removed %v from an empty listing", names(got.Remove))
	}
	if got.Held == "" {
		t.Error("nothing was deleted and nothing said why")
	}
}

// A destination that has lost most of its archives has a problem, and retention
// deleting more of them would compound it.
func TestRetentionDeletesNothingWhenFarFewerArchivesAreListedThanLastTime(t *testing.T) {
	objects := []Object{
		obj("20260208T040000Z", time.Time{}),
		obj("20260201T040000Z", time.Time{}),
	}
	got := Retain(objects, Policy{KeepLast: 1}, objects[0].Name, 12, time.UTC)
	if len(got.Remove) != 0 {
		t.Errorf("removed %v from a listing that had lost most of its archives", names(got.Remove))
	}
	if got.Held == "" {
		t.Error("nothing was deleted and nothing said why")
	}

	// The first few runs must not trip it: two archives where the last run saw
	// three is a destination filling up, not one losing files.
	got = Retain(objects, Policy{KeepLast: 1}, objects[0].Name, 3, time.UTC)
	if got.Held != "" {
		t.Errorf("held back on an ordinary early run: %s", got.Held)
	}
}

// Files somebody else put in a shared folder are never considered, either to
// keep or to delete.
func TestRetentionIgnoresFilesThatAreNotOurs(t *testing.T) {
	objects := []Object{
		obj("20260208T040000Z", time.Time{}),
		obj("20260201T040000Z", time.Time{}),
		{Name: "family-photos.zip"},
		{Name: "mcpd-20260208T040000Z.mcpdbak.part"},
		{Name: "mcpd-notes20260208T040000Z.mcpdbak"},
	}
	got := Retain(objects, Policy{KeepLast: 1}, objects[0].Name, 0, time.UTC)
	if got.Seen != 2 {
		t.Errorf("saw %d archives, want the 2 that are ours", got.Seen)
	}
	want := []string{"mcpd-nas-20260201T040000Z.mcpdbak"}
	if !equal(names(got.Remove), want) {
		t.Errorf("removed %v, want %v", names(got.Remove), want)
	}
}

// Two hosts backing up to one folder do not delete each other's archives.
//
// A destination is shared as easily with another mcpd as with a person -- one
// bucket, one prefix, two hosts -- and their names differ only by the slug in
// the middle. Pooling them makes each host's listing look twice as healthy as
// it is, and with keep-last one it has whichever host runs second delete the
// other's only backup.
func TestRetentionIgnoresAnotherInstancesArchives(t *testing.T) {
	// Newest first: the other host's are newer, which is what would make them
	// the ones kept if they were counted at all.
	objects := []Object{
		named("other-host", "20260208T050000Z", time.Time{}),
		named("other-host", "20260201T050000Z", time.Time{}),
		named("nas", "20260208T040000Z", time.Time{}),
		named("nas", "20260201T040000Z", time.Time{}),
		named("nas", "20260125T040000Z", time.Time{}),
	}
	uploaded := "mcpd-nas-20260208T040000Z.mcpdbak"

	got := Retain(objects, Policy{KeepLast: 1}, uploaded, 0, time.UTC)
	if got.Held != "" {
		t.Fatalf("retention was held back: %s", got.Held)
	}
	if got.Seen != 3 {
		t.Errorf("saw %d archives, want the 3 belonging to this host", got.Seen)
	}
	want := []string{
		"mcpd-nas-20260125T040000Z.mcpdbak",
		"mcpd-nas-20260201T040000Z.mcpdbak",
	}
	if !equal(names(got.Remove), want) {
		t.Fatalf("removed %v, want %v -- another host's archives are not ours to "+
			"keep or to delete", names(got.Remove), want)
	}

	// And from the other host's point of view, symmetrically.
	got = Retain(objects, Policy{KeepLast: 1},
		"mcpd-other-host-20260208T050000Z.mcpdbak", 0, time.UTC)
	if !equal(names(got.Remove), []string{"mcpd-other-host-20260201T050000Z.mcpdbak"}) {
		t.Errorf("the other host removed %v", names(got.Remove))
	}
}

// A host with no slug -- one nobody has given a public address -- owns only the
// names that have none either.
func TestRetentionScopesAHostWithNoSlugToTheNamesWithNoSlug(t *testing.T) {
	objects := []Object{
		{Name: "mcpd-20260208T040000Z.mcpdbak"},
		{Name: "mcpd-20260201T040000Z.mcpdbak"},
		named("someone-else", "20260208T040000Z", time.Time{}),
		named("someone-else", "20260201T040000Z", time.Time{}),
	}
	got := Retain(objects, Policy{KeepLast: 1}, "mcpd-20260208T040000Z.mcpdbak", 0, time.UTC)
	if got.Seen != 2 {
		t.Errorf("saw %d archives, want the 2 with no slug", got.Seen)
	}
	if !equal(names(got.Remove), []string{"mcpd-20260201T040000Z.mcpdbak"}) {
		t.Errorf("removed %v", names(got.Remove))
	}
}

// Every abort reports UnknownSeen, never the count it reached.
//
// The count a distrusted listing produced is a symptom of the listing, and
// recording it would make it the baseline the *next* run measures itself
// against -- so the check that just fired would never fire again, and the
// following truncated listing would delete real backups. This is the safety
// property the whole abort exists for, and it is one line away from being
// disarmed.
func TestRetentionReportsAnUnknownCountWheneverItHeldBack(t *testing.T) {
	cases := []struct {
		name       string
		objects    []Object
		uploaded   string
		seenBefore int
	}{
		{
			name:     "an empty listing",
			objects:  nil,
			uploaded: "mcpd-nas-20260208T040000Z.mcpdbak",
		},
		{
			name:     "a listing missing the upload",
			objects:  []Object{obj("20260201T040000Z", time.Time{})},
			uploaded: "mcpd-nas-20260208T040000Z.mcpdbak",
		},
		{
			name: "a listing that has lost most of its archives",
			objects: []Object{
				obj("20260208T040000Z", time.Time{}),
				obj("20260201T040000Z", time.Time{}),
			},
			uploaded:   "mcpd-nas-20260208T040000Z.mcpdbak",
			seenBefore: 12,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Retain(tc.objects, Policy{KeepLast: 1}, tc.uploaded, tc.seenBefore, time.UTC)
			if got.Held == "" {
				t.Fatal("retention ran on a listing it should have held back")
			}
			if got.Seen != UnknownSeen {
				t.Errorf("reported %d archives seen, want UnknownSeen -- writing a "+
					"count from a listing it distrusts is what disarms this check "+
					"for every run afterwards", got.Seen)
			}
		})
	}
}

// The calendar rules count in the schedule's timezone, because an operator who
// asked for a weekly backup on Sunday means their Sunday.
func TestRetentionCountsDaysInTheGivenLocation(t *testing.T) {
	chicago, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Skipf("no timezone database on this machine: %v", err)
	}
	// 02:00 and 23:00 UTC on the same UTC day are two different days in
	// Chicago: 20:00 the previous evening and 17:00.
	objects := []Object{
		obj("20260208T230000Z", time.Time{}),
		obj("20260208T020000Z", time.Time{}),
	}
	got := Retain(objects, Policy{KeepLast: 1, KeepDaily: 2}, objects[0].Name, 0, chicago)
	if len(got.Remove) != 0 {
		t.Errorf("removed %v; both archives fall on different days in Chicago", names(got.Remove))
	}

	// The same two, counted in UTC, are one day and the older one goes.
	got = Retain(objects, Policy{KeepLast: 1, KeepDaily: 2}, objects[0].Name, 0, time.UTC)
	if len(got.Remove) != 1 {
		t.Errorf("removed %v; in UTC the two fall on one day", names(got.Remove))
	}
}
