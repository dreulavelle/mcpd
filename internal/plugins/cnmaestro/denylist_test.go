package cnmaestro

import (
	"errors"
	"testing"
)

func TestCheckPath_BlocksRemoteExecution(t *testing.T) {
	blocked := []string{
		// The two that matter most: arbitrary command execution.
		"/devices/AA:BB:CC:DD:EE:FF/cli",
		"/cnwave60/devices/AA:BB:CC:DD:EE:FF/remote_command",
		"/cnwave60/devices/AA:BB:CC:DD:EE:FF/remote_command/list_commands",

		// Device-executed diagnostics.
		"/devices/AA:BB:CC:DD:EE:FF/ping",
		"/devices/AA:BB:CC:DD:EE:FF/traceroute",
		"/devices/AA:BB:CC:DD:EE:FF/pull_config",
		"/devices/AA:BB:CC:DD:EE:FF/wifi_perf",
		"/cnwave60/devices/AA:BB:CC:DD:EE:FF/iperf",
		"/cnwave60/devices/AA:BB:CC:DD:EE:FF/topology_scan",
	}
	for _, p := range blocked {
		t.Run(p, func(t *testing.T) {
			err := checkPath(p)
			if err == nil {
				t.Fatalf("%s must be blocked", p)
			}
			var blockedErr *ErrPathBlocked
			if !errors.As(err, &blockedErr) {
				t.Fatalf("want *ErrPathBlocked, got %T", err)
			}
		})
	}
}

// A deny-list that can be walked around is decoration. Each of these is a
// spelling of a blocked path that a naive string comparison would miss.
func TestCheckPath_ResistsEvasion(t *testing.T) {
	evasions := []struct {
		name string
		path string
	}{
		{"api prefix", "/api/v2/devices/AA:BB/cli"},
		{"v1 prefix", "/api/v1/devices/AA:BB/cli"},
		{"trailing slash", "/devices/AA:BB/cli/"},
		{"duplicate separators", "//devices//AA:BB//cli"},
		{"uppercase", "/DEVICES/AA:BB/CLI"},
		{"mixed case", "/Devices/AA:BB/Cli"},
		{"encoded separator", "/devices/AA:BB%2Fcli"},
		{"query string appended", "/devices/AA:BB/cli?foo=bar"},
		{"fragment appended", "/devices/AA:BB/cli#x"},
		{"no leading slash", "devices/AA:BB/cli"},
	}
	for _, tc := range evasions {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkPath(tc.path); err == nil {
				t.Fatalf("%q evaded the deny-list (normalised to %q)",
					tc.path, normalizePath(tc.path))
			}
		})
	}
}

func TestCheckPath_AllowsLegitimateEndpoints(t *testing.T) {
	allowed := []string{
		"/devices",
		"/devices/AA:BB:CC:DD:EE:FF",
		"/devices/AA:BB:CC:DD:EE:FF/reboot",
		"/devices/AA:BB:CC:DD:EE:FF/statistics",
		"/devices/AA:BB:CC:DD:EE:FF/performance",
		"/devices/AA:BB:CC:DD:EE:FF/clients",
		"/devices/AA:BB:CC:DD:EE:FF/disconnect_clients",
		"/devices/statistics",
		"/devices/clients",
		"/alarms",
		"/events",
		"/networks",
		"/jobs",
		"/wifi_enterprise/ap_groups",
		"/configuration/commit",
		"/api/v2/devices",
		// Endpoints whose names merely resemble blocked ones must pass.
		"/devices/AA:BB/clients",
		"/devices/AA:BB/network_info",
	}
	for _, p := range allowed {
		t.Run(p, func(t *testing.T) {
			if err := checkPath(p); err != nil {
				t.Fatalf("%s should be permitted: %v", p, err)
			}
		})
	}
}

func TestNormalizePath(t *testing.T) {
	tests := []struct{ in, want string }{
		{"/api/v2/devices", "/devices"},
		{"/devices/", "/devices"},
		{"//devices//x", "/devices/x"},
		{"devices", "/devices"},
		{"/DEVICES", "/devices"},
		{"/devices?limit=10", "/devices"},
		{"/", "/"},
	}
	for _, tc := range tests {
		if got := normalizePath(tc.in); got != tc.want {
			t.Errorf("normalizePath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
