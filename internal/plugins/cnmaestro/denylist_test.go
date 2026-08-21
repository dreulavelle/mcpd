package cnmaestro

import (
	"strings"
	"testing"
)

// The deny-list is the plugin's one hard guarantee, so it is tested against
// the spellings a path could arrive in rather than only the canonical one.
func TestCheckPath_RefusesCommandExecution(t *testing.T) {
	blocked := []string{
		"/devices/AA:BB:CC:DD:EE:FF/cli",
		"/cnwave60/devices/AA:BB:CC:DD:EE:FF/remote_command",
		"/cnwave60/devices/AA:BB:CC:DD:EE:FF/remote_command/status",
		"/devices/AA:BB:CC:DD:EE:FF/ping",
		"/devices/AA:BB:CC:DD:EE:FF/traceroute",
		"/devices/AA:BB:CC:DD:EE:FF/pull_config",
		"/devices/AA:BB:CC:DD:EE:FF/wifi_perf",
		"/devices/AA:BB:CC:DD:EE:FF/network_info",
		"/devices/AA:BB:CC:DD:EE:FF/network_info/wan",
		"/devices/AA:BB:CC:DD:EE:FF/reboot",
		"/devices/AA:BB:CC:DD:EE:FF/disconnect_clients",
		"/cnwave60/devices/AA:BB:CC:DD:EE:FF/topology_scan",
		"/cnwave60/devices/AA:BB:CC:DD:EE:FF/links/link-1/iperf",
	}
	for _, path := range blocked {
		if err := checkPath(path); err == nil {
			t.Errorf("%s must be refused", path)
		}
	}
}

// A blocked path reached by a different spelling is the same path. Anchored
// patterns are exactly what a trailing slash or a doubled separator slips
// past, so normalisation happens before matching.
func TestCheckPath_NormalisesBeforeMatching(t *testing.T) {
	evasions := []string{
		"devices/AA:BB:CC:DD:EE:FF/cli",   // no leading slash
		"/devices/AA:BB:CC:DD:EE:FF/cli/", // trailing slash
		"//devices/AA:BB:CC:DD:EE:FF/cli", // doubled separator
		"/devices//AA:BB:CC:DD:EE:FF//cli",
		"  /devices/AA:BB:CC:DD:EE:FF/cli  ", // surrounding space
		"/devices/AA:BB:CC:DD:EE:FF/cli?x=1", // query string
		"/devices/AA:BB:CC:DD:EE:FF/cli#frag",
	}
	for _, path := range evasions {
		if err := checkPath(path); err == nil {
			t.Errorf("%q reaches a blocked endpoint and must be refused", path)
		}
	}
}

// The read endpoints this plugin exists to call must not be caught by it.
func TestCheckPath_AllowsReads(t *testing.T) {
	allowed := []string{
		"/devices",
		"/devices/AA:BB:CC:DD:EE:FF",
		"/networks",
		"/networks/Main/sites",
		"/alarms",
		"/events",
		"/devices/AA:BB:CC:DD:EE:FF/performance",
		"/devices/AA:BB:CC:DD:EE:FF/statistics",
	}
	for _, path := range allowed {
		if err := checkPath(path); err != nil {
			t.Errorf("%s is an ordinary read and must be allowed: %v", path, err)
		}
	}
}

// The refusal has to say why, because a model that reads "not found" will try
// something else, and one that reads this will not.
func TestCheckPath_ExplainsItself(t *testing.T) {
	err := checkPath("/devices/AA:BB:CC:DD:EE:FF/cli")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "deny-list") {
		t.Errorf("error = %q, want it to name the deny-list", err)
	}
}
