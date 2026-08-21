package cnmaestro

import (
	"fmt"
	"regexp"
	"strings"
)

// blockedPaths are cnMaestro endpoints mcpd will never call, whatever a tool
// or a future mutation asks for.
//
// The first two are the reason this is enforced in code rather than by review.
// Both are arbitrary remote command execution on network infrastructure,
// reachable with the same account-wide token as every read:
//
//	POST /devices/{mac}/cli                       "Initiate execution of CLI command"
//	POST /cnwave60/devices/{mac}/remote_command   the 60 GHz equivalent
//
// The second did not exist in API 5.0.1 and appeared in 6.3.0. A deny-list
// that only lives in a design document does not survive the API growing a new
// way to run commands, so this one is checked on every request.
//
// The rest are diagnostics that execute something on the device. Individually
// low-risk, collectively an unbounded side-channel, and none of them is needed
// to manage a network.
//
// The integration is read-only, so nothing here is reachable by any tool it
// registers. The list is checked anyway, on the decoded path of every request
// as it leaves: a guarantee that depends on nobody adding the wrong tool is
// not a guarantee, and these endpoints run commands on live infrastructure.
var blockedPaths = []*regexp.Regexp{
	// Arbitrary command execution.
	regexp.MustCompile(`^/devices/[^/]+/cli$`),
	regexp.MustCompile(`^/cnwave60/devices/[^/]+/remote_command(/.*)?$`),

	// Device-executed diagnostics.
	regexp.MustCompile(`^/devices/[^/]+/ping$`),
	regexp.MustCompile(`^/devices/[^/]+/traceroute$`),
	regexp.MustCompile(`^/devices/[^/]+/pull_config$`),
	regexp.MustCompile(`^/devices/[^/]+/wifi_perf$`),
	regexp.MustCompile(`^/devices/[^/]+/network_info(/.*)?$`),
	regexp.MustCompile(`^/cnwave60/devices/[^/]+/ping$`),
	regexp.MustCompile(`^/cnwave60/devices/[^/]+/iperf$`),
	regexp.MustCompile(`^/cnwave60/devices/[^/]+/links/[^/]+/iperf$`),
	regexp.MustCompile(`^/cnwave60/devices/[^/]+/topology_scan$`),

	// Disruptive device actions. Not diagnostics, and not something an
	// assistant should be able to reach even by constructing a path.
	regexp.MustCompile(`^/devices/[^/]+/reboot$`),
	regexp.MustCompile(`^/devices/[^/]+/disconnect_clients$`),
}

// checkPath refuses a blocked endpoint.
//
// It normalises first, so that a path reaching the deny-list by a different
// spelling — a trailing slash, a doubled separator, a missing leading one —
// is compared in the form the patterns are written for.
func checkPath(path string) error {
	normalised := normalisePath(path)
	for _, blocked := range blockedPaths {
		if blocked.MatchString(normalised) {
			return fmt.Errorf(
				"cnmaestro: %s is on mcpd's deny-list and will not be called. "+
					"It executes a command on the device, which is outside what "+
					"this integration does", normalised)
		}
	}
	return nil
}

// normalisePath puts a path into the single form the deny-list matches.
func normalisePath(path string) string {
	p := strings.TrimSpace(path)
	// Query strings and fragments are not part of what is being matched, and
	// leaving them on would let "?x=1" slip a blocked path past an anchored
	// pattern.
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
	}
	return p
}
