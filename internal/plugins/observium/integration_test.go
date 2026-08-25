package observium

import (
	"context"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/plugins"
)

// Run against a real Observium. Skipped unless the connection is supplied,
// so it costs nothing in CI and is there when somebody has an instance:
//
//	OBSERVIUM_TEST_DB_HOST=192.168.50.101 \
//	OBSERVIUM_TEST_DB_NAME=observium \
//	OBSERVIUM_TEST_DB_USER=mcpd_ro \
//	OBSERVIUM_TEST_DB_PASSWORD=… \
//	go test ./internal/plugins/observium/ -run Integration -v
//
// What it defends is the half of this package that unit tests cannot reach.
// The grant check, the schema check and every column name are claims about
// somebody else's database, and a fake proves nothing about any of them.
func integrationPlugin(t *testing.T) *Plugin {
	t.Helper()
	host := os.Getenv("OBSERVIUM_TEST_DB_HOST")
	if host == "" {
		t.Skip("set OBSERVIUM_TEST_DB_HOST and friends to run against a real Observium")
	}
	port := 3306
	if p := os.Getenv("OBSERVIUM_TEST_DB_PORT"); p != "" {
		port, _ = strconv.Atoi(p)
	}

	cfg := Config{
		Backend: BackendDatabase,
		DBHost:  host, DBPort: port,
		DBName:     os.Getenv("OBSERVIUM_TEST_DB_NAME"),
		DBUser:     os.Getenv("OBSERVIUM_TEST_DB_USER"),
		DBPassword: os.Getenv("OBSERVIUM_TEST_DB_PASSWORD"),
	}
	p, err := New(pluginDepsFor(t), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	return p
}

func pluginDepsFor(t *testing.T) plugins.Deps {
	t.Helper()
	return plugins.Deps{
		Instance: "observium",
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:      time.Now,
	}
}

// Probe is the whole startup contract: reachable, the account cannot write,
// and every column these queries name exists. A failure here is the thing an
// operator would otherwise meet inside their first tool call.
func TestIntegration_Probe(t *testing.T) {
	p := integrationPlugin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := p.reader.Probe(ctx); err != nil {
		t.Fatalf("probe failed against a real Observium: %v", err)
	}
	t.Logf("reading %s", p.reader.Describe())
}

// Every tool, against real data. Empty is a legitimate answer -- a small
// estate has no alerts -- so this checks that each one runs and returns the
// shape it promises rather than that it found something.
func TestIntegration_EveryToolRuns(t *testing.T) {
	p := integrationPlugin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := p.reader.Probe(ctx); err != nil {
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

	// Every later call is scoped to a real device id, which is how an
	// assistant would actually use these.
	id, _ := devices.Items[0]["device_id"].(int64)
	t.Logf("scoping to device_id=%d (%v)", id, devices.Items[0]["hostname"])

	one, err := p.getDevice(ctx, deviceArgs{DeviceID: int(id)})
	if err != nil {
		t.Errorf("device: %v", err)
	} else if one.Count != 1 {
		t.Errorf("device returned %d rows, want 1", one.Count)
	}

	ports, err := p.listPorts(ctx, portsArgs{DeviceID: int(id)})
	if err != nil {
		t.Errorf("ports: %v", err)
	} else {
		t.Logf("ports: %d", ports.Count)
		// The claim this package corrected: Observium computes per-second
		// rates on every poll. If they are absent the ports tool is
		// describing something it does not have.
		var withRate int
		for _, port := range ports.Items {
			if _, ok := port["ifInOctets_rate"]; ok {
				withRate++
			}
		}
		if ports.Count > 0 && withRate == 0 {
			t.Error("no interface carried ifInOctets_rate; the rate columns are " +
				"the reason this backend can answer about throughput")
		}
	}

	sensors, err := p.listSensors(ctx, sensorsArgs{DeviceID: int(id)})
	if err != nil {
		t.Errorf("sensors: %v", err)
	} else {
		t.Logf("sensors: %d", sensors.Count)
	}

	alerts, err := p.listAlerts(ctx, alertsArgs{})
	if err != nil {
		t.Errorf("alerts: %v", err)
	} else {
		t.Logf("alerts: %d (%s)", alerts.Count, alerts.Note)
	}

	history, err := p.alertHistory(ctx, alertHistoryArgs{Limit: 5})
	if err != nil {
		t.Errorf("alert_history: %v", err)
	} else {
		t.Logf("alert history: %d", history.Count)
	}

	capacity, err := p.capacity(ctx, capacityArgs{DeviceID: int(id)})
	if err != nil {
		t.Errorf("capacity: %v", err)
	} else {
		t.Logf("capacity: storage=%d memory=%d processors=%d",
			capacity.Storage.Count, capacity.Memory.Count, capacity.Processors.Count)
	}

	topo, err := p.topology(ctx, topologyArgs{DeviceID: int(id), VLANs: true})
	if err != nil {
		t.Errorf("topology: %v", err)
	} else {
		t.Logf("topology: neighbours=%d addresses=%d vlans=%d",
			topo.Neighbours.Count, topo.Addresses.Count, topo.VLANs.Count)
	}

	inv, err := p.listInventory(ctx, inventoryArgs{DeviceID: int(id)})
	if err != nil {
		t.Errorf("inventory: %v", err)
	} else {
		t.Logf("inventory: %d", inv.Count)
	}
}

// Values have to survive as values. MySQL hands back []byte for almost
// everything, and a hostname that reaches a model as base64 is a hostname
// nobody can use.
func TestIntegration_ValuesAreUsable(t *testing.T) {
	p := integrationPlugin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := p.reader.Probe(ctx); err != nil {
		t.Fatalf("probe: %v", err)
	}

	page, err := p.reader.Read(ctx, EntityDevices, url.Values{}, 5)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(page.Items) == 0 {
		t.Skip("nothing monitored")
	}
	d := page.Items[0]

	if _, ok := d["hostname"].(string); !ok {
		t.Errorf("hostname is %T, want a string -- a []byte marshals to base64", d["hostname"])
	}
	if _, ok := d["device_id"].(int64); !ok {
		t.Errorf("device_id is %T, want a number a model can compare", d["device_id"])
	}
	// A credential must never appear, whatever the schema grows.
	for key := range d {
		switch key {
		case "snmp_community", "snmp_authpass", "snmp_cryptopass":
			t.Errorf("a device carried %q into the tool result", key)
		}
	}
	t.Logf("device: id=%v hostname=%v os=%v status=%v uptime=%v",
		d["device_id"], d["hostname"], d["os"], d["status"], d["uptime"])
}

// The filters, against real data.
//
// Every one of these was wrong until it was run against an actual Observium.
// The API's vocabulary and the schema's values disagree in ways the
// documentation does not mention: status=up is a tinyint 1, the sensor event
// the API calls "warn" is stored as "warning", and ports had no state mapping
// at all. Each failure looked identical from a fake -- a query that runs and
// matches nothing.
func TestIntegration_FiltersActuallyMatch(t *testing.T) {
	p := integrationPlugin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := p.reader.Probe(ctx); err != nil {
		t.Fatalf("probe: %v", err)
	}

	all, err := p.listDevices(ctx, devicesArgs{})
	if err != nil {
		t.Fatalf("devices: %v", err)
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
	// The bug this defends: status=up became WHERE status = 'up' against a
	// tinyint, matched nothing, and reported an estate where no device is up.
	if all.Count > 0 && up.Count+down.Count == 0 {
		t.Error("no device matched up or down, so the status filter is not " +
			"reaching the column the schema actually uses")
	}

	upPorts, err := p.listPorts(ctx, portsArgs{State: "up"})
	if err != nil {
		t.Fatalf("ports state=up: %v", err)
	}
	allPorts, err := p.listPorts(ctx, portsArgs{})
	if err != nil {
		t.Fatalf("ports: %v", err)
	}
	t.Logf("ports: %d total, %d up", allPorts.Count, upPorts.Count)
	if allPorts.Count > 0 && upPorts.Count == 0 {
		t.Error("no interface matched state=up; ports had no state mapping at all")
	}
	if upPorts.Count > allPorts.Count {
		t.Error("filtering returned more than not filtering")
	}

	// "warn" is what the API documents and what this tool used to advertise.
	// It must reach the enum's "warning" rather than matching nothing.
	for _, event := range []string{"ok", "warn", "warning", "alert"} {
		got, err := p.listSensors(ctx, sensorsArgs{Event: event})
		if err != nil {
			t.Errorf("sensors event=%s: %v", event, err)
			continue
		}
		t.Logf("sensors event=%s: %d", event, got.Count)
	}

	// A filter no backend can apply must be an error, never a full result
	// wearing a filter's name.
	if _, err := p.listAlerts(ctx, alertsArgs{Status: "suppressed"}); err == nil {
		t.Error("an unsupported alert status was accepted; answering without " +
			"the filter would report every alert as suppressed")
	}

	// Alert history takes a window, which needs a conversion the schema does
	// not do for us: the argument is a unix timestamp, the column a datetime.
	recent, err := p.alertHistory(ctx, alertHistoryArgs{
		From:  time.Now().Add(-90 * 24 * time.Hour).Unix(),
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("alert_history windowed: %v", err)
	}
	t.Logf("alert history in the last 90 days: %d", recent.Count)
}
