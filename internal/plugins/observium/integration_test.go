package observium

import (
	"context"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/plugins"
)

// Run against a real Observium. Skipped unless one is supplied, so it costs
// nothing in CI and is there when somebody has a subscription instance:
//
//	OBSERVIUM_TEST_URL=https://observium.example.com \
//	OBSERVIUM_TEST_TOKEN=… \
//	go test ./internal/plugins/observium/ -run Integration -v
//
// This is the half of the package a fake cannot reach, and it is worth more
// than the rest of the suite put together. The database backend this replaced
// had every one of its filters broken -- status matched nothing, one was
// dropped silently, an alert state was inverted -- and all of it looked
// correct until these ran. Nothing here has been run against a live API.
func integrationPlugin(t *testing.T) *Plugin {
	t.Helper()
	base := os.Getenv("OBSERVIUM_TEST_URL")
	if base == "" {
		t.Skip("set OBSERVIUM_TEST_URL and OBSERVIUM_TEST_TOKEN to run against a real Observium")
	}

	cfg := Config{
		BaseURL:  base,
		Token:    os.Getenv("OBSERVIUM_TEST_TOKEN"),
		Username: os.Getenv("OBSERVIUM_TEST_USER"),
		Password: os.Getenv("OBSERVIUM_TEST_PASSWORD"),
	}
	p, err := New(plugins.Deps{
		Instance: "observium",
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:      time.Now,
	}, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

// Probe is the startup contract: the address resolves, the credential is
// accepted, and what comes back is the API's JSON rather than a sign-in page.
//
// A redirect here is the answer to the most likely misconfiguration there is
// -- no API at this address, because it is switched off or because this is
// Community Edition -- and the error says which two things to check.
func TestIntegration_Probe(t *testing.T) {
	p := integrationPlugin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := p.client.Probe(ctx); err != nil {
		t.Fatalf("probe failed against a real Observium: %v", err)
	}
	t.Logf("reading %s", p.client.Describe())
}

// Every tool, against real data. Empty is a legitimate answer -- a small
// estate has no alerts -- so this checks each runs and returns the shape it
// promises rather than that it found something.
func TestIntegration_EveryToolRuns(t *testing.T) {
	p := integrationPlugin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := p.client.Probe(ctx); err != nil {
		t.Fatalf("probe: %v", err)
	}

	devices, err := p.listDevices(ctx, devicesArgs{})
	if err != nil {
		t.Fatalf("devices: %v", err)
	}
	t.Logf("devices: %d", devices.Count)
	if devices.Count == 0 {
		t.Skip("this Observium monitors nothing, so the rest proves little")
	}

	id := deviceID(t, devices.Items[0])
	t.Logf("scoping to device_id=%d (%v)", id, devices.Items[0]["hostname"])

	one, err := p.getDevice(ctx, deviceArgs{DeviceID: id})
	if err != nil {
		t.Errorf("device: %v", err)
	} else if one.Count != 1 {
		t.Errorf("device returned %d rows, want 1", one.Count)
	}

	ports, err := p.listPorts(ctx, portsArgs{DeviceID: id})
	if err != nil {
		t.Errorf("ports: %v", err)
	} else {
		t.Logf("ports: %d", ports.Count)
		// Observium computes per-second rates on every poll and stores them
		// beside the counters. The tool's own note promises them, so their
		// absence would make it describe something it does not have.
		var withRate int
		for _, port := range ports.Items {
			if _, ok := port["ifInOctets_rate"]; ok {
				withRate++
			}
		}
		if ports.Count > 0 && withRate == 0 {
			t.Error("no interface carried ifInOctets_rate, which the ports tool " +
				"tells the model to read for current throughput")
		}
	}

	for name, run := range map[string]func() (int, error){
		"sensors": func() (int, error) {
			r, err := p.listSensors(ctx, sensorsArgs{DeviceID: id})
			return r.Sensors.Count, err
		},
		"state indicators": func() (int, error) {
			r, err := p.listSensors(ctx, sensorsArgs{DeviceID: id})
			return r.Status.Count, err
		},
		"maintenance": func() (int, error) {
			r, err := p.listMaintenance(ctx, maintenanceArgs{})
			return r.Count, err
		},
		"groups": func() (int, error) {
			r, err := p.listGroups(ctx, groupsArgs{})
			return r.Count, err
		},
		"alerts": func() (int, error) {
			r, err := p.listAlerts(ctx, alertsArgs{})
			return r.Count, err
		},
		"alert_history": func() (int, error) {
			r, err := p.alertHistory(ctx, alertHistoryArgs{Limit: 5})
			return r.Count, err
		},
		"inventory": func() (int, error) {
			r, err := p.listInventory(ctx, inventoryArgs{DeviceID: id})
			return r.Count, err
		},
	} {
		n, err := run()
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		t.Logf("%s: %d", name, n)
	}

	capacity, err := p.capacity(ctx, capacityArgs{DeviceID: id})
	if err != nil {
		t.Errorf("capacity: %v", err)
	} else {
		t.Logf("capacity: storage=%d memory=%d processors=%d",
			capacity.Storage.Count, capacity.Memory.Count, capacity.Processors.Count)
	}

	// VLANs need a level 7 account, so a refusal is reported in place rather
	// than failing the call -- the neighbours are still most of the answer.
	topo, err := p.topology(ctx, topologyArgs{DeviceID: id, VLANs: true})
	if err != nil {
		t.Errorf("topology: %v", err)
	} else {
		t.Logf("topology: neighbours=%d addresses=%d vlans=%d (%s)",
			topo.Neighbours.Count, topo.Addresses.Count, topo.VLANs.Count, topo.VLANs.Note)
	}
}

// The filters, against real data.
//
// This is the test that matters most. Every filter in the backend this
// replaced was wrong, and each failed the same way: a request that succeeds,
// matches nothing, and returns an empty result which reads as an answer. The
// API's documented vocabulary has never been checked against what a live
// instance actually accepts.
func TestIntegration_FiltersActuallyMatch(t *testing.T) {
	p := integrationPlugin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := p.client.Probe(ctx); err != nil {
		t.Fatalf("probe: %v", err)
	}

	all, err := p.listDevices(ctx, devicesArgs{})
	if err != nil {
		t.Fatalf("devices: %v", err)
	}
	if all.Count == 0 {
		t.Skip("nothing monitored")
	}

	up, err := p.listDevices(ctx, devicesArgs{Status: "up"})
	if err != nil {
		t.Fatalf("devices status=up: %v", err)
	}
	down, err := p.listDevices(ctx, devicesArgs{Status: "down"})
	if err != nil {
		t.Fatalf("devices status=down: %v", err)
	}
	t.Logf("devices: %d total, %d up, %d down", all.Count, up.Count, down.Count)
	if up.Count+down.Count == 0 {
		t.Error("no device matched up or down, so the status filter is not " +
			"reaching anything this Observium understands")
	}
	if up.Count > all.Count || down.Count > all.Count {
		t.Error("filtering returned more than not filtering")
	}

	allPorts, err := p.listPorts(ctx, portsArgs{})
	if err != nil {
		t.Fatalf("ports: %v", err)
	}
	upPorts, err := p.listPorts(ctx, portsArgs{State: "up"})
	if err != nil {
		t.Fatalf("ports state=up: %v", err)
	}
	t.Logf("ports: %d total, %d up", allPorts.Count, upPorts.Count)
	if allPorts.Count > 0 && upPorts.Count == 0 {
		t.Error("no interface matched state=up; the filter is being accepted " +
			"and matching nothing, which reads as an estate with no live ports")
	}
	if upPorts.Count > allPorts.Count {
		t.Error("filtering returned more than not filtering")
	}

	// The API documents these; whether the enum behind them agrees is exactly
	// the thing that was wrong last time.
	for _, event := range []string{"ok", "warning", "alert"} {
		got, err := p.listSensors(ctx, sensorsArgs{Event: event})
		if err != nil {
			t.Errorf("sensors event=%s: %v", event, err)
			continue
		}
		t.Logf("sensors event=%s: %d sensors, %d state indicators",
			event, got.Sensors.Count, got.Status.Count)
	}

	recent, err := p.alertHistory(ctx, alertHistoryArgs{
		From:  time.Now().Add(-90 * 24 * time.Hour).Unix(),
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("alert_history windowed: %v", err)
	}
	t.Logf("alert history in the last 90 days: %d", recent.Count)
}

// Values have to arrive as values. A hostname that reaches a model as a
// number, or a number as a string, is one it cannot reason about.
func TestIntegration_ValuesAreUsable(t *testing.T) {
	p := integrationPlugin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := p.client.Probe(ctx); err != nil {
		t.Fatalf("probe: %v", err)
	}

	page, err := p.client.Read(ctx, EntityDevices, url.Values{}, 5)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(page.Items) == 0 {
		t.Skip("nothing monitored")
	}
	d := page.Items[0]

	if s, ok := d["hostname"].(string); !ok || strings.TrimSpace(s) == "" {
		t.Errorf("hostname is %T (%v), want a non-empty string", d["hostname"], d["hostname"])
	}
	// A credential must never appear, whatever the API decides to include.
	for key := range d {
		switch key {
		case "snmp_community", "snmp_authpass", "snmp_cryptopass", "snmp_authname":
			t.Errorf("a device carried %q into the tool result", key)
		}
	}
	t.Logf("device: id=%v hostname=%v os=%v status=%v",
		d["device_id"], d["hostname"], d["os"], d["status"])
}

// deviceID copes with the API returning an id as a number or as a string,
// which it does inconsistently between endpoints.
func deviceID(t *testing.T, item map[string]any) int {
	t.Helper()
	switch v := item["device_id"].(type) {
	case float64:
		return int(v)
	case string:
		var n int
		for _, r := range v {
			if r < '0' || r > '9' {
				t.Fatalf("device_id %q is not a number", v)
			}
			n = n*10 + int(r-'0')
		}
		return n
	default:
		t.Fatalf("device_id is %T, want a number or a numeric string", v)
		return 0
	}
}
