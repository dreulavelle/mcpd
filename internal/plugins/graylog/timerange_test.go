package graylog

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)

// Graylog treats a relative range of zero as every message it holds. So "the
// caller named no window" must never reach the API as a zero -- it becomes the
// configured default here, and this is the test that says so.
func TestResolve_NeverSendsAnUnboundedRange(t *testing.T) {
	cfg := testConfig("https://graylog.example")

	got, err := cfg.resolve(timeArgs{}, testNow)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Type != "relative" || got.Range != cfg.DefaultRangeSeconds {
		t.Fatalf("resolve = %+v, want the configured default window", got)
	}
	// And the zero must not vanish in encoding either: `omitempty` on Range
	// would drop it, and a body with no range is the same unbounded scan by
	// another route.
	raw, _ := json.Marshal(got)
	if !strings.Contains(string(raw), `"range":`) {
		t.Errorf("the range did not survive encoding: %s", raw)
	}
}

// A caller who supplies more than one kind of window is refused rather than
// silently having one ignored. The whole point of a time range is that
// somebody knows which window they are looking at.
func TestResolve_RefusesTwoWindowsAtOnce(t *testing.T) {
	cfg := testConfig("https://graylog.example")

	for _, in := range []timeArgs{
		{RangeSeconds: 60, Keyword: "yesterday"},
		{RangeSeconds: 60, From: "2026-08-01T00:00:00Z", To: "2026-08-02T00:00:00Z"},
		{Keyword: "yesterday", From: "2026-08-01T00:00:00Z", To: "2026-08-02T00:00:00Z"},
	} {
		if _, err := cfg.resolve(in, testNow); err == nil {
			t.Errorf("%+v was accepted; two windows means searching one of them silently", in)
		}
	}
}

// A half-open window is not a smaller ask, it is an ambiguous one. "From
// Tuesday" could mean until now or until the end of Tuesday, and the two
// differ by days of indices on a busy cluster.
func TestResolve_AbsoluteNeedsBothEnds(t *testing.T) {
	cfg := testConfig("https://graylog.example")

	for _, in := range []timeArgs{
		{From: "2026-08-01T00:00:00Z"},
		{To: "2026-08-01T00:00:00Z"},
	} {
		_, err := cfg.resolve(in, testNow)
		if err == nil {
			t.Fatalf("%+v was accepted", in)
		}
		if !strings.Contains(err.Error(), "both from and to") {
			t.Errorf("the message should say what is missing, got: %v", err)
		}
	}
}

func TestResolve_AbsoluteRefusesAnEmptyWindow(t *testing.T) {
	cfg := testConfig("https://graylog.example")
	_, err := cfg.resolve(timeArgs{
		From: "2026-08-02T00:00:00Z",
		To:   "2026-08-01T00:00:00Z",
	}, testNow)
	if err == nil {
		t.Fatal("a backwards window was accepted")
	}
}

// A timestamp with no zone is read as UTC. Guessing the host's local zone
// would make the same call mean different hours on different machines.
func TestResolve_UndatedTimestampsAreUTC(t *testing.T) {
	cfg := testConfig("https://graylog.example")
	got, err := cfg.resolve(timeArgs{
		From: "2026-08-26 09:00:00",
		To:   "2026-08-26 10:00:00",
	}, testNow)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !strings.HasPrefix(got.From, "2026-08-26T09:00:00") || !strings.HasSuffix(got.From, "Z") {
		t.Errorf("from = %q, want the same wall clock in UTC", got.From)
	}
}

// The ceiling is on how far back a search reaches rather than on how wide it
// is: an operator who caps searches at seven days means "not older than seven
// days", and a narrow window a year ago is exactly what they meant to refuse.
func TestResolve_CeilingIsAboutAgeNotWidth(t *testing.T) {
	cfg := testConfig("https://graylog.example")
	cfg.MaxRangeSeconds = 7 * 24 * 3600

	// A one-minute window, a year ago.
	_, err := cfg.resolve(timeArgs{
		From: "2025-08-26T09:00:00Z",
		To:   "2025-08-26T09:01:00Z",
	}, testNow)
	if err == nil {
		t.Fatal("a narrow window outside the ceiling was accepted")
	}

	// The same width, inside it.
	if _, err := cfg.resolve(timeArgs{
		From: "2026-08-26T09:00:00Z",
		To:   "2026-08-26T09:01:00Z",
	}, testNow); err != nil {
		t.Fatalf("a window inside the ceiling was refused: %v", err)
	}
}

// Graylog resolves a keyword window on its side, so this code cannot measure
// it and therefore cannot hold it to a limit. A ceiling with one way around it
// is not a ceiling, so the keyword is refused while one is set -- and the
// refusal says what to use instead.
func TestResolve_KeywordIsRefusedUnderACeiling(t *testing.T) {
	cfg := testConfig("https://graylog.example")

	if _, err := cfg.resolve(timeArgs{Keyword: "last 2 hours"}, testNow); err != nil {
		t.Fatalf("a keyword window with no ceiling should work: %v", err)
	}

	cfg.MaxRangeSeconds = 3600
	_, err := cfg.resolve(timeArgs{Keyword: "last year"}, testNow)
	if err == nil {
		t.Fatal("a keyword window slipped past the ceiling")
	}
	if !strings.Contains(err.Error(), "range_seconds") {
		t.Errorf("the refusal should say what to use instead, got: %v", err)
	}
}

func TestResolve_RelativeRespectsTheCeiling(t *testing.T) {
	cfg := testConfig("https://graylog.example")
	cfg.MaxRangeSeconds = 3600

	if _, err := cfg.resolve(timeArgs{RangeSeconds: 7200}, testNow); err == nil {
		t.Fatal("a range past the ceiling was accepted")
	}
	if _, err := cfg.resolve(timeArgs{RangeSeconds: 600}, testNow); err != nil {
		t.Fatalf("a range inside the ceiling was refused: %v", err)
	}
}

// A ceiling below the default would refuse every search nobody narrowed --
// a configuration that looks fine and answers nothing.
func TestValidate_RefusesACeilingBelowTheDefault(t *testing.T) {
	cfg := testConfig("https://graylog.example")
	cfg.DefaultRangeSeconds = 900
	cfg.MaxRangeSeconds = 60

	err := cfg.Validate()
	if err == nil {
		t.Fatal("a ceiling below the default window was accepted")
	}
	if !strings.Contains(err.Error(), "would be refused") {
		t.Errorf("the message should say what breaks, got: %v", err)
	}
}
