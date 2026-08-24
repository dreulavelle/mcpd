package observium

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// blockedPaths are Observium endpoints mcpd will never call, whatever method
// is used and whatever a future mutation asks for.
//
// This integration is read-only today and transport.go refuses every method
// but GET, so nothing here is reachable. The list exists for the day that
// changes. A read-only guard is one line to widen; the endpoints below are the
// ones that must survive it being widened, so they are checked separately and
// on every request rather than being implied by the method check.
//
// What is on it, and why each one:
//
//	DELETE /devices/{id}    removes a device and, with delete_rrd=1, every
//	                        metric ever recorded for it. Monitoring history is
//	                        not reconstructible: there is no upstream to
//	                        re-poll the past from. This is the entry the list
//	                        exists for.
//	PUT    /devices/{id}    rewrites SNMP credentials and poller assignment. A
//	                        device silently stops being polled and nothing
//	                        looks broken until somebody needs the data.
//	POST   /devices/        adds a device, which starts SNMP traffic towards
//	                        an address a model chose.
//	PUT    /alert_checks/   disables an alert checker. The failure mode is an
//	                        estate that reports healthy because nothing is
//	                        checking it any more.
//
// Scheduled maintenance is deliberately absent. Creating and ending a
// maintenance window is reversible, verifiable by reading it back, and the
// obvious first mutation for this plugin -- blocking it here would be blocking
// the thing we intend to add.
var blockedPaths = []*regexp.Regexp{
	regexp.MustCompile(`^/devices/?$`),
	regexp.MustCompile(`^/devices/[^/]+$`),
	regexp.MustCompile(`^/alert_checks/[^/]+$`),
}

// blockedMethods pairs with blockedPaths: these paths are refused only for
// methods that change something. GET /devices/ is the most ordinary read this
// plugin makes, and a deny-list that blocked it would block the plugin.
//
// Written as a set rather than "anything but GET" so that adding a method
// later is a decision someone makes here, in front of this comment.
var blockedMethods = map[string]bool{
	http.MethodPost:   true,
	http.MethodPut:    true,
	http.MethodPatch:  true,
	http.MethodDelete: true,
}

// checkPath refuses a blocked endpoint.
//
// It normalises first, so a path reaching the deny-list by a different
// spelling -- a trailing slash, a doubled separator, a missing leading one --
// is compared in the form the patterns are written for.
func checkPath(method, path string) error {
	if !blockedMethods[strings.ToUpper(method)] {
		return nil
	}
	normalised := normalisePath(path)
	for _, blocked := range blockedPaths {
		if blocked.MatchString(normalised) {
			return fmt.Errorf(
				"observium: %s %s is on mcpd's deny-list and will not be called. "+
					"It edits or removes monitoring itself, and a device deleted "+
					"through the API takes its recorded history with it",
				strings.ToUpper(method), normalised)
		}
	}
	return nil
}

// normalisePath puts a path into the single form the deny-list matches.
func normalisePath(path string) string {
	p := strings.TrimSpace(path)
	// Query strings and fragments are not part of what is matched, and leaving
	// them on would let "?delete_rrd=1" slip a blocked path past an anchored
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
