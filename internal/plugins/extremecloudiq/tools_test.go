package extremecloudiq

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// The location tree every test that names a site reads.
const locationTree = `[{"id":1,"name":"Springfield","type":"SITE","unique_name":"Springfield",
  "children":[{"id":2,"name":"Main","type":"BUILDING","unique_name":"Springfield/Main",
    "parent_id":1,"children":[{"id":3,"name":"1","type":"FLOOR",
      "unique_name":"Springfield/Main/1","parent_id":2}]}]},
 {"id":4,"name":"Northgate","type":"SITE","unique_name":"Northgate",
  "children":[{"id":5,"name":"1","type":"FLOOR","unique_name":"Northgate/1","parent_id":4}]}]`

// Every location filter in this API is a numeric id, and a model has a name.
// Resolving it here is what turns "devices at Springfield" into something that
// works -- an id guessed wrong is a filter that silently matches nothing
// rather than an error, which is the worst way for this to fail.
func TestListDevices_ResolvesASiteNameToItsID(t *testing.T) {
	var sent string
	p := toolPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/locations/tree":
			_, _ = io.WriteString(w, locationTree)
		case "/devices":
			sent = r.URL.Query().Get("locationIds")
			_, _ = io.WriteString(w, page(1, `{"id":9,"hostname":"ap-1","connected":true}`))
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
	})

	out, err := p.listDevices(context.Background(), DevicesInput{Site: "Springfield"})
	if err != nil {
		t.Fatalf("listDevices: %v", err)
	}
	if sent != "1" {
		t.Errorf("locationIds = %q, want the resolved id 1", sent)
	}
	if len(out.Devices) != 1 || out.Total != 1 {
		t.Errorf("got %d devices of %d", len(out.Devices), out.Total)
	}
	if !strings.Contains(out.Note, "Springfield") {
		t.Errorf("the note does not say which site was read: %q", out.Note)
	}
}

// A name matching two places is refused with the candidates named. Quietly
// picking the first would answer confidently about the wrong building, and
// nothing in the answer would say so -- two floors called "1" is the ordinary
// case, not a contrived one.
func TestLocationID_RefusesAnAmbiguousName(t *testing.T) {
	p := toolPlugin(t, jsonOK(locationTree))
	_, _, err := p.locationID(context.Background(), "1")
	if err == nil {
		t.Fatal("a name matching two floors was resolved to one of them")
	}
	if !strings.Contains(err.Error(), "Springfield/Main/1") ||
		!strings.Contains(err.Error(), "Northgate/1") {
		t.Errorf("the refusal does not name the candidates: %v", err)
	}
}

// The full path disambiguates, which is what makes the refusal above
// actionable rather than a dead end.
func TestLocationID_TakesTheFullPath(t *testing.T) {
	p := toolPlugin(t, jsonOK(locationTree))
	id, where, err := p.locationID(context.Background(), "Springfield/Main/1")
	if err != nil {
		t.Fatalf("locationID: %v", err)
	}
	if id != 3 {
		t.Errorf("id = %d, want 3", id)
	}
	if where != "Springfield/Main/1" {
		t.Errorf("described as %q", where)
	}
}

// The hierarchy is flattened with a path per row, because that is what a
// caller needs from it: a name to pass to another tool, and enough context to
// know which one they picked.
func TestListLocations_FlattensWithPaths(t *testing.T) {
	p := toolPlugin(t, jsonOK(locationTree))
	out, err := p.listLocations(context.Background(), LocationsInput{})
	if err != nil {
		t.Fatalf("listLocations: %v", err)
	}
	if out.Returned != 5 {
		t.Fatalf("returned %d locations, want all 5 of the tree", out.Returned)
	}
	var floor locationRow
	for _, row := range out.Locations {
		if row.ID == 3 {
			floor = row
		}
	}
	if floor.Path != "Springfield/Main/1" || floor.Type != "FLOOR" {
		t.Errorf("the nested floor came out as %+v", floor)
	}
}

// A model has whichever identifier was in front of it -- a serial from an
// asset list, a MAC from a switch table, a hostname from a ticket -- and this
// API keys everything on a numeric id that appears nowhere else.
func TestGetDevice_ResolvesASerialAndComposesThreeReads(t *testing.T) {
	p := toolPlugin(t, routes(t, map[string]string{
		"/devices":                     page(1, `{"id":4711,"serial_number":"SN123"}`),
		"/devices/4711":                `{"id":4711,"hostname":"ap-1","connected":true}`,
		"/devices/4711/location":       `{"id":3,"name":"1"}`,
		"/devices/4711/network-policy": `{"id":8,"name":"Corporate"}`,
	}))

	out, err := p.getDevice(context.Background(), DeviceInput{Device: "SN123"})
	if err != nil {
		t.Fatalf("getDevice: %v", err)
	}
	if out.Device["hostname"] != "ap-1" {
		t.Errorf("device = %v", out.Device)
	}
	if out.Location["name"] != "1" {
		t.Errorf("location = %v", out.Location)
	}
	if out.NetworkPolicy["name"] != "Corporate" {
		t.Errorf("network policy = %v", out.NetworkPolicy)
	}
	if len(out.Warnings) != 0 {
		t.Errorf("warnings on a call where nothing failed: %v", out.Warnings)
	}
}

// The device itself is the answer. A token without the scope to read policies
// should still be told about the access point somebody asked about, with the
// gap named rather than the whole call failing.
func TestGetDevice_NamesWhatItCouldNotReadRatherThanFailing(t *testing.T) {
	p := toolPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/devices/4711":
			_, _ = io.WriteString(w, `{"id":4711,"hostname":"ap-1"}`)
		case "/devices/4711/network-policy":
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error_message":"no scope"}`)
		default:
			_, _ = io.WriteString(w, `{}`)
		}
	})

	out, err := p.getDevice(context.Background(), DeviceInput{Device: "4711"})
	if err != nil {
		t.Fatalf("the whole call failed because one part was refused: %v", err)
	}
	if out.Device["hostname"] != "ap-1" {
		t.Fatalf("the device was not returned: %v", out.Device)
	}
	if len(out.Warnings) != 1 || !strings.Contains(out.Warnings[0], "network policy") {
		t.Errorf("warnings = %v; the refused part is not named", out.Warnings)
	}
}

// A partial name finding two devices is refused rather than answered about one
// of them.
func TestDeviceID_RefusesAnAmbiguousName(t *testing.T) {
	p := toolPlugin(t, jsonOK(page(2, `{"id":1,"hostname":"ap-1"},{"id":2,"hostname":"ap-2"}`)))
	_, err := p.deviceID(context.Background(), "ap")
	if err == nil {
		t.Fatal("a name matching two devices resolved to one of them")
	}
	if !strings.Contains(err.Error(), "serial number") {
		t.Errorf("the refusal does not say what is unique: %v", err)
	}
}

// A name matching nothing says how the matching works, because a partial name
// is what somebody will have tried and it will never work.
func TestDeviceID_SaysTheMatchIsExact(t *testing.T) {
	p := toolPlugin(t, jsonOK(page(0, ``)))
	_, err := p.deviceID(context.Background(), "reception")
	if err == nil {
		t.Fatal("a name matching nothing was accepted")
	}
	if !strings.Contains(err.Error(), "matched exactly") {
		t.Errorf("the refusal does not say the match is exact: %v", err)
	}
}

// The counts cover the whole window whatever the listing was truncated to, so
// "how bad is it" is answered even when "what exactly happened" is not.
func TestListAlerts_KeepsTheCountsWhenTheListingIsCut(t *testing.T) {
	var window string
	p := toolPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/alerts/count-by-SEVERITY":
			window = r.URL.Query().Get("startTime")
			_, _ = io.WriteString(w, `[{"group_id":1,"group_name":"CRITICAL","count":91},
				{"group_id":2,"group_name":"WARNING","count":204}]`)
		case "/alerts":
			_, _ = io.WriteString(w,
				`{"page":1,"count":2,"total_count":295,"data":[{"id":"a"},{"id":"b"}]}`)
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
	})

	out, err := p.listAlerts(context.Background(), AlertsInput{Limit: 2})
	if err != nil {
		t.Fatalf("listAlerts: %v", err)
	}
	if len(out.CountsBySeverity) != 2 {
		t.Errorf("counts = %v; they are what survives truncation", out.CountsBySeverity)
	}
	if out.Total != 295 || !out.Truncated {
		t.Errorf("Total = %d truncated = %v; a cut listing has to say so",
			out.Total, out.Truncated)
	}
	if out.Window == "" {
		t.Error("no window was reported; a count without one is a number with no unit")
	}
	// The counts have to cover the same window as the listing, or the two
	// halves of the answer describe different hours.
	if window == "" {
		t.Error("the counts were fetched without a window")
	}
}

// A listing that cannot be counted is still a listing. The counts are the nice
// half; refusing the whole answer because a summary endpoint was unhappy would
// be the worse trade.
func TestListAlerts_SurvivesTheCountEndpointFailing(t *testing.T) {
	p := toolPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/alerts/count-by") {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error_message":"boom"}`)
			return
		}
		_, _ = io.WriteString(w, page(1, `{"id":"a"}`))
	})

	out, err := p.listAlerts(context.Background(), AlertsInput{})
	if err != nil {
		t.Fatalf("the listing failed because the counts did: %v", err)
	}
	if len(out.Alerts) != 1 {
		t.Errorf("alerts = %v", out.Alerts)
	}
	if len(out.Warnings) != 1 {
		t.Errorf("the failed count is not named: %v", out.Warnings)
	}
}

// Newest first, explicitly. The API defaults to it and says so, but a default
// is a thing that changes -- and an assistant reading the first ten of a
// thousand alerts must be reading the ten that just happened.
func TestListAlerts_AsksForNewestFirst(t *testing.T) {
	var order, sortField string
	p := toolPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/alerts" {
			order, sortField = r.URL.Query().Get("order"), r.URL.Query().Get("sortField")
		}
		if strings.HasPrefix(r.URL.Path, "/alerts/count-by") {
			_, _ = io.WriteString(w, `[]`)
			return
		}
		_, _ = io.WriteString(w, page(0, ``))
	})
	if _, err := p.listAlerts(context.Background(), AlertsInput{}); err != nil {
		t.Fatalf("listAlerts: %v", err)
	}
	if sortField != "TIMESTAMP" || order != "DESC" {
		t.Errorf("sortField=%q order=%q, want the newest first", sortField, order)
	}
}

// Empty collections read as a failed call, so a device with nothing to report
// says so in words.
func TestGetDeviceHealth_SaysWhenThereIsNothingToReport(t *testing.T) {
	p := toolPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/alarms"):
			_, _ = io.WriteString(w, page(0, ``))
		case r.URL.Path == "/d360/device/issues":
			// An object rather than an array: this endpoint answers with
			// counts, not rows.
			_, _ = io.WriteString(w, `{}`)
		default:
			_, _ = io.WriteString(w, `[]`)
		}
	})

	out, err := p.getDeviceHealth(context.Background(), DeviceHealthInput{Device: "4711"})
	if err != nil {
		t.Fatalf("getDeviceHealth: %v", err)
	}
	if out.Note == "" {
		t.Error("an empty result said nothing about being empty")
	}
	if out.Window == "" {
		t.Error("no window was reported")
	}
	if out.DeviceID != 4711 {
		t.Errorf("DeviceID = %d; it is echoed because the caller may have named "+
			"a serial", out.DeviceID)
	}
}

// A device whose own numbers are fine and whose clients are all failing
// authentication is a RADIUS problem, not a hardware one -- and that is not
// visible in the processor, memory or radio series. It is why the failure
// counts are on this tool rather than on a fifteenth one.
func TestGetDeviceHealth_CountsClientFailures(t *testing.T) {
	p := toolPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/alarms"):
			_, _ = io.WriteString(w, page(0, ``))
		case r.URL.Path == "/d360/device/issues":
			_, _ = io.WriteString(w,
				`{"authentication_failures":41,"association_failures":0,`+
					`"ip_address_issues":2,"total_clients":58}`)
		default:
			_, _ = io.WriteString(w, `[]`)
		}
	})

	out, err := p.getDeviceHealth(context.Background(), DeviceHealthInput{Device: "4711"})
	if err != nil {
		t.Fatalf("getDeviceHealth: %v", err)
	}
	if out.ClientFailures["authentication_failures"] != float64(41) {
		t.Errorf("client failures = %v", out.ClientFailures)
	}
	// And with something to report, it must not also claim there is nothing.
	if strings.Contains(out.Note, "holds no samples") {
		t.Errorf("a device with 41 auth failures was described as having "+
			"nothing to report: %q", out.Note)
	}
}

// A summary where every read failed is not a partial answer, it is a broken
// connection wearing the shape of one.
func TestGetEstateSummary_FailsOnlyWhenEverythingDoes(t *testing.T) {
	allBad := toolPlugin(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error_message":"down"}`)
	})
	if _, err := allBad.getEstateSummary(context.Background(), EstateSummaryInput{}); err == nil {
		t.Error("a summary where nothing succeeded reported itself as an answer")
	}

	partial := toolPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/devices/stats" {
			_, _ = io.WriteString(w, `{"total_device_count":42,"connected_device_count":40}`)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error_message":"no scope"}`)
	})
	out, err := partial.getEstateSummary(context.Background(), EstateSummaryInput{})
	if err != nil {
		t.Fatalf("a summary with one part readable failed entirely: %v", err)
	}
	if out.Devices["total_device_count"] != float64(42) {
		t.Errorf("devices = %v", out.Devices)
	}
	if len(out.Warnings) != 2 {
		t.Errorf("warnings = %v; both refused parts should be named", out.Warnings)
	}
}

// A model told "not configured yet" stops and says so; one handed a connection
// error tries three more tools first.
func TestTools_RefuseAnUnconfiguredInstanceByName(t *testing.T) {
	p := toolPlugin(t, jsonOK(`{}`))
	p.configured = false

	if _, err := p.listDevices(context.Background(), DevicesInput{}); err == nil ||
		!strings.Contains(err.Error(), "not configured yet") {
		t.Errorf("listDevices on an unconfigured instance: %v", err)
	}
	if _, err := p.getEstateSummary(context.Background(), EstateSummaryInput{}); err == nil ||
		!strings.Contains(err.Error(), "not configured yet") {
		t.Errorf("getEstateSummary on an unconfigured instance: %v", err)
	}
}

// A word outside the vocabulary is refused with the vocabulary, rather than
// silently becoming the default -- a caller who asked for metrics and got
// identity fields would report the absence of a health score as a fact.
func TestViews_RefuseAWordTheyDoNotKnow(t *testing.T) {
	p := toolPlugin(t, jsonOK(page(0, ``)))
	_, err := p.listClients(context.Background(), ClientsInput{View: "everything"})
	if err == nil {
		t.Fatal("an unknown view was accepted")
	}
	for _, name := range []string{"basic", "metrics", "full"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("the refusal does not offer %q: %v", name, err)
		}
	}
}

// How the estate is arranged changes when somebody changes it, so it is held.
// Whether an access point is connected does not, so it is not -- a cached
// answer to that is indistinguishable from a true one.
func TestReadCache_HoldsArrangementAndNothingElse(t *testing.T) {
	var tree, devices, alerts atomic.Int32
	p := cachingPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/locations/tree":
			tree.Add(1)
			_, _ = io.WriteString(w, locationTree)
		case r.URL.Path == "/devices":
			devices.Add(1)
			_, _ = io.WriteString(w, page(0, ``))
		case strings.HasPrefix(r.URL.Path, "/alerts"):
			alerts.Add(1)
			if strings.Contains(r.URL.Path, "count-by") {
				_, _ = io.WriteString(w, `[]`)
				return
			}
			_, _ = io.WriteString(w, page(0, ``))
		}
	})

	for range 3 {
		if _, err := p.listLocations(context.Background(), LocationsInput{}); err != nil {
			t.Fatalf("listLocations: %v", err)
		}
		if _, err := p.listDevices(context.Background(), DevicesInput{}); err != nil {
			t.Fatalf("listDevices: %v", err)
		}
		if _, err := p.listAlerts(context.Background(), AlertsInput{}); err != nil {
			t.Fatalf("listAlerts: %v", err)
		}
	}

	if got := tree.Load(); got != 1 {
		t.Errorf("the location tree was fetched %d times; it is cacheable", got)
	}
	if got := devices.Load(); got != 3 {
		t.Errorf("devices were fetched %d times; whether one is connected must "+
			"never be answered from memory", got)
	}
	if got := alerts.Load(); got != 6 {
		t.Errorf("alerts were fetched %d times over three calls; they are read "+
			"precisely when somebody suspects something is wrong", got)
	}
}
