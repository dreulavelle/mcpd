package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func validConfig() *Config {
	c := Default()
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
		{"token reaching nothing", func(c *Config) {
			c.Auth.StaticTokens[0].Plugins = nil
			c.Auth.StaticTokens[0].Grants = nil
		}, "grants nothing"},
		{"token granting unknown plugin", func(c *Config) { c.Auth.StaticTokens[0].Plugins = []string{"ghost"} }, "not configured"},
		// A role is now a row rather than one of two words, so the file
		// cannot know which names exist; startup resolves it against the
		// store. All validation can say is that one was named at all.
		{"no role", func(c *Config) { c.Auth.StaticTokens[0].Role = "" }, "role is required"},
		{"grant at a level that is not read or write", func(c *Config) {
			c.Auth.StaticTokens[0].Plugins = nil
			c.Auth.StaticTokens[0].Grants = []GrantConfig{{Plugin: "cnmaestro", Level: "decide"}}
		}, "must be read or write"},
		{"grant naming no plugin", func(c *Config) {
			c.Auth.StaticTokens[0].Grants = []GrantConfig{{Plugin: "  ", Level: "read"}}
		}, "names no plugin"},
		{"required but disabled plugin", func(c *Config) {
			c.Plugins["cnmaestro"] = PluginConfig{Enabled: false, Required: true}
		}, "required but not enabled"},
		{"no bind address", func(c *Config) { c.Server.Listen = "" }, "server.listen is required"},
		{"one port for both listeners", func(c *Config) {
			c.Server.FrontendListen = c.Server.Listen
		}, "must differ"},
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

// A role naming something this build does not define is no longer a refusal:
// roles are rows now, so the file cannot be checked against them without the
// database open. buildVerifier resolves the name at startup and fails there,
// where the store can say which roles exist.
func TestValidate_AcceptsAnUnknownRoleName(t *testing.T) {
	c := validConfig()
	c.Auth.StaticTokens[0].Role = "Night Shift"
	if err := c.Validate(); err != nil {
		t.Fatalf("a custom role name must reach startup to be resolved: %v", err)
	}
}

// The finer form stands on its own: a token that lists grants and no plugins
// still says what it reaches, so requiring the older list too would refuse a
// file that is complete.
func TestValidate_AcceptsATokenWithGrantsAndNoPlugins(t *testing.T) {
	c := validConfig()
	c.Auth.StaticTokens[0].Plugins = nil
	c.Auth.StaticTokens[0].Grants = []GrantConfig{{Plugin: "cnmaestro", Level: "read"}}
	if err := c.Validate(); err != nil {
		t.Fatalf("grants alone must be enough to describe a token: %v", err)
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
func TestEffectiveWarnings_FlagsRelaxedDurability(t *testing.T) {
	got := Effective{PublicURL: "https://mcp.example.net", RelaxedDurability: true}.Warnings()
	if len(got) == 0 || !strings.Contains(got[0], "durability") {
		t.Fatalf("expected a warning about relaxed durability, got %v", got)
	}
}

// The address lives in the database now, so a bad one cannot be a refusal:
// refusing to start would take away the page it is corrected on. It has to be
// a warning, and the warning has to actually fire.
func TestEffectiveWarnings_AddressProblemsAreWarningsNotRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		eff  Effective
		want string
	}{
		{"unset", Effective{}, "no public address is set"},
		{"unparseable", Effective{PublicURL: "://nope"}, "not a usable URL"},
		{"http against a self-signed listener",
			Effective{PublicURL: "http://mcp.example.net", TLSSelfSigned: true},
			"does not speak plaintext"},
		{"plaintext on a network",
			Effective{PublicURL: "http://192.168.50.125:9090"}, "in the clear"},
		{"metrics with no dashboard to serve them",
			Effective{PublicURL: "https://mcp.example.net", MetricsEnabled: true},
			"nothing is exposing it"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var found bool
			for _, w := range tc.eff.Warnings() {
				if strings.Contains(w, tc.want) {
					found = true
				}
			}
			if !found {
				t.Fatalf("warnings %v do not mention %q", tc.eff.Warnings(), tc.want)
			}
		})
	}
}

// Loopback never leaves the machine, by name as well as by address, so it
// needs no plaintext warning.
func TestEffectiveWarnings_LoopbackIsQuiet(t *testing.T) {
	for _, host := range []string{
		"http://localhost:9090", "http://127.0.0.1:9090", "http://[::1]:9090",
	} {
		eff := Effective{PublicURL: host, FrontendEnabled: true}
		for _, w := range eff.Warnings() {
			if strings.Contains(w, "in the clear") {
				t.Errorf("%s should not warn about plaintext: %s", host, w)
			}
		}
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
	if !c.Plugins["cnmaestro"].Enabled {
		t.Fatal("plugin enablement was not overridden")
	}
}

// Silently treating a typo as false would disable something with no
// indication why.
func TestEnvOverrides_RejectMalformedBoolean(t *testing.T) {
	t.Setenv("MCPD_FRONTEND_ENABLED", "ture")

	if _, err := parseLegacy(nil); err == nil ||
		!strings.Contains(err.Error(), "not a boolean") {
		t.Fatalf("a malformed boolean must be reported, not defaulted; got %v", err)
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

// A plugin's name and its type are the same thing until someone configures two
// instances of one integration, which is what every configuration written
// before instances existed relies on.
func TestPluginConfig_ResolvedType(t *testing.T) {
	for _, tc := range []struct {
		name, typ, want string
	}{
		{"echo", "", "echo"},
		{"nas-primary", "synology", "synology"},
		{"nas-backup", "  synology  ", "synology"},
		{"cnmaestro", "cnmaestro", "cnmaestro"},
	} {
		got := PluginConfig{Type: tc.typ}.ResolvedType(tc.name)
		if got != tc.want {
			t.Errorf("PluginConfig{Type:%q}.ResolvedType(%q) = %q, want %q",
				tc.typ, tc.name, got, tc.want)
		}
	}
}

// Both catalogues are on unless a deployment says otherwise, and saying
// otherwise has to work: a bool that defaults true is only switchable if the
// file's absent key leaves the default alone and its explicit false wins.
func TestCatalog_DefaultsOnAndCanBeSwitchedOff(t *testing.T) {
	// The three that need no credential are on; PulseMCP is not, because its
	// API refuses every unauthenticated request and a source that can only
	// answer 401 is worse to default to than one that is absent.
	def := Default()
	if def.Catalog.Docker {
		t.Error("the Docker catalogue is on by default; 29 of its 317 servers " +
			"can be imported here, and it made every page look mis-paged")
	}
	if !def.Catalog.Official || !def.Catalog.Smithery {
		t.Errorf("catalog = %+v, want the credential-free sources on by default", def.Catalog)
	}
	if def.Catalog.PulseMCP {
		t.Errorf("catalog = %+v, want pulsemcp off until it is configured", def.Catalog)
	}

	tests := []struct {
		name         string
		yaml         string
		wantOfficial bool
		wantDocker   bool
		wantSmithery bool
		wantPulseMCP bool
		wantEnabled  bool
	}{
		// Docker is the one source that is off unless asked for. See the
		// default: 29 of its 317 servers can be imported here.
		{name: "no catalog block at all", yaml: "",
			wantOfficial: true, wantSmithery: true, wantEnabled: true},
		{name: "the official registry only",
			yaml:         "catalog:\n  smithery: false\n",
			wantOfficial: true, wantEnabled: true},
		{name: "docker only, asked for",
			yaml:       "catalog:\n  official: false\n  smithery: false\n  docker: true\n",
			wantDocker: true, wantEnabled: true},
		{name: "smithery only",
			yaml:         "catalog:\n  official: false\n",
			wantSmithery: true, wantEnabled: true},
		// PulseMCP is the one source that is off unless asked for: its v0.1
		// API authenticates every request, so a deployment that has not been
		// issued a key would get a page of 401s rather than a catalogue.
		{name: "pulsemcp is off by default",
			yaml:         "catalog:\n  docker: false\n  smithery: false\n",
			wantOfficial: true, wantEnabled: true},
		{name: "pulsemcp switched on",
			yaml: "catalog:\n  official: false\n  docker: false\n" +
				"  smithery: false\n  pulsemcp: true\n",
			wantPulseMCP: true, wantEnabled: true},
		{name: "no catalogue at all",
			yaml: "catalog:\n  official: false\n  docker: false\n  smithery: false\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			if err := yaml.Unmarshal([]byte(tc.yaml), c); err != nil {
				t.Fatal(err)
			}
			if c.Catalog.Official != tc.wantOfficial {
				t.Errorf("official = %v, want %v", c.Catalog.Official, tc.wantOfficial)
			}
			if c.Catalog.Docker != tc.wantDocker {
				t.Errorf("docker = %v, want %v", c.Catalog.Docker, tc.wantDocker)
			}
			if c.Catalog.Smithery != tc.wantSmithery {
				t.Errorf("smithery = %v, want %v", c.Catalog.Smithery, tc.wantSmithery)
			}
			if c.Catalog.PulseMCP != tc.wantPulseMCP {
				t.Errorf("pulsemcp = %v, want %v", c.Catalog.PulseMCP, tc.wantPulseMCP)
			}
			if c.Catalog.Enabled() != tc.wantEnabled {
				t.Errorf("enabled = %v, want %v", c.Catalog.Enabled(), tc.wantEnabled)
			}
		})
	}
}
