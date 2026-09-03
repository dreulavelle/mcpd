package supportinfo

import (
	"testing"
	"time"
)

/*
The trap this whole file exists for.

3CX files "a backup completed successfully" as an Error, and every provisioning
file it has ever served as one too. A tool that believed the severity column
would open on a wall of red, nearly all of it routine — and a technician would
learn inside a day to ignore the screen, which is worse than never building it.
*/
func TestRoutineEventsAreNotErrors(t *testing.T) {
	totals := newEventTotals()
	at := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)

	totals.readEvent("Web Manager", "10031", "Error",
		"A backup of your PBX has been successfully completed.", at)
	totals.readEvent("Web Manager", "10029", "Error",
		"Provisioning file for MAC 249AD8935E6F of user 101 was successfully generated", at)

	for _, g := range totals.result().Groups {
		if g.Severity != Note {
			t.Errorf("event %s reported as %s; 3CX calls its own successes errors and we should not",
				g.ID, g.Severity)
		}
	}
	if findings := totals.findings(); len(findings) != 0 {
		t.Errorf("routine events produced %d findings, want none", len(findings))
	}
}

// One trunk drop at three in the morning is routine. Six thousand is why the
// phones do not work, and the difference has to be the count.
func TestFrequencyDecidesSeverity(t *testing.T) {
	at := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)

	once := newEventTotals()
	once.readEvent("SBC / Tunnel", "4102", "Error", "Trunk SBC 'DellSBC' has changed status to Down", at)
	if got := once.result().Groups[0].Severity; got != Note {
		t.Errorf("a single trunk drop is %s, want a note", got)
	}

	often := newEventTotals()
	for range 40 {
		often.readEvent("SBC / Tunnel", "4102", "Error", "Trunk SBC 'DellSBC' has changed status to Down", at)
	}
	if got := often.result().Groups[0].Severity; got != Critical {
		t.Errorf("a flapping trunk is %s, want critical", got)
	}

	findings := often.findings()
	if len(findings) != 1 {
		t.Fatalf("got %d findings, want one", len(findings))
	}
	// The trunk has to be named, or the finding is not actionable.
	if want := "DellSBC"; !contains(findings[0].Detail, want) {
		t.Errorf("finding does not name the trunk that flapped:\n%s", findings[0].Detail)
	}
}

// An event nobody catalogued should be visible without being alarming, because
// we do not know what it means.
func TestUnknownEventsAreDampened(t *testing.T) {
	totals := newEventTotals()
	at := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)
	totals.readEvent("SIP Server", "50031", "Error", "Something nobody has documented", at)

	if got := totals.result().Groups[0].Severity; got != Warning {
		t.Errorf("unknown error reported as %s, want warning", got)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
