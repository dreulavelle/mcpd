package updates

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func checker(t *testing.T, current string, cfg Config, h http.HandlerFunc) (*Checker, *int) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	calls := 0
	client := &http.Client{Transport: rewrite{base: srv.URL, count: &calls}}
	return New(current, func() Config { return cfg }, client, func() time.Time {
		return time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	}), &calls
}

// rewrite sends the checker's api.github.com request to the test server
// instead, so the URL it builds is exercised rather than bypassed.
type rewrite struct {
	base  string
	count *int
}

func (r rewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	*r.count++
	out := req.Clone(req.Context())
	out.URL.Scheme = "http"
	out.URL.Host = req.URL.Host
	if u, err := http.NewRequest(req.Method, r.base+req.URL.Path+"?"+req.URL.RawQuery, nil); err == nil {
		out.URL = u.URL
	}
	return http.DefaultTransport.RoundTrip(out)
}

const twoReleases = `[
  {"tag_name":"v0.5.0","name":"0.5.0","html_url":"https://x/5","body":"newest","published_at":"2026-08-20T00:00:00Z"},
  {"tag_name":"v0.4.0","name":"0.4.0","html_url":"https://x/4","body":"middle","published_at":"2026-08-10T00:00:00Z"},
  {"tag_name":"v0.3.0","name":"0.3.0","html_url":"https://x/3","body":"running","published_at":"2026-08-01T00:00:00Z"}
]`

// Every release above the running one is reported, not just the newest: an
// operator deciding whether to upgrade wants what the upgrade brings.
func TestStatus_ReportsEveryReleaseAhead(t *testing.T) {
	c, _ := checker(t, "v0.3.0", Config{Enabled: true}, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, twoReleases)
	})
	got := c.Status(context.Background(), false)

	if !got.UpdateAvailable {
		t.Fatal("an update was available and was not reported")
	}
	if got.Latest != "0.5.0" {
		t.Errorf("latest = %q, want 0.5.0", got.Latest)
	}
	if len(got.Newer) != 2 {
		t.Fatalf("got %d newer releases, want 2", len(got.Newer))
	}
	if got.Newer[0].Version != "0.5.0" {
		t.Errorf("newest first: got %q", got.Newer[0].Version)
	}
	if got.Newer[0].Notes != "newest" {
		t.Errorf("release notes were dropped: %+v", got.Newer[0])
	}
}

// A build that does not name a version cannot be behind one. Reporting "dev"
// as out of date would put an update banner on every development start.
func TestStatus_DevBuildIsNotReportedAsBehind(t *testing.T) {
	c, _ := checker(t, "dev", Config{Enabled: true}, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, twoReleases)
	})
	got := c.Status(context.Background(), false)

	if got.Comparable {
		t.Error("dev was treated as a comparable version")
	}
	if got.UpdateAvailable {
		t.Error("a dev build was reported as having an update available")
	}
	if got.Latest != "0.5.0" {
		t.Errorf("the newest release should still be reported: %q", got.Latest)
	}
}

// Switched off means nothing is asked, and nothing is claimed.
func TestStatus_DisabledMakesNoRequest(t *testing.T) {
	c, calls := checker(t, "v0.3.0", Config{Enabled: false}, func(w http.ResponseWriter, r *http.Request) {
		t.Error("a disabled checker made a request")
	})
	got := c.Status(context.Background(), false)

	if *calls != 0 {
		t.Errorf("made %d requests while disabled", *calls)
	}
	if got.Enabled || got.UpdateAvailable {
		t.Errorf("a disabled check claimed something: %+v", got)
	}
}

// The answer is cached: several dashboard tabs must not each hit GitHub.
func TestStatus_CachesUntilTheIntervalPasses(t *testing.T) {
	c, calls := checker(t, "v0.3.0", Config{Enabled: true, Interval: time.Hour},
		func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, twoReleases) })

	c.Status(context.Background(), false)
	c.Status(context.Background(), false)
	c.Status(context.Background(), false)
	if *calls != 1 {
		t.Errorf("made %d requests, want 1 -- the rest should be cached", *calls)
	}

	c.Status(context.Background(), true)
	if *calls != 2 {
		t.Errorf("forcing a check made %d requests in total, want 2", *calls)
	}
}

// A failed check must not erase a good answer, and must say it failed.
func TestStatus_FailureKeepsThePreviousAnswer(t *testing.T) {
	fail := false
	c, _ := checker(t, "v0.3.0", Config{Enabled: true}, func(w http.ResponseWriter, r *http.Request) {
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, twoReleases)
	})

	first := c.Status(context.Background(), true)
	if first.Latest != "0.5.0" {
		t.Fatalf("setup: latest = %q", first.Latest)
	}

	fail = true
	second := c.Status(context.Background(), true)
	if second.Latest != "0.5.0" {
		t.Errorf("a failed check discarded the previous answer: %+v", second)
	}
	if second.Error == "" {
		t.Error("a failed check reported no error, so it looks up to date")
	}
}

// Drafts and prereleases are not what a deployment should be told to run.
func TestStatus_SkipsDraftsAndPrereleases(t *testing.T) {
	c, _ := checker(t, "v0.3.0", Config{Enabled: true}, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[
		  {"tag_name":"v0.9.0","draft":true,"published_at":"2026-08-22T00:00:00Z"},
		  {"tag_name":"v0.8.0","prerelease":true,"published_at":"2026-08-21T00:00:00Z"},
		  {"tag_name":"v0.4.0","published_at":"2026-08-10T00:00:00Z"}
		]`)
	})
	got := c.Status(context.Background(), false)

	if got.Latest != "0.4.0" {
		t.Errorf("latest = %q, want 0.4.0 -- a draft or prerelease was offered", got.Latest)
	}
}

func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"v1.2.3", true}, {"1.2.3", true}, {"v0.1", true}, {"v2", true},
		{"v1.2.3-rc1", true}, {"v1.2.3-dirty", true}, {"v1.2.3+meta", true},
		{"dev", false}, {"", false}, {"v1.2.3.4", false}, {"vx.y.z", false},
	}
	for _, tc := range cases {
		if got := parse(tc.in) != nil; got != tc.want {
			t.Errorf("parse(%q) parsed=%v, want %v", tc.in, got, tc.want)
		}
	}
	if compare(parse("v1.2.3"), parse("v1.2.10")) >= 0 {
		t.Error("1.2.3 should sort below 1.2.10")
	}
	if compare(parse("v2.0.0"), parse("v1.99.99")) <= 0 {
		t.Error("2.0.0 should sort above 1.99.99")
	}
}
