package cnmaestro

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/plugins"
)

// testPlugin builds a plugin around the fake API.
//
// The struct is built directly rather than through New, which requires an
// https address the fake server does not have. What is under test here is the
// tools, not the constructor.
func testPlugin(t *testing.T, f *fakeAPI, mutate func(*Config)) *Plugin {
	t.Helper()
	client := testClient(t, f, mutate)
	return &Plugin{
		deps:       plugins.Deps{Log: discardLogger(), Now: time.Now},
		cfg:        client.cfg,
		client:     client,
		configured: true,
	}
}

func emptyPage(w http.ResponseWriter, _ *http.Request) {
	_, _ = io.WriteString(w, `{"data":[],"paging":{"total":0}}`)
}

// A read about one named device is not a statement about every account.
//
// The note exists because an unnamed account means different things per
// request, but "this spans every account the credential can see" is false
// about a single access point's clients -- and a caller acting on it would go
// looking for a tenant that has nothing to do with the answer.
func TestSpanningNote_SaysNothingForADeviceScopedRead(t *testing.T) {
	if got := spanningNote("", scopeDevice); got != "" {
		t.Fatalf("device-scoped note = %q, want none", got)
	}
	if got := spanningNote("", scopeEstate); !strings.Contains(got, "every account") {
		t.Fatalf("estate note = %q, want it to say the read spans accounts", got)
	}
	if got := spanningNote("", scopeHierarchy); !strings.Contains(got, "main account") {
		t.Fatalf("hierarchy note = %q, want it to say the main account answered", got)
	}
	if got := spanningNote("Acme Networks", scopeEstate); got != "" {
		t.Fatalf("note with an account named = %q, want none", got)
	}
}

// A filter the API rejects is caught here, where the message can name the
// choices. Upstream answers 400 without them, and a model that cannot see the
// choices guesses again.
func TestAlarms_RefusesAnUnknownSeverity(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("/alarms", emptyPage)
	p := testPlugin(t, f, nil)

	_, err := p.listAlarms(context.Background(), AlarmsInput{Severity: "urgent"})
	if err == nil {
		t.Fatal("an unknown severity must be refused")
	}
	for _, want := range []string{"critical", "major", "minor", "urgent"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if f.dataRequests.Load() != 0 {
		t.Fatal("a refused filter must not reach the API")
	}
}

// The account travels with every read, so one instance answers about any
// tenant. Filters go with it rather than replacing it.
func TestAlarms_SendsFiltersAndTheNamedAccount(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("/alarms", emptyPage)
	p := testPlugin(t, f, func(cfg *Config) { cfg.ManagedAccount = MainAccount })

	out, err := p.listAlarms(context.Background(), AlarmsInput{
		Account: "Acme Networks", Severity: "Critical", Network: "Campus-1",
	})
	if err != nil {
		t.Fatalf("listAlarms: %v", err)
	}
	q := f.query()
	if got := q.Get("managed_account"); got != "Acme Networks" {
		t.Errorf("managed_account = %q, want the account the call named", got)
	}
	// Matched case-insensitively and sent as the API spells it.
	if got := q.Get("severity"); got != "critical" {
		t.Errorf("severity = %q, want %q", got, "critical")
	}
	if got := q.Get("network"); got != "Campus-1" {
		t.Errorf("network = %q, want it passed through", got)
	}
	if out.Account != "Acme Networks" {
		t.Errorf("reported account = %q, want the one that answered", out.Account)
	}
}

// Sites and towers hang off a network in the API's hierarchy. An empty name
// would request the collection above them, which answers 200 with the wrong
// thing -- a listing of networks presented as a listing of sites.
func TestSites_RefuseAnEmptyNetwork(t *testing.T) {
	f := newFakeAPI(t)
	p := testPlugin(t, f, nil)

	if _, err := p.listSites(context.Background(), SitesInput{}); err == nil {
		t.Fatal("an empty network name must be refused")
	}
	if _, err := p.listTowers(context.Background(), TowersInput{Network: "  "}); err == nil {
		t.Fatal("a blank network name must be refused")
	}
	if f.dataRequests.Load() != 0 {
		t.Fatal("neither may reach the API")
	}
}

func TestSites_BuildTheNetworkScopedPath(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("/networks/Campus%201/sites", emptyPage)
	p := testPlugin(t, f, nil)

	if _, err := p.listSites(context.Background(), SitesInput{Network: "Campus 1"}); err != nil {
		t.Fatalf("listSites: %v", err)
	}
	if f.dataRequests.Load() != 1 {
		t.Fatal("a network name with a space must still reach the sites path")
	}
}

// The API requires both times on a performance read and defaults neither.
// Saying so here is the difference between a clear message and a 400 naming a
// parameter the caller never saw.
func TestDevicePerformance_RequiresBothTimes(t *testing.T) {
	f := newFakeAPI(t)
	p := testPlugin(t, f, nil)
	mac := "AA:BB:CC:DD:EE:FF"

	_, err := p.getDevicePerformance(context.Background(),
		DevicePerformanceInput{MAC: mac, StopTime: "2026-08-01T00:00:00Z"})
	if err == nil || !strings.Contains(err.Error(), "start_time") {
		t.Fatalf("error = %v, want it to name start_time", err)
	}

	_, err = p.getDevicePerformance(context.Background(), DevicePerformanceInput{
		MAC: mac, StartTime: "last tuesday", StopTime: "2026-08-01T00:00:00Z",
	})
	if err == nil || !strings.Contains(err.Error(), "ISO 8601") {
		t.Fatalf("error = %v, want it to say what a timestamp looks like", err)
	}
	if f.dataRequests.Load() != 0 {
		t.Fatal("neither may reach the API")
	}
}

// Naming a device asks about that access point; naming none asks about the
// estate. They are different paths, and the estate-wide one is the request
// that gets truncated on a real network.
func TestClients_DeviceScopedUsesTheDevicePath(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("/devices/AA:BB:CC:DD:EE:FF/clients", emptyPage)
	f.handle("/devices/clients", emptyPage)
	p := testPlugin(t, f, nil)

	out, err := p.listClients(context.Background(),
		ClientsInput{Device: "AA:BB:CC:DD:EE:FF", Type: "all"})
	if err != nil {
		t.Fatalf("listClients: %v", err)
	}
	if got := f.query().Get("client_type"); got != "all" {
		t.Errorf("client_type = %q, want it sent", got)
	}
	// The one place this is load-bearing: a device-scoped answer must not
	// claim to be about every account.
	if out.Note != "" {
		t.Errorf("note = %q, want none for a single access point", out.Note)
	}

	if _, err := p.listClients(context.Background(), ClientsInput{}); err != nil {
		t.Fatalf("estate-wide listClients: %v", err)
	}
	if f.dataRequests.Load() != 2 {
		t.Fatalf("data requests = %d, want one per form", f.dataRequests.Load())
	}
}

// A mistyped MAC is a message rather than a 404 that reads as "no such
// device" and invites the conclusion that the estate is missing something.
func TestDeviceScopedTools_RefuseABadMAC(t *testing.T) {
	f := newFakeAPI(t)
	p := testPlugin(t, f, nil)

	if _, err := p.getDeviceStatistics(context.Background(),
		DeviceStatisticsInput{MAC: "not-a-mac"}); err == nil {
		t.Fatal("device_statistics must refuse a bad MAC")
	}
	if _, err := p.listClients(context.Background(),
		ClientsInput{Device: "not-a-mac"}); err == nil {
		t.Fatal("clients must refuse a bad device MAC")
	}
	if f.dataRequests.Load() != 0 {
		t.Fatal("neither may reach the API")
	}
}
