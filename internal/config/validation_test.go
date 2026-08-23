package config

import (
	"strings"
	"testing"

	"github.com/spoked/mcpd/internal/auth/apikeys"
)

// A key created in the dashboard carries a generated id beginning "key_", and
// both kinds of credential land in the same TokenID field and the same audit
// column. Identifiers are generated on one side, so the only way the two
// namespaces could meet is an operator choosing one -- which is refused here,
// where they can read the reason, rather than being resolved by whichever
// verifier happened to answer first.
func TestAStaticTokenCannotTakeAKeyIdentifier(t *testing.T) {
	cfg := validConfig()
	cfg.Auth.StaticTokens[0].ID = "key_deadbeef"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("a file token wearing a key's identifier must be refused")
	}
	if !strings.Contains(err.Error(), "reserved for keys") {
		t.Fatalf("error = %v; it must say why the id is refused", err)
	}
}

// The prefix is spelled out in two packages, so it is held together by a test
// rather than by memory.
func TestTheReservedPrefixMatchesTheOneKeysUse(t *testing.T) {
	if reservedTokenIDPrefix != apikeys.IDPrefix {
		t.Fatalf("config reserves %q but keys are issued with %q",
			reservedTokenIDPrefix, apikeys.IDPrefix)
	}
}

// Validation says nothing about the keys that moved, and that is deliberate
// rather than an oversight: they are not configuration any more, so a file
// still carrying one must not stop the host from starting. What checks them is
// the settings schema, at the moment the import offers them to it.
func TestValidateIsSilentAboutTheKeysThatMoved(t *testing.T) {
	cfg := validConfig()
	legacy, err := parseLegacy([]byte(`
server:
  public_url: "ftp://nonsense"
  read_timeout: 9999h
tunnel:
  role: approver
approval:
  inline_max_risk: catastrophic
`))
	if err != nil {
		t.Fatal(err)
	}
	cfg.legacy = legacy

	if err := cfg.Validate(); err != nil {
		t.Fatalf("a file carrying moved keys must still validate: %v", err)
	}
}
