package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const populatedFile = `
server:
  listen: "127.0.0.1:9080"
  frontend_listen: "127.0.0.1:9090"
  public_url: "https://mcp.example.net"
  frontend_public_url: "https://mcpd.example.net"
  frontend_enabled: true
  tls:
    mode: self-signed
  read_header_timeout: 10s
  read_timeout: 90s
  write_timeout: 120s
  idle_timeout: 120s
  shutdown_timeout: 45s
storage:
  path: /var/lib/mcpd/mcpd.db
  busy_timeout: 12s
  relaxed_durability: true
secret_key_ref: env:MCPD_SECRET_KEY
auth:
  accounts:
    session_ttl: 24h
approval:
  proposal_ttl: 45m
  approval_ttl: 20m
  lease_ttl: 3m
  inline_max_risk: high
logging:
  level: debug
  format: text
tunnel:
  enabled: true
  tunnel_id: "tunnel_0123456789abcdef0123456789abcdef"
  api_key_ref: env:OPENAI_TUNNEL_API_KEY
  principal: svc:chatgpt
  role: user
  plugins: ["echo"]
  check_for_updates: false
  diagnostics_addr: "127.0.0.1:9095"
`

// Presence is the whole question, so the parse has to distinguish a key the
// file sets from one it merely could have.
func TestParseLegacy_ReadsWhatTheFileSets(t *testing.T) {
	l, err := parseLegacy([]byte(populatedFile))
	if err != nil {
		t.Fatal(err)
	}

	if got := *l.Server.PublicURL; got != "https://mcp.example.net" {
		t.Errorf("public_url = %q", got)
	}
	if got := *l.Server.TLS.Mode; got != "self-signed" {
		t.Errorf("tls.mode = %q", got)
	}
	if got := *l.Server.ReadTimeout; got != 90*time.Second {
		t.Errorf("read_timeout = %s", got)
	}
	if got := *l.Storage.BusyTimeout; got != 12*time.Second {
		t.Errorf("busy_timeout = %s", got)
	}
	if got := *l.Storage.RelaxedDurability; !got {
		t.Error("relaxed_durability was not read")
	}
	if got := *l.Auth.Accounts.SessionTTL; got != 24*time.Hour {
		t.Errorf("session_ttl = %s", got)
	}
	if got := *l.Approval.InlineMaxRisk; got != "high" {
		t.Errorf("inline_max_risk = %q", got)
	}
	if got := *l.Logging.Format; got != "text" {
		t.Errorf("logging.format = %q", got)
	}
	if got := *l.Tunnel.Plugins; len(got) != 1 || got[0] != "echo" {
		t.Errorf("tunnel.plugins = %v", got)
	}
	// check_for_updates: false is a value the file sets, and it must not be
	// mistaken for a key the file leaves out. That distinction is the reason
	// every field here is a pointer.
	if l.Tunnel.CheckForUpdates == nil || *l.Tunnel.CheckForUpdates {
		t.Error("an explicit false must be read as a value, not as an absence")
	}

	if l.Server.ShutdownTimeout == nil {
		t.Error("shutdown_timeout was not read")
	}
	for path, source := range l.Sources() {
		if source != SourceFile {
			t.Errorf("%s: source = %q, want the file", path, source)
		}
	}
	if n := len(l.Sources()); n != 26 {
		t.Errorf("the file sets %d moved keys; the sources map has %d", 26, n)
	}
}

// A file that says nothing about a moved key supplies nothing: there is
// neither anything to import nor anything to warn about.
func TestParseLegacy_AnEmptyFileSuppliesNothing(t *testing.T) {
	l, err := parseLegacy([]byte("server:\n  listen: \"127.0.0.1:9080\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if l.Any() {
		t.Fatalf("an empty file supplied %v", l.Sources())
	}
	if l.Server.PublicURL != nil || l.Logging.Level != nil {
		t.Fatal("absent keys must stay nil rather than taking a default")
	}
}

// The MCPD_ overrides that used to apply to these keys still work, and the
// warning that eventually names one has to name the variable rather than a
// file that does not mention it.
func TestParseLegacy_EnvironmentOverridesNameThemselves(t *testing.T) {
	t.Setenv("MCPD_PUBLIC_URL", "https://from-the-environment.example.net")
	t.Setenv("MCPD_LOG_LEVEL", "warn")
	t.Setenv("MCPD_FRONTEND_ENABLED", "false")

	l, err := parseLegacy([]byte("server:\n  public_url: \"https://from-the-file.example.net\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := *l.Server.PublicURL; got != "https://from-the-environment.example.net" {
		t.Errorf("public_url = %q, want the environment to win over the file", got)
	}
	if got := l.Sources()["server.public_url"]; got != "MCPD_PUBLIC_URL" {
		t.Errorf("source = %q, want the variable that set it", got)
	}
	if got := *l.Logging.Level; got != "warn" {
		t.Errorf("logging.level = %q", got)
	}
	if l.Server.FrontendEnabled == nil || *l.Server.FrontendEnabled {
		t.Error("MCPD_FRONTEND_ENABLED=false was not applied")
	}
}

// Load reads both halves out of the same bytes, and the moved half must not
// reach Config: nothing that runs may read a value from the file for a key the
// database owns.
func TestLoad_KeepsTheMovedKeysOutOfConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(populatedFile), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Legacy().Any() {
		t.Fatal("the moved keys were not read for import")
	}
	// What is left is the four that stay, and they still come from the file.
	if cfg.Server.Listen != "127.0.0.1:9080" {
		t.Errorf("listen = %q", cfg.Server.Listen)
	}
	if cfg.Server.FrontendListen != "127.0.0.1:9090" {
		t.Errorf("frontend_listen = %q", cfg.Server.FrontendListen)
	}
	if cfg.Storage.Path != "/var/lib/mcpd/mcpd.db" {
		t.Errorf("storage.path = %q", cfg.Storage.Path)
	}
	if cfg.SecretKeyRef != "env:MCPD_SECRET_KEY" {
		t.Errorf("secret_key_ref = %q", cfg.SecretKeyRef)
	}
}
