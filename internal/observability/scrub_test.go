package observability

import (
	"strings"
	"testing"
)

// A crash report is the one thing mcpd sends to a server the operator does not
// control. Every string in this table is one this project actually produces --
// the error messages were written for somebody reading their own logs, where
// naming the upstream is helpful and correct.
func TestScrub(t *testing.T) {
	for _, tc := range []struct {
		name     string
		in       string
		mustGo   []string
		mustStay []string
	}{
		{
			name:     "an upstream address",
			in:       "observium: could not reach https://observium.acme-hospital.internal/api/v0/devices",
			mustGo:   []string{"acme-hospital", "observium.acme-hospital.internal"},
			mustStay: []string{"could not reach", "https://"},
		},
		{
			name:     "credentials in a URL",
			in:       `Get "https://admin:hunter2@obs.example.com/api": dial failed`,
			mustGo:   []string{"hunter2", "admin:hunter2", "obs.example.com"},
			mustStay: []string{"dial failed"},
		},
		{
			name:     "a token in a query string",
			in:       "graph.php?type=port_bits&glitchtip_key=1c91477990f54924912306681fc99972",
			mustGo:   []string{"1c91477990f54924912306681fc99972"},
			mustStay: []string{"graph.php"},
		},
		{
			name:     "a bearer token on its own",
			in:       "the API rejected token hnvKb3ygMNubJ24SYHaEWlUv4pVA",
			mustGo:   []string{"hnvKb3ygMNubJ24SYHaEWlUv4pVA"},
			mustStay: []string{"the API rejected token"},
		},
		{
			name:     "a database host and port",
			in:       "observium: cannot reach the database at 192.168.50.101:3306",
			mustGo:   []string{"192.168.50.101", "3306"},
			mustStay: []string{"cannot reach the database"},
		},
		{
			name:     "an IPv6 address",
			in:       "device unreachable at 2001:0db8:85a3:0000:0000:8a2e:0370:7334",
			mustGo:   []string{"2001:0db8", "8a2e"},
			mustStay: []string{"device unreachable"},
		},
		{
			name:     "a MAC address naming one device",
			in:       "cnmaestro: device 00:1A:2B:3C:4D:5E is offline",
			mustGo:   []string{"00:1A:2B:3C:4D:5E"},
			mustStay: []string{"is offline"},
		},
		{
			name:     "an operator's email",
			in:       "approval denied for principal user:alice@acme-hospital.com",
			mustGo:   []string{"alice@acme-hospital.com", "alice"},
			mustStay: []string{"approval denied"},
		},
		{
			name:   "a device hostname with no scheme",
			in:     "no device core-sw-01.dc2.acme.internal, or this account cannot read it",
			mustGo: []string{"core-sw-01.dc2.acme.internal", "acme"},
			// The sentence is what says which code path produced this.
			mustStay: []string{"cannot read it"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Scrub(tc.in)
			for _, gone := range tc.mustGo {
				if strings.Contains(got, gone) {
					t.Errorf("%q survived scrubbing:\n  in:  %s\n  out: %s", gone, tc.in, got)
				}
			}
			for _, stays := range tc.mustStay {
				if !strings.Contains(got, stays) {
					t.Errorf("%q was removed, and it is what makes the report "+
						"actionable:\n  in:  %s\n  out: %s", stays, tc.in, got)
				}
			}
		})
	}
}

// Redacting the customer's estate is the point. Redacting our own source tree
// is a bug: a stack trace with its identifiers removed is a report nobody can
// act on, and Go import paths are the same shape as domain names.
func TestScrub_LeavesOurOwnIdentifiersAlone(t *testing.T) {
	for _, keep := range []string{
		"github.com/spoked/mcpd/internal/plugins/observium",
		"internal/plugins/observium/mysql.go:214",
		"/usr/local/go/src/runtime/panic.go",
		"config.yaml",
		"sentry.Init",
		"observium: reading eventlog from the database cannot filter by",
	} {
		if got := Scrub(keep); got != keep {
			t.Errorf("scrubbing changed something it should not have:\n  in:  %s\n  out: %s",
				keep, got)
		}
	}
}

// A real stack frame, whole, because the interaction between the rules is
// where this would break rather than in any one of them.
func TestScrub_AStackFrame(t *testing.T) {
	in := `panic: observium: could not reach https://obs.acme.internal:8080/api/v0/devices?pagesize=250

goroutine 42 [running]:
github.com/spoked/mcpd/internal/plugins/observium.(*Client).walk(0xc000123456)
	/build/internal/plugins/observium/client.go:214 +0x1a4`

	got := Scrub(in)
	for _, gone := range []string{"obs.acme.internal", "acme", "pagesize=250"} {
		if strings.Contains(got, gone) {
			t.Errorf("%q survived:\n%s", gone, got)
		}
	}
	for _, stays := range []string{
		"could not reach",
		"internal/plugins/observium/client.go:214",
		"observium.(*Client).walk",
		"goroutine 42",
	} {
		if !strings.Contains(got, stays) {
			t.Errorf("%q was removed, and it is the whole value of the report:\n%s", stays, got)
		}
	}
}

func TestScrub_Empty(t *testing.T) {
	if Scrub("") != "" {
		t.Error("scrubbing an empty string produced something")
	}
}
