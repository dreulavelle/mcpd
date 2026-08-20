package cnmaestro

import (
	"fmt"
	"regexp"
	"strings"
)

// blockedPaths are cnMaestro endpoints mcpd will never call, whatever a
// mutation or tool asks for.
//
// The first two are the reason this list is enforced in code rather than by
// review. Both are arbitrary remote command execution on network
// infrastructure, reachable with the same account-wide token as every read:
//
//	POST /devices/{mac}/cli                     "Initiate execution of CLI command"
//	POST /cnwave60/devices/{mac}/remote_command  the 60 GHz equivalent
//
// The second one did not exist in API 5.0.1 and appeared in 6.3.0. A deny-list
// that only lives in a design document does not survive that kind of change,
// so this one is checked on every request and covered by a test that fails if
// a registered mutation resolves to a blocked path.
//
// The remaining entries are diagnostic endpoints that execute something on the
// device. They are individually low-risk and collectively an unbounded
// side-channel, and none of them is needed to manage a network.
var blockedPaths = []*regexp.Regexp{
	// Arbitrary command execution.
	regexp.MustCompile(`^/devices/[^/]+/cli$`),
	regexp.MustCompile(`^/cnwave60/devices/[^/]+/remote_command(/.*)?$`),

	// Device-executed diagnostics.
	regexp.MustCompile(`^/devices/[^/]+/ping$`),
	regexp.MustCompile(`^/devices/[^/]+/traceroute$`),
	regexp.MustCompile(`^/devices/[^/]+/pull_config$`),
	regexp.MustCompile(`^/devices/[^/]+/wifi_perf$`),
	regexp.MustCompile(`^/cnwave60/devices/[^/]+/ping$`),
	regexp.MustCompile(`^/cnwave60/devices/[^/]+/iperf$`),
	regexp.MustCompile(`^/cnwave60/devices/[^/]+/links/[^/]+/iperf$`),
	regexp.MustCompile(`^/cnwave60/devices/[^/]+/topology_scan$`),
}

// ErrPathBlocked reports an attempt to call a permanently denied endpoint.
type ErrPathBlocked struct {
	Path string
}

func (e *ErrPathBlocked) Error() string {
	return fmt.Sprintf("cnmaestro: %s is on the permanent deny-list and will never be called", e.Path)
}

// checkPath reports whether a request path is permitted.
//
// The path is normalised before matching so that a caller cannot slip past the
// patterns with a trailing slash, a duplicated separator, or an encoded one.
func checkPath(path string) error {
	norm := normalizePath(path)
	for _, re := range blockedPaths {
		if re.MatchString(norm) {
			return &ErrPathBlocked{Path: path}
		}
	}
	return nil
}

// normalizePath reduces a path to the form the deny-list patterns expect:
// leading slash, no API prefix, no duplicate separators, no trailing slash,
// lowercased.
func normalizePath(path string) string {
	// Strip any query or fragment; the deny-list matches on path alone.
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	path = strings.ToLower(path)

	// Decode the one escape that matters here. A caller writing %2f instead of
	// / would otherwise sail past every pattern.
	path = strings.ReplaceAll(path, "%2f", "/")

	path = strings.TrimPrefix(path, "/api/v2")
	path = strings.TrimPrefix(path, "/api/v1")

	// Collapse repeated separators: //devices//x//cli must not evade the
	// single-slash patterns.
	for strings.Contains(path, "//") {
		path = strings.ReplaceAll(path, "//", "/")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if len(path) > 1 {
		path = strings.TrimSuffix(path, "/")
	}
	return path
}
