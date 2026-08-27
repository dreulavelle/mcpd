package extremecloudiq

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// The diagnostics grids are POSTs, and the whole risk of admitting POST to a
// read-only integration is that the guard stops meaning anything. It does not:
// the four grids are named, and POST to anything else is refused exactly as
// before.
func TestGuard_AdmitsTheNamedGridsAndNoOtherPost(t *testing.T) {
	for _, path := range []string{
		"/dashboard/wireless/device-health/grid",
		"/dashboard/wired/device-health/grid",
		"/dashboard/wireless/client-health/grid",
		"/dashboard/wired/client-health/grid",
		"/dashboard/wireless/usage-capacity/grid",
		"/dashboard/wired/usage-capacity/grid",
		"/dashboard/sites-with-issues",
	} {
		if err := roundTrip(t, http.MethodPost, path); err != nil {
			t.Errorf("POST %s was refused by this plugin's own guard: %v", path, err)
		}
	}

	// Everything else that a widened "GET or POST" would have let through.
	// Each of these changes somebody's estate.
	for _, path := range []string{
		"/devices/:reboot",
		"/devices/:delete",
		"/devices/:cli",
		"/logout",
		"/auth/apitoken",
		"/network-policies",
		"/dashboard/export",
		"/dashboard/wireless/client-health/export",
	} {
		if err := roundTrip(t, http.MethodPost, path); err == nil {
			t.Errorf("POST %s was permitted", path)
		}
	}
}

// The four grids answer four different questions and none of the row shapes
// overlap, so sending the wrong one is a wrong answer rather than an error. A
// test of the routing is a test of the answers.
func TestListDeviceIssues_AsksTheRightGrid(t *testing.T) {
	for _, tc := range []struct{ kind, concern, want string }{
		{"", "", "/dashboard/wireless/device-health/grid"},
		{"wireless", "health", "/dashboard/wireless/device-health/grid"},
		{"wireless", "capacity", "/dashboard/wireless/usage-capacity/grid"},
		{"wired", "health", "/dashboard/wired/device-health/grid"},
		{"wired", "capacity", "/dashboard/wired/usage-capacity/grid"},
	} {
		t.Run(tc.kind+"/"+tc.concern, func(t *testing.T) {
			var asked, method string
			p := toolPlugin(t, func(w http.ResponseWriter, r *http.Request) {
				asked, method = r.URL.Path, r.Method
				_, _ = io.WriteString(w, page(0, ``))
			})
			if _, err := p.listDeviceIssues(context.Background(),
				DeviceIssuesInput{Kind: tc.kind, Concern: tc.concern}); err != nil {
				t.Fatalf("listDeviceIssues: %v", err)
			}
			if asked != tc.want {
				t.Errorf("asked %s, want %s", asked, tc.want)
			}
			if method != http.MethodPost {
				t.Errorf("method was %s; these grids are POSTs", method)
			}
		})
	}
}

// The row fields differ completely between the four, so a result that did not
// say which one it came from would be a page of numbers a model has to guess
// the meaning of.
func TestListDeviceIssues_SaysWhichQuestionItAnswered(t *testing.T) {
	p := toolPlugin(t, jsonOK(page(0, ``)))
	out, err := p.listDeviceIssues(context.Background(),
		DeviceIssuesInput{Kind: "wired", Concern: "health"})
	if err != nil {
		t.Fatalf("listDeviceIssues: %v", err)
	}
	if out.Kind != "wired" || out.Concern != "health" {
		t.Errorf("kind=%q concern=%q were not echoed", out.Kind, out.Concern)
	}
	if !strings.Contains(out.Note, "poe_error_slots") {
		t.Errorf("the note does not say what to read in the rows: %q", out.Note)
	}
}

// A word outside the vocabulary is refused rather than silently defaulted: a
// caller who asked about switches and got access points would report the
// absence of a switch fault as a fact.
func TestListDeviceIssues_RefusesAWordItDoesNotKnow(t *testing.T) {
	p := toolPlugin(t, jsonOK(page(0, ``)))
	for _, in := range []DeviceIssuesInput{{Kind: "switches"}, {Concern: "everything"}} {
		_, err := p.listDeviceIssues(context.Background(), in)
		if err == nil {
			t.Fatalf("%+v was accepted", in)
		}
		if !strings.Contains(err.Error(), "it is one of") {
			t.Errorf("the refusal does not offer the vocabulary: %v", err)
		}
	}
}

// The site and device filters travel in the body, not the query string, which
// is the entire reason these are POSTs. A filter that silently did not reach
// the API would return the whole estate and look like a working call.
func TestListClientIssues_SendsTheFilterInTheBody(t *testing.T) {
	var body gridFilter
	var asked string
	p := toolPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/locations/tree" {
			_, _ = io.WriteString(w, locationTree)
			return
		}
		asked = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, page(0, ``))
	})

	out, err := p.listClientIssues(context.Background(),
		ClientIssuesInput{Connection: "wired", Site: "Springfield"})
	if err != nil {
		t.Fatalf("listClientIssues: %v", err)
	}
	if asked != "/dashboard/wired/client-health/grid" {
		t.Errorf("asked %s", asked)
	}
	if len(body.SiteIDs) != 1 || body.SiteIDs[0] != 1 {
		t.Errorf("site_ids = %v, want the resolved id 1 in the body", body.SiteIDs)
	}
	// The wired note is the only place this API will say what a client is
	// plugged into, so it has to point at those fields.
	if !strings.Contains(out.Note, "switch_name") || !strings.Contains(out.Note, "port_number") {
		t.Errorf("the wired note does not name the port fields: %q", out.Note)
	}
}

// The five scores are per location, so they are worth five requests for one
// site and would be five hundred for a hundred. The estate-wide listing must
// not fetch them.
func TestListSitesWithIssues_ScoresOnlyWhenOneSiteIsNamed(t *testing.T) {
	var scoreCalls int
	handler := func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/locations/tree":
			_, _ = io.WriteString(w, locationTree)
		case strings.HasPrefix(r.URL.Path, "/network-scorecard/"):
			scoreCalls++
			_, _ = io.WriteString(w, `{"overall_score":81}`)
		default:
			_, _ = io.WriteString(w, page(1, `{"site_name":"Springfield"}`))
		}
	}

	wide := toolPlugin(t, handler)
	out, err := wide.listSitesWithIssues(context.Background(), SitesInput{})
	if err != nil {
		t.Fatalf("listSitesWithIssues: %v", err)
	}
	if scoreCalls != 0 {
		t.Errorf("an estate-wide listing made %d scorecard calls", scoreCalls)
	}
	if len(out.Scores) != 0 {
		t.Errorf("scores were returned without a site being named: %v", out.Scores)
	}

	scoreCalls = 0
	one := toolPlugin(t, handler)
	out, err = one.listSitesWithIssues(context.Background(), SitesInput{Site: "Springfield"})
	if err != nil {
		t.Fatalf("listSitesWithIssues: %v", err)
	}
	if scoreCalls != len(scorecards) {
		t.Errorf("made %d scorecard calls for one site, want %d", scoreCalls, len(scorecards))
	}
	for _, card := range scorecards {
		if _, ok := out.Scores[card.name]; !ok {
			t.Errorf("the %s score is missing", card.name)
		}
	}
}

// A grid that is never cached, whatever the cache is set to. These answer
// "what is wrong right now", which is the class of question a held answer is
// indistinguishable from a true one for.
func TestDiagnostics_AreNeverCached(t *testing.T) {
	var calls int
	p := cachingPlugin(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = io.WriteString(w, page(0, ``))
	})
	for range 3 {
		if _, err := p.listDeviceIssues(context.Background(), DeviceIssuesInput{}); err != nil {
			t.Fatalf("listDeviceIssues: %v", err)
		}
	}
	if calls != 3 {
		t.Errorf("the device-health grid was fetched %d times over three calls; "+
			"whether a device is coping must never be answered from memory", calls)
	}
}
