package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validConfig() *Config {
	c := Default()
	c.Server.PublicURL = "https://mcp.example.net"
	c.Storage.Path = "/var/lib/mcpd/mcpd.db"
	c.Plugins = map[string]PluginConfig{"cnmaestro": {Enabled: true}}
	c.Auth.StaticTokens = []StaticTokenConfig{{
		ID: "agent-a", SecretRef: "env:MCPD_TOKEN_A",
		Principal: "svc:agent-a", Role: "operator", Plugins: []string{"cnmaestro"},
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
		{"plaintext public url", func(c *Config) { c.Server.PublicURL = "http://mcp.example.net" }, "must use https"},
		{"unknown auth mode", func(c *Config) { c.Auth.Mode = "magic" }, "auth.mode"},
		{"static mode with no tokens", func(c *Config) { c.Auth.StaticTokens = nil }, "no static_tokens"},
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
		{"oauth with no reachable base url", func(c *Config) {
			c.Auth.Mode = "oauth"
			c.Auth.OAuth.Issuer = ""
			c.Server.PublicURL = ""
		}, "auth.oauth.issuer or server.public_url is required"},
		{"bootstrap without password reference", func(c *Config) {
			c.Auth.Mode = "oauth"
			c.Auth.OAuth.Bootstrap.Username = "admin"
		}, "password_ref is required"},
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
		Principal: "svc:agent-b", Role: "viewer", Plugins: []string{"cnmaestro"},
	})
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "share secret_ref") {
		t.Fatalf("expected a shared-secret error, got %v", err)
	}
}

// Static tokens cannot distinguish principals, so a separation-of-duties
// policy will refuse rather than self-approve. Operators must be told.
func TestWarnings_FlagsSeparationOfDutiesUnderStaticAuth(t *testing.T) {
	c := validConfig()
	c.Approval.RequireDistinctApproverAtOrAbove = "high"
	c.Auth.Mode = "static"

	var found bool
	for _, w := range c.Warnings() {
		if strings.Contains(w, "cannot distinguish principals") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a separation-of-duties warning, got %v", c.Warnings())
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
