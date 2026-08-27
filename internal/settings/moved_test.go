package settings

import (
	"strings"
	"testing"
	"time"
)

// A field declares its own default and its own unit. The dashboard reads them
// and so does the host, and the whole point of declaring them once is that the
// two cannot disagree -- so the readers have to actually read them.
func TestFieldDefaultsAreReadFromTheDeclaration(t *testing.T) {
	for _, tc := range []struct {
		key  string
		want time.Duration
	}{
		{KeyServerReadHeaderTimeout, 10 * time.Second},
		{KeyServerReadTimeout, 60 * time.Second},
		{KeyServerWriteTimeout, 120 * time.Second},
		{KeyServerIdleTimeout, 120 * time.Second},
		{KeyServerShutdownTimeout, 30 * time.Second},
		{KeyStorageBusyTimeout, 5 * time.Second},
		{KeyAccountsSessionTTL, 12 * time.Hour},
		{KeyApprovalProposalTTL, 30 * time.Minute},
		{KeyApprovalApprovalTTL, 15 * time.Minute},
		{KeyApprovalLeaseTTL, 2 * time.Minute},
	} {
		if got := DefaultDuration(tc.key); got != tc.want {
			t.Errorf("%s default = %s, want %s", tc.key, got, tc.want)
		}
	}

	if got := DefaultString(KeyServerTLSMode); got != "off" {
		t.Errorf("tls mode default = %q, want off", got)
	}
	if got := DefaultString(KeyApprovalInlineMaxRisk); got != "medium" {
		t.Errorf("inline ceiling default = %q; moving this setting must not have "+
			"changed what a deployment does", got)
	}
	if !DefaultBool(KeyServerFrontendEnabled) {
		t.Error("the dashboard must still be on by default")
	}
	if DefaultBool(KeyStorageRelaxedDurability) {
		t.Error("relaxed durability must stay off by default")
	}
}

// A duration counted in the wrong unit is the mistake this guards: 30 minutes
// read as 30 seconds is an approval window that expires while somebody reads
// the change.
func TestEveryDurationFieldDeclaresItsUnit(t *testing.T) {
	for _, g := range Schema() {
		for _, f := range g.Fields {
			if f.Kind != KindDuration {
				continue
			}
			switch f.Unit {
			case UnitSeconds, UnitMinutes, UnitHours:
			default:
				t.Errorf("%s is a duration with unit %q; say what it counts in",
					f.Key, f.Unit)
			}
		}
	}
}

// The values that were once in config.yaml are declared here now, and a key
// that is stored but not declared is one the dashboard cannot show and
// validation cannot check.
func TestTheMovedKeysAreDeclaredFields(t *testing.T) {
	for _, key := range []string{
		KeyServerPublicURL, KeyServerFrontendPublicURL, KeyServerTLSMode,
		KeyServerFrontendEnabled, KeyServerReadHeaderTimeout, KeyServerReadTimeout,
		KeyServerWriteTimeout, KeyServerIdleTimeout, KeyServerShutdownTimeout,
		KeyStorageBusyTimeout, KeyStorageRelaxedDurability, KeyAccountsSessionTTL,
		KeyApprovalInlineMaxRisk, KeyLoggingLevel, KeyLoggingFormat,
		KeyTunnelDiagnostics,
	} {
		if _, ok := FieldFor(key); !ok {
			t.Errorf("%s is not declared, so nothing can render or validate it", key)
		}
	}
}

// Every group is on a page that exists.
//
// A section is how a group says where it renders, and the dashboard renders
// what it is told -- so a group naming a section no page reads is a setting
// that is declared, validated, stored, and invisible. That failure is silent
// in both directions: nothing errors, and the field simply never appears.
//
// The set is checked rather than each group's own section, deliberately.
// Pinning a group to a section makes this fail every time the pages are
// rearranged, which is a test that objects to tidying rather than one that
// catches a mistake. What must hold is that the section is a real one.
func TestEveryGroupIsOnAPageThatExists(t *testing.T) {
	pages := map[string]bool{
		SectionSettings:       true,
		SectionPlugins:        true,
		SectionTunnels:        true,
		SectionAuthentication: true,
		SectionChatGPT:        true,
		SectionApprovals:      true,
		SectionAdvanced:       true,
		SectionDiagnostics:    true,
	}
	for _, g := range Schema() {
		if g.Section == "" {
			t.Errorf("group %q names no section, so nothing renders it", g.Name)
			continue
		}
		if !pages[g.Section] {
			t.Errorf("group %q is on section %q, which no page reads", g.Name, g.Section)
		}
	}
}

// The keys that moved out of config.yaml are still declared and still
// reachable. Where they render is a layout decision; that they render at all
// is not.
func TestTheMovedKeysAreStillReachable(t *testing.T) {
	for _, name := range []string{"server", "timeouts", "storage", "logging", "sessions"} {
		found := false
		for _, g := range Schema() {
			if g.Name == name {
				found = true
				if len(g.Fields) == 0 {
					t.Errorf("group %q has no fields left", name)
				}
				break
			}
		}
		if !found {
			t.Errorf("group %q is missing from the schema", name)
		}
	}
}

// The same validator the dashboard uses is the one the upgrade import runs a
// file value through, so what it refuses is worth pinning.
func TestValidatingTheMovedFields(t *testing.T) {
	for _, tc := range []struct {
		name    string
		key     string
		value   string
		wantSub string
	}{
		{"an address without a scheme", KeyServerPublicURL, "mcp.example.net",
			"http:// or https://"},
		{"an address that is not a URL", KeyServerPublicURL, "://nope", "not a valid URL"},
		{"an empty address is allowed", KeyServerPublicURL, "", ""},
		{"a good address", KeyServerPublicURL, "https://mcp.example.net", ""},
		{"an unknown TLS mode", KeyServerTLSMode, "acme", "must be one of"},
		{"a timeout below the floor", KeyServerReadTimeout, "0", "at least"},
		{"a timeout that is not a number", KeyServerReadTimeout, "60s", "whole number"},
		{"a risk level that does not exist", KeyApprovalInlineMaxRisk, "catastrophic",
			"must be one of"},
		{"the strictest ceiling", KeyApprovalInlineMaxRisk, RiskNone, ""},
		{"a diagnostics address with no port", KeyTunnelDiagnostics, "127.0.0.1",
			"must be host:port"},
		{"a diagnostics address left off", KeyTunnelDiagnostics, "", ""},
		{"an unknown log level", KeyLoggingLevel, "verbose", "must be one of"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.key, tc.value)
			if tc.wantSub == "" {
				if err != nil {
					t.Fatalf("%s = %q was refused: %v", tc.key, tc.value, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("%s = %q was accepted", tc.key, tc.value)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q does not mention %q", err, tc.wantSub)
			}
		})
	}
}

// The four keys that stay in the startup file must not be settings. Storing a
// bind address here is exactly the value that could lock an operator out of
// the page they would correct it on.
func TestTheKeysThatStayInTheFileAreNotSettings(t *testing.T) {
	for _, key := range []string{
		"server.listen", "server.frontend_listen", "storage.path", "secret_key_ref",
	} {
		if _, ok := FieldFor(key); ok {
			t.Errorf("%s is a declared setting; it belongs in the startup file", key)
		}
		if err := Validate(key, "anything"); err == nil {
			t.Errorf("%s was accepted as a setting", key)
		}
	}
}

// One encoder for the dashboard and for the upgrade import, so a value read
// back does not depend on which door it came in through.
func TestEncode(t *testing.T) {
	for _, tc := range []struct {
		kind  Kind
		in    string
		want  string
		wants bool
	}{
		{KindString, "https://x", `"https://x"`, true},
		{KindEnum, "self-signed", `"self-signed"`, true},
		{KindBool, "true", "true", true},
		{KindBool, "yes", "", false},
		{KindDuration, "90", "90", true},
		{KindDuration, "90s", "", false},
		{KindInt, "7", "7", true},
		{KindList, "echo, cnmaestro", `["echo","cnmaestro"]`, true},
		{KindList, "", "[]", true},
	} {
		got, err := Encode(tc.kind, tc.in)
		if tc.wants && err != nil {
			t.Errorf("Encode(%s, %q): %v", tc.kind, tc.in, err)
			continue
		}
		if !tc.wants {
			if err == nil {
				t.Errorf("Encode(%s, %q) = %q, want an error", tc.kind, tc.in, got)
			}
			continue
		}
		if got != tc.want {
			t.Errorf("Encode(%s, %q) = %q, want %q", tc.kind, tc.in, got, tc.want)
		}
	}
}
