package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validConfig() *Config {
	c := Default()
	c.Server.PublicURL = "https://mcp.example.net"
	c.Storage.Path = "/var/lib/mcpd/mcpd.db"
	c.Plugins = map[string]PluginConfig{"cnmaestro": {Enabled: true}}
	c.Auth.StaticTokens = []StaticTokenConfig{{
		ID: "agent-a", SecretRef: "env:MCPD_TOKEN_A",
		Principal: "svc:agent-a", Role: "user", Plugins: []string{"cnmaestro"},
	}}
	return c
}

func TestValidate_AcceptsValidConfig(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestValidate_Rejects(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantSub string
	}{
		{"relative storage path", func(c *Config) { c.Storage.Path = "mcpd.db" }, "must be absolute"},
		{"token without secret ref", func(c *Config) { c.Auth.StaticTokens[0].SecretRef = "" }, "secret_ref is required"},
		{"token with no plugins", func(c *Config) { c.Auth.StaticTokens[0].Plugins = nil }, "plugins is empty"},
		{"token granting unknown plugin", func(c *Config) { c.Auth.StaticTokens[0].Plugins = []string{"ghost"} }, "not configured"},
		{"bad role", func(c *Config) { c.Auth.StaticTokens[0].Role = "wizard" }, "role must be one of"},
		{"required but disabled plugin", func(c *Config) {
			c.Plugins["cnmaestro"] = PluginConfig{Enabled: false, Required: true}
		}, "required but not enabled"},
		{"approval outliving proposal", func(c *Config) {
			c.Approval.ApprovalTTL = c.Approval.ProposalTTL * 2
		}, "outlive"},
		{"negative session ttl", func(c *Config) {
			c.Auth.Accounts.SessionTTL = -time.Minute
		}, "session_ttl"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := validConfig()
			tc.mutate(c)
			err := c.Validate()
			if err == nil {
				t.Fatal("expected validation to fail")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q does not mention %q", err, tc.wantSub)
			}
		})
	}
}

// Two credentials sharing one secret are the same credential with two names,
// which quietly breaks revocation and audit attribution.
func TestValidate_RejectsSharedSecretReference(t *testing.T) {
	c := validConfig()
	c.Auth.StaticTokens = append(c.Auth.StaticTokens, StaticTokenConfig{
		ID: "agent-b", SecretRef: "env:MCPD_TOKEN_A",
		Principal: "svc:agent-b", Role: "user", Plugins: []string{"cnmaestro"},
	})
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "share secret_ref") {
		t.Fatalf("expected a shared-secret error, got %v", err)
	}
}
func TestWarnings_FlagsRelaxedDurability(t *testing.T) {
	c := validConfig()
	c.Storage.RelaxedDurability = true
	if err := c.Validate(); err != nil {
		t.Fatalf("relaxed durability should warn, not fail: %v", err)
	}
	if len(c.Warnings()) == 0 {
		t.Fatal("expected a warning about relaxed durability")
	}
}

func TestSecretResolver(t *testing.T) {
	dir := t.TempDir()
	credFile := filepath.Join(dir, "agent-a")
	if err := os.WriteFile(credFile, []byte("s3cret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREDENTIALS_DIRECTORY", dir)
	t.Setenv("MCPD_TEST_TOKEN", "from-env")
	r := NewSecretResolver()

	tests := []struct {
		name    string
		ref     string
		want    string
		wantErr string
	}{
		{"env", "env:MCPD_TEST_TOKEN", "from-env", ""},
		{"credential trims newline", "credential:agent-a", "s3cret-value", ""},
		{"file", "file:" + credFile, "s3cret-value", ""},
		{"missing env", "env:MCPD_NOT_SET", "", "is not set"},
		{"no scheme", "just-a-token", "", "no scheme"},
		{"unknown scheme", "vault:thing", "", "unknown secret scheme"},
		{"empty name", "env:", "", "names nothing"},
		{"traversal blocked", "credential:../../etc/passwd", "", "must not contain a path"},
		{"relative file blocked", "file:relative/path", "", "must be an absolute path"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.Resolve(tc.ref)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Resolve(%q) error = %v, want mention of %q", tc.ref, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve(%q): %v", tc.ref, err)
			}
			if got != tc.want {
				t.Fatalf("Resolve(%q) = %q, want %q", tc.ref, got, tc.want)
			}
		})
	}
}

// An error resolving a secret must never quote the secret itself.
func TestSecretResolver_ErrorsDoNotLeakValues(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewSecretResolver()
	_, err := r.Resolve("file:" + empty)
	if err == nil {
		t.Fatal("expected an error for an empty secret file")
	}
	t.Setenv("MCPD_LEAK_TEST", "super-secret-value")
	_, err = r.Resolve("env:MCPD_LEAK_TEST_MISSING")
	if err != nil && strings.Contains(err.Error(), "super-secret-value") {
		t.Fatal("resolver error leaked a secret value")
	}
}

func TestEnvOverrides(t *testing.T) {
	t.Setenv("MCPD_LISTEN", "0.0.0.0:9000")
	t.Setenv("MCPD_FRONTEND_LISTEN", ":8081")
	t.Setenv("MCPD_LOG_LEVEL", "debug")
	t.Setenv("MCPD_PLUGIN_CNMAESTRO_ENABLED", "true")

	c := validConfig()
	c.Plugins["cnmaestro"] = PluginConfig{Enabled: false}
	if err := c.applyEnvOverrides(); err != nil {
		t.Fatal(err)
	}

	if c.Server.Listen != "0.0.0.0:9000" {
		t.Fatalf("listen = %q", c.Server.Listen)
	}
	if c.Server.FrontendListen != ":8081" {
		t.Fatalf("frontend_listen = %q", c.Server.FrontendListen)
	}
	if c.Logging.Level != "debug" {
		t.Fatalf("log level = %q", c.Logging.Level)
	}
	if !c.Plugins["cnmaestro"].Enabled {
		t.Fatal("plugin enablement was not overridden")
	}
}

// Silently treating a typo as false would disable something with no
// indication why.
func TestEnvOverrides_RejectMalformedBoolean(t *testing.T) {
	t.Setenv("MCPD_FRONTEND_ENABLED", "ture")

	c := validConfig()
	err := c.applyEnvOverrides()
	if err == nil {
		t.Fatal("a malformed boolean must be reported, not defaulted")
	}
	if !strings.Contains(err.Error(), "not a boolean") {
		t.Fatalf("error should explain the problem, got %v", err)
	}
}

// An unset variable must leave the file's value alone.
func TestEnvOverrides_UnsetLeavesFileValues(t *testing.T) {
	c := validConfig()
	original := c.Server.Listen
	if err := c.applyEnvOverrides(); err != nil {
		t.Fatal(err)
	}
	if c.Server.Listen != original {
		t.Fatalf("listen changed to %q with no override set", c.Server.Listen)
	}
}

// Plaintext is refused for a public address but permitted on a private one:
// that is where development happens, and the traffic does not cross a network
// the operator does not control.
func TestValidate_PlaintextPublicURL(t *testing.T) {
	tests := []struct {
		url   string
		valid bool
	}{
		{"https://mcp.example.net", true},
		{"http://localhost:9090", true},
		{"http://127.0.0.1:9090", true},
		{"http://192.168.50.125:9090", true},
		{"http://10.0.0.5:9090", true},
		{"http://172.16.4.1:9090", true},
		{"http://[::1]:9090", true},
		{"http://mcpd.local:9090", true},
		{"http://mcpd.internal:9090", true},
		// Publicly routable plaintext hands the token to anything on the path.
		{"http://mcp.example.net", false},
		{"http://8.8.8.8:9090", false},
		{"ftp://mcp.example.net", false},
	}
	for _, tc := range tests {
		t.Run(tc.url, func(t *testing.T) {
			c := validConfig()
			c.Server.PublicURL = tc.url
			err := c.Validate()
			if tc.valid && err != nil {
				t.Fatalf("%s should be accepted: %v", tc.url, err)
			}
			if !tc.valid && err == nil {
				t.Fatalf("%s should be refused", tc.url)
			}
		})
	}
}

// A plaintext LAN address is allowed, but the operator must be told the token
// is in the clear.
func TestWarnings_FlagsPlaintextOnANetwork(t *testing.T) {
	c := validConfig()
	c.Server.PublicURL = "http://192.168.50.125:9090"

	var found bool
	for _, w := range c.Warnings() {
		if strings.Contains(w, "in the clear") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a plaintext warning, got %v", c.Warnings())
	}

	// Loopback never leaves the machine, so it needs no warning.
	c.Server.PublicURL = "http://127.0.0.1:9090"
	for _, w := range c.Warnings() {
		if strings.Contains(w, "in the clear") {
			t.Fatal("loopback should not produce a plaintext warning")
		}
	}
}

// Loopback never leaves the machine, by name as well as by address, so it
// needs no plaintext warning.
func TestWarnings_LoopbackNamesDoNotWarn(t *testing.T) {
	for _, host := range []string{
		"http://localhost:9090", "http://127.0.0.1:9090", "http://[::1]:9090",
	} {
		c := validConfig()
		c.Server.PublicURL = host
		for _, w := range c.Warnings() {
			if strings.Contains(w, "in the clear") {
				t.Errorf("%s should not warn about plaintext: %s", host, w)
			}
		}
	}
}
