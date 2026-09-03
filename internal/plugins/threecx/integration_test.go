package threecx

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/plugins"
)

// Run against a real 3CX. Skipped unless one is supplied, so it costs nothing
// in CI and is there when somebody has a PBX:
//
//	THREECX_TEST_HOST=acme.ny.3cx.us THREECX_TEST_EXTENSION=100 \
//	THREECX_TEST_PASSWORD=… go test ./internal/plugins/threecx/ -run Integration -v
//
// This is the half of the package a fake cannot reach. The fake server in
// tools_test.go answers with what the metadata says the API returns; these
// prove the API agrees, which on 3CX is worth checking: azir's plugin found
// two invented role names and a misrouted forwarding property that way.
func integrationPlugin(t *testing.T) *Plugin {
	t.Helper()
	host := os.Getenv("THREECX_TEST_HOST")
	ext := os.Getenv("THREECX_TEST_EXTENSION")
	pass := os.Getenv("THREECX_TEST_PASSWORD")
	if host == "" || ext == "" || pass == "" {
		t.Skip("set THREECX_TEST_HOST, THREECX_TEST_EXTENSION and THREECX_TEST_PASSWORD to run against a real 3CX")
	}
	p, err := New(plugins.Deps{
		Instance: "threecx",
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:      time.Now,
	}, Config{Customers: []Customer{{Name: "Trial", Host: host, Extension: ext, Password: pass}}})
	if err != nil {
		t.Fatalf("building the plugin: %v", err)
	}
	return p
}

func TestIntegration_Starts(t *testing.T) {
	p := integrationPlugin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if h := p.Check(ctx); h.State != plugins.HealthyState {
		t.Errorf("Check after a successful start: %+v", h)
	}
}

// Every read tool, against the live system, with the one property that matters
// most asserted on each: that it answers, and that nothing in the answer is a
// credential.
func TestIntegration_EveryReadAnswers(t *testing.T) {
	p := integrationPlugin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	status, err := p.getSystemStatus(ctx, statusArgs{})
	if err != nil {
		t.Fatalf("get_system_status: %v", err)
	}
	if status.Version == "" || status.FQDN == "" {
		t.Errorf("status should name the system: %+v", status)
	}
	t.Logf("status: %d/%d extensions, %d/%d trunks, concerns %v",
		status.ExtensionsRegistered, status.ExtensionsTotal, status.TrunksRegistered, status.TrunksTotal, status.Concerns)

	exts, err := p.listExtensions(ctx, extensionsArgs{})
	if err != nil {
		t.Fatalf("list_extensions: %v", err)
	}
	if exts.Total != status.ExtensionsTotal {
		t.Errorf("list_extensions total %d disagrees with status %d", exts.Total, status.ExtensionsTotal)
	}
	if len(exts.Extensions) > 0 {
		one, err := p.getExtension(ctx, extensionArgs{Extension: exts.Extensions[0].Number})
		if err != nil {
			t.Fatalf("get_extension %s: %v", exts.Extensions[0].Number, err)
		}
		if one.Number != exts.Extensions[0].Number {
			t.Errorf("get_extension answered for %s, asked %s", one.Number, exts.Extensions[0].Number)
		}
		t.Logf("extension %s: %d phones, %d profiles, %d keys", one.Number, len(one.Phones), len(one.Forwarding), len(one.Keys))
	}
	if _, err := p.getExtension(ctx, extensionArgs{Extension: "999999"}); err == nil ||
		!strings.Contains(err.Error(), "no extension") {
		t.Errorf("an unknown extension should be refused by name, got %v", err)
	}

	calls := []struct {
		name string
		run  func() error
	}{
		{"list_services", func() error { _, err := p.listServices(ctx, servicesArgs{}); return err }},
		{"list_active_calls", func() error { _, err := p.listActiveCalls(ctx, activeCallsArgs{}); return err }},
		{"search_events", func() error {
			_, err := p.searchEvents(ctx, eventsArgs{Query: "trunk", Type: "Info", Limit: 5})
			return err
		}},
		{"list_devices", func() error { _, err := p.listDevices(ctx, devicesArgs{}); return err }},
		{"list_trunks", func() error { _, err := p.listTrunks(ctx, trunksArgs{}); return err }},
		{"list_inbound_rules", func() error { _, err := p.listInboundRules(ctx, inboundRulesArgs{}); return err }},
		{"list_outbound_rules", func() error { _, err := p.listOutboundRules(ctx, outboundRulesArgs{}); return err }},
		{"search_directory", func() error { _, err := p.searchDirectory(ctx, directoryArgs{Type: "Extension"}); return err }},
		{"list_ring_groups", func() error { _, err := p.listRingGroups(ctx, ringGroupsArgs{}); return err }},
		{"list_queues", func() error { _, err := p.listQueues(ctx, queuesArgs{}); return err }},
		{"list_receptionists", func() error { _, err := p.listReceptionists(ctx, receptionistsArgs{}); return err }},
		{"get_schedule", func() error { _, err := p.getSchedule(ctx, scheduleArgs{}); return err }},
		{"search_call_history", func() error {
			_, err := p.searchCallHistory(ctx, callHistoryArgs{Since: "2020-01-01", Limit: 5})
			return err
		}},
	}
	for _, c := range calls {
		if err := c.run(); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

// The schedule against the live system: the default department resolves, its
// time zone is named rather than numbered, and an unknown department is refused
// with the list of real ones.
func TestIntegration_Schedule(t *testing.T) {
	p := integrationPlugin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s, err := p.getSchedule(ctx, scheduleArgs{})
	if err != nil {
		t.Fatalf("get_schedule: %v", err)
	}
	if !s.IsDefault {
		t.Errorf("no department named should resolve to the default one, got %+v", s)
	}
	if s.TimeZone != "" && !strings.Contains(s.TimeZone, "/") && len(s.TimeZone) < 4 {
		t.Errorf("time zone should be a name, not an id: %q", s.TimeZone)
	}
	t.Logf("schedule: %s tz=%s hours=%d holidays=%d forced=%q others=%v",
		s.Department, s.TimeZone, len(s.OfficeHours), len(s.Holidays), s.Forced, s.Departments)

	if _, err := p.getSchedule(ctx, scheduleArgs{Department: "no-such-department"}); err == nil {
		t.Error("an unknown department should be refused")
	}
}

// The support bundle, end to end against the live system: started, polled to
// completion, and read as a digest that names the system and carries no zip.
func TestIntegration_SupportBundle(t *testing.T) {
	p := integrationPlugin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	st, err := p.startBundle(ctx, startBundleArgs{})
	if err != nil {
		t.Fatalf("aggregate_support_bundle: %v", err)
	}
	if st.State != "running" {
		t.Fatalf("expected a running job, got %+v", st)
	}
	var r BundleReport
	for {
		r, err = p.bundleReport(ctx, bundleReportArgs{})
		if err != nil {
			t.Fatal(err)
		}
		if r.State != "running" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("the capture did not finish in time")
		case <-time.After(2 * time.Second):
		}
	}
	if r.State != "done" {
		t.Fatalf("capture: %+v", r)
	}
	if r.System == nil || r.System.Version == "" {
		t.Errorf("the digest should name the system version: %+v", r.System)
	}
	t.Logf("bundle: version %s, %d findings, %d health checks, %d event groups, %d edits",
		r.System.Version, r.Counts.Findings, r.Counts.Health, r.Counts.Events, r.Counts.Changes)
	for _, f := range r.Findings {
		t.Logf("  [%s] %s", f.Severity, f.Title)
	}
	files, err := p.bundleReport(ctx, bundleReportArgs{Section: "files"})
	if err != nil || len(files.Files) == 0 {
		t.Errorf("the files section should list what was read: %+v %v", files.Files, err)
	}
}
