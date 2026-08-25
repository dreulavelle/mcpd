package observium

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/plugins"
)

// toolPlugin builds a plugin around a fake Observium.
//
// The struct is built directly rather than through New, which the fake
// server's http address is fine for but which would also rebuild the client.
// What is under test here is the tools.
func toolPlugin(t *testing.T, h http.HandlerFunc) *Plugin {
	t.Helper()
	c, _ := testClient(t, h)
	return &Plugin{
		deps: plugins.Deps{
			Instance: "observium",
			Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
			Now:      time.Now,
		},
		cfg:        c.cfg,
		client:     c,
		reader:     c,
		configured: true,
	}
}

// jsonAPI answers every request with one collection.
func jsonAPI(key string, items map[string]any, count int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok", "count": count, key: items,
		})
	}
}

// "What is wrong" is the question the alerts tool exists for, and an
// unfiltered listing on a healthy estate is thousands of rows saying nothing
// is wrong. Defaulting to failed is what makes the tool answer its question.
func TestListAlerts_DefaultsToFailed(t *testing.T) {
	var gotStatus string
	p := toolPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		gotStatus = r.URL.Query().Get("status")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"ok","count":0,"alerts":[]}`)
	})

	got, err := p.listAlerts(context.Background(), alertsArgs{})
	if err != nil {
		t.Fatalf("listAlerts: %v", err)
	}
	if gotStatus != "failed" {
		t.Errorf("status filter = %q, want %q", gotStatus, "failed")
	}
	// An empty result here means something specific, and saying so stops a
	// model reporting "no data" when the answer is "nothing is broken".
	if !strings.Contains(got.Note, "failed state") {
		t.Errorf("an empty alert list should say nothing is failing: %q", got.Note)
	}
}

// A caller may narrow what comes back but never widen it past what the
// operator configured. max_items is a bound on what one answer may pull into a
// conversation, and an argument that could raise it would not be a bound.
func TestFetch_PerCallLimitCannotExceedTheConfiguredCeiling(t *testing.T) {
	items := map[string]any{}
	for i := 0; i < 40; i++ {
		items[fmt.Sprint(i)] = map[string]any{"device_id": i}
	}
	p := toolPlugin(t, jsonAPI("devices", items, 40))
	p.cfg.MaxItems = 5
	p.client.cfg.MaxItems = 5

	got, err := p.listDevices(context.Background(), devicesArgs{Limit: 1000})
	if err != nil {
		t.Fatalf("listDevices: %v", err)
	}
	if got.Count > 5 {
		t.Fatalf("a limit argument raised the ceiling to %d; max_items is 5", got.Count)
	}
	if !got.Truncated {
		t.Error("a truncated answer must say so")
	}
	if !strings.Contains(got.Note, "not all of them") {
		t.Errorf("the note should say this is not the whole estate: %q", got.Note)
	}
}

// A narrower per-call limit is honoured, and must not leak into the shared
// client where a concurrent call would see it.
func TestFetch_NarrowerLimitDoesNotMutateTheSharedClient(t *testing.T) {
	items := map[string]any{}
	for i := 0; i < 40; i++ {
		items[fmt.Sprint(i)] = map[string]any{"device_id": i}
	}
	p := toolPlugin(t, jsonAPI("devices", items, 40))

	before := p.client.cfg.MaxItems
	got, err := p.listDevices(context.Background(), devicesArgs{Limit: 3})
	if err != nil {
		t.Fatalf("listDevices: %v", err)
	}
	if got.Count != 3 {
		t.Fatalf("count = %d, want the requested 3", got.Count)
	}
	if p.client.cfg.MaxItems != before {
		t.Fatalf("the shared client's ceiling changed to %d; a per-call limit "+
			"must not be visible to a concurrent call", p.client.cfg.MaxItems)
	}
}

// An empty argument must not become a filter matching the empty string, which
// upstream would answer with nothing at all.
func TestListDevices_EmptyArgumentsAreNotFilters(t *testing.T) {
	var gotQuery string
	p := toolPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"ok","count":0,"devices":[]}`)
	})

	if _, err := p.listDevices(context.Background(), devicesArgs{Status: "up"}); err != nil {
		t.Fatalf("listDevices: %v", err)
	}
	for _, absent := range []string{"os=", "hostname=", "location=", "vendor="} {
		if strings.Contains(gotQuery, absent) {
			t.Errorf("query %q carries an empty filter %q", gotQuery, absent)
		}
	}
	if !strings.Contains(gotQuery, "status=up") {
		t.Errorf("query %q lost the filter that was given", gotQuery)
	}
}

// Naming neither a device_id nor a hostname is a caller mistake worth catching
// here: without one the path would be /devices/, which is the whole estate.
func TestGetDevice_RequiresAnIdentifier(t *testing.T) {
	p := toolPlugin(t, jsonAPI("devices", map[string]any{}, 0))
	if _, err := p.getDevice(context.Background(), deviceArgs{}); err == nil {
		t.Fatal("a device lookup with no identifier must be refused")
	}
}

// Observium reports an entity outside the account's permissions as absent
// rather than forbidden, so an empty answer here has two causes and the error
// has to name both.
func TestGetDevice_EmptyAnswerNamesBothCauses(t *testing.T) {
	p := toolPlugin(t, jsonAPI("devices", map[string]any{}, 0))
	_, err := p.getDevice(context.Background(), deviceArgs{DeviceID: 9})
	if err == nil {
		t.Fatal("a device that is not there should be an error, not an empty list")
	}
	if !strings.Contains(err.Error(), "cannot read it") {
		t.Errorf("the error should say permissions are indistinguishable "+
			"from absence: %v", err)
	}
}

// VLANs need a level 7 account. A topology answer without them is still the
// answer to most of the question, so a permission failure degrades with a note
// rather than discarding the neighbours already fetched.
func TestTopology_VLANFailureDoesNotLoseTheRest(t *testing.T) {
	p := toolPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/vlans") {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"status":"failed","message":"insufficient level"}`)
			return
		}
		fmt.Fprint(w, `{"status":"ok","count":1,"neighbours":{"1":{"neighbour_id":"1"}},`+
			`"addresses":{"1":{"address_id":"1"}}}`)
	})

	got, err := p.topology(context.Background(), topologyArgs{DeviceID: 1, VLANs: true})
	if err != nil {
		t.Fatalf("a VLAN permission failure must not fail the whole call: %v", err)
	}
	if got.Neighbours.Count != 1 {
		t.Errorf("neighbours were lost: %+v", got.Neighbours)
	}
	if !strings.Contains(got.VLANs.Note, "could not be read") {
		t.Errorf("the VLAN failure should be reported in place: %q", got.VLANs.Note)
	}
}

// An unconfigured plugin must refuse in a way that says what to do, rather
// than making a request to an empty address.
func TestFetch_UnconfiguredSaysWhatIsMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("an unconfigured plugin made a request")
	}))
	defer srv.Close()

	p := toolPlugin(t, func(http.ResponseWriter, *http.Request) {})
	p.configured = false

	_, err := p.listDevices(context.Background(), devicesArgs{})
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("want a not-configured error, got %v", err)
	}
}
