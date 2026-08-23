package config

import (
	"strings"
	"testing"

	"github.com/spoked/mcpd/internal/auth/apikeys"
)

// Plaintext on a private network is a warning, not an error. The rule that
// made it an error existed for the OAuth issuer contract -- RFC 8414 requires
// an https issuer -- and mcpd is no longer an authorization server.
func TestPlaintextPublicURLIsAccepted(t *testing.T) {
	cfg := validConfig()
	cfg.Server.PublicURL = "http://192.168.50.125:9080"

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// Serving https while advertising http would send every client to a port that
// cannot answer them.
func TestATLSListenerNeedsAnHTTPSPublicURL(t *testing.T) {
	cfg := validConfig()
	cfg.Server.TLS = TLS{Mode: "self-signed"}
	cfg.Server.PublicURL = "http://192.168.50.125:9080"

	if err := cfg.Validate(); err == nil {
		t.Fatal("an https listener advertised as http must be refused")
	}
}

// A stale tunnel.role in the file has to be caught by -check, not at connect
// time.
//
// The tunnel is normally turned on from the dashboard, so a file saying
// enabled: false still supplies the identity every tunnel runs with. Gating
// this check on the file's own enabled flag let "approver" survive the
// collapse to two roles, pass validation, and then fail every tunnel with
// `unknown role "approver"` several seconds into startup.
func TestTunnelRoleIsCheckedEvenWhenTheFileDoesNotEnableTheTunnel(t *testing.T) {
	cfg := validConfig()
	cfg.Tunnel.Enabled = false
	cfg.Tunnel.Role = "approver"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("a role that no longer exists must be refused")
	}
	if !strings.Contains(err.Error(), "tunnel.role") {
		t.Fatalf("error = %v, want it to name tunnel.role", err)
	}
}

// Empty is legitimate: it means "use the default", and the default is a real
// role.
func TestAnUnsetTunnelRoleIsAccepted(t *testing.T) {
	cfg := validConfig()
	cfg.Tunnel.Enabled = false
	cfg.Tunnel.Role = ""

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// Tunnels are made and assigned in the dashboard, so a file that enables the
// tunnel without naming one is ordinary rather than an error.
func TestEnablingTheTunnelWithoutAnIDIsAllowed(t *testing.T) {
	cfg := validConfig()
	cfg.Tunnel.Enabled = true
	cfg.Tunnel.TunnelID = ""
	cfg.Tunnel.APIKeyRef = "env:OPENAI_TUNNEL_API_KEY"
	cfg.Tunnel.Principal = "svc:chatgpt"
	cfg.Tunnel.Role = "user"
	cfg.Tunnel.Plugins = []string{"*"}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

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
