package extremecloudiq

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

// Every windowed endpoint here takes milliseconds. Seconds is the unit
// everybody reaches for first, and the failure mode is not an error: a window
// a thousand times too narrow returns nothing, from a moment in 1970, which
// reads exactly like an estate with no alerts.
func TestWindow_IsSentInMilliseconds(t *testing.T) {
	w := window{
		From: time.Date(2026, 8, 27, 11, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC),
	}
	q := url.Values{}
	w.apply(q)

	if got := q.Get("startTime"); got != "1787828400000" {
		t.Errorf("startTime = %s; it should be epoch milliseconds", got)
	}
	if got := q.Get("endTime"); got != "1787832000000" {
		t.Errorf("endTime = %s; it should be epoch milliseconds", got)
	}
}

// A caller who names no window gets the configured default rather than a zero,
// because a zero is a 400 on every endpoint this reaches.
func TestResolve_DefaultsRatherThanSendingNothing(t *testing.T) {
	cfg := testConfig("https://api.invalid")
	got, err := cfg.resolve(timeArgs{}, fixedNow)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if want := time.Duration(cfg.DefaultRangeSeconds) * time.Second; got.span() != want {
		t.Errorf("span = %s, want the configured default %s", got.span(), want)
	}
	if !got.To.Equal(fixedNow) {
		t.Errorf("the window does not end now: %s", got.To)
	}
}

// Two ways of naming one window is refused rather than resolved by
// precedence. The whole point of a window is that somebody knows which one
// they are looking at, and quietly picking is how an answer comes to describe
// a different day than the question did.
func TestResolve_RefusesTwoWaysOfSayingIt(t *testing.T) {
	cfg := testConfig("https://api.invalid")
	_, err := cfg.resolve(timeArgs{
		RangeSeconds: 60,
		From:         "2026-08-01T00:00:00Z",
		To:           "2026-08-02T00:00:00Z",
	}, fixedNow)
	if err == nil {
		t.Fatal("both a relative and an exact window were accepted")
	}
	if !strings.Contains(err.Error(), "one way") {
		t.Errorf("the message does not say to pick one: %v", err)
	}
}

// Half an exact window is not a smaller ask, it is an ambiguous one: "from
// Tuesday" could mean until now or until the end of Tuesday.
func TestResolve_RefusesHalfAnExactWindow(t *testing.T) {
	cfg := testConfig("https://api.invalid")
	if _, err := cfg.resolve(timeArgs{From: "2026-08-01T00:00:00Z"}, fixedNow); err == nil {
		t.Error("a window with no end was accepted")
	}
	if _, err := cfg.resolve(timeArgs{To: "2026-08-01T00:00:00Z"}, fixedNow); err == nil {
		t.Error("a window with no start was accepted")
	}
}

// The ceiling is on how far back a read reaches rather than on how wide it is.
// An operator who caps this at seven days means "not older than seven days",
// so a narrow window a year ago is exactly what they meant to refuse.
func TestResolve_CeilingIsAboutAgeNotWidth(t *testing.T) {
	cfg := testConfig("https://api.invalid")
	cfg.MaxRangeSeconds = 7 * 24 * 3600

	_, err := cfg.resolve(timeArgs{
		From: "2025-08-01T00:00:00Z", To: "2025-08-01T01:00:00Z",
	}, fixedNow)
	if err == nil {
		t.Fatal("an hour-wide window a year ago was accepted past a seven-day ceiling")
	}
	if !strings.Contains(err.Error(), "Narrow the window") {
		t.Errorf("the message does not say what to do: %v", err)
	}
}

// The audit endpoint takes at most 30 days. Refused here so the message names
// the limit, rather than arriving as a 400 about a parameter and a number of
// milliseconds.
func TestAuditLimit_RefusesAWiderWindowWithTheReason(t *testing.T) {
	wide := window{From: fixedNow.Add(-60 * 24 * time.Hour), To: fixedNow}
	err := withinAuditLimit(wide)
	if err == nil {
		t.Fatal("a 60-day audit window was accepted")
	}
	if !strings.Contains(err.Error(), "30 days") {
		t.Errorf("the message does not name the limit: %v", err)
	}
	if err := withinAuditLimit(window{From: fixedNow.Add(-24 * time.Hour), To: fixedNow}); err != nil {
		t.Errorf("a one-day audit window was refused: %v", err)
	}
}

// The spellings a model actually produces, rather than only the one the schema
// asks for. Refusing a missing colon would cost a round trip to be told about
// a colon.
func TestParseInstant_AcceptsWhatAModelWrites(t *testing.T) {
	for _, in := range []string{
		"2026-08-27T12:00:00Z",
		"2026-08-27T12:00:00",
		"2026-08-27 12:00:00",
		"2026-08-27",
	} {
		if _, err := parseInstant(in, "from"); err != nil {
			t.Errorf("parseInstant(%q): %v", in, err)
		}
	}
	if _, err := parseInstant("last tuesday", "from"); err == nil {
		t.Error("a phrase was accepted; this API has no parser for one")
	}
}
