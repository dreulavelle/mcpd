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

// Every one of them is administrative to change and readable to see, which is
// what the Settings page already is. A field on a page nobody with read can
// open would be a setting an operator cannot answer questions about.
func TestTheMovedKeysAreOnPagesThatAlreadyExist(t *testing.T) {
	want := map[string]string{
		"server":   SectionSettings,
		"timeouts": SectionSettings,
		"storage":  SectionSettings,
		"logging":  SectionSettings,
		"sessions": SectionAuthentication,
	}
	seen := map[string]bool{}
	for _, g := range Schema() {
		if section, ok := want[g.Name]; ok {
			seen[g.Name] = true
			if g.Section != section {
				t.Errorf("group %q is on section %q, want %q", g.Name, g.Section, section)
			}
		}
	}
	for name := range want {
		if !seen[name] {
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
