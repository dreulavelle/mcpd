package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spoked/mcpd/internal/config"
	"github.com/spoked/mcpd/internal/settings"
)

// buildExternal compiles a test plugin into a plugins directory, the way an
// operator would drop a binary in.
func buildExternal(t *testing.T, pkg, name string) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain unavailable")
	}
	root := t.TempDir()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", filepath.Join(dir, name), pkg)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building %s: %v\n%s", pkg, err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"name":"`+name+`","exec":"`+name+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// An out-of-process plugin's settings used to be declared over the wire and
// then dropped: nothing on the host asked for them, so the form never
// appeared, readiness never applied, and Configured always answered nothing.
// This is the test that would have caught it. A plugin dropped into the
// plugins directory appears as a type and an instance, waits for its required
// setting, and once the setting and a table row are stored it is mounted with
// both delivered -- the row's secret included.
func TestExternalPlugin_SettingsReachTheProcess(t *testing.T) {
	pluginsDir := buildExternal(t, "github.com/spoked/mcpd/internal/plugins/external/testdata/settingsdemo", "settingsdemo")

	dir := t.TempDir()
	t.Setenv("MCPD_TOKEN_WILDCARD", tokenWildcard)
	key, err := settings.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MCPD_SECRET_KEY", key)
	cfg := config.Default()
	cfg.SecretKeyRef = "env:MCPD_SECRET_KEY"
	cfg.Storage.Path = filepath.Join(dir, "mcpd.db")
	cfg.Storage.PluginsDir = pluginsDir
	cfg.Legacy().Storage.RelaxedDurability = ptr(true)
	cfg.Legacy().Server.PublicURL = ptr("https://mcp.test.invalid")
	cfg.Auth.StaticTokens = []config.StaticTokenConfig{{
		ID: "wildcard", SecretRef: "env:MCPD_TOKEN_WILDCARD",
		Principal: "svc:wildcard", Role: "admin", Plugins: []string{"*"},
	}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	a, err := New(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.db.Close() })

	// It is a type, with the settings it declared.
	typ, ok := a.types.Lookup("settingsdemo")
	if !ok {
		t.Fatal("the discovered plugin should be a type")
	}
	if len(typ.Settings) != 4 || typ.Settings[3].Kind != settings.KindCollection {
		t.Fatalf("type settings: %+v", typ.Settings)
	}

	// And an instance of itself, waiting on its required setting rather than
	// mounted with nothing.
	var found *Instance
	for _, inst := range a.instances(ctx) {
		if inst.Name == "settingsdemo" {
			found = &inst
		}
	}
	if found == nil || !found.FromPluginsDir || found.Type != "settingsdemo" || !found.Enabled {
		t.Fatalf("instance: %+v", found)
	}
	if a.manager.Lookup("settingsdemo") != nil {
		t.Fatal("a plugin whose required setting is empty must not be mounted")
	}
	if ready, missing := a.ready(ctx, *found); ready || strings.Join(missing, ",") != "Greeting" {
		t.Errorf("readiness should name the missing setting, got %v %v", ready, missing)
	}
	if _, ok := a.settingsCatalog().FieldFor("plugins.settingsdemo.greeting"); !ok {
		t.Error("the plugin's settings should be in the catalogue the dashboard renders")
	}

	// Configure it the way the dashboard does: a scalar through the settings
	// store, a row through the row store.
	if err := a.settings.Apply(ctx, "user:test", []settings.Change{
		{Key: "plugins.settingsdemo.greeting", Value: `"hello"`},
		{Key: "plugins.settingsdemo.token", Value: "tok-secret", Secret: true},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.pluginRows.Create(ctx, "user:test", "settingsdemo", "hosts", "alpha",
		map[string]any{"name": "alpha", "url": "https://alpha.example"}, map[string]string{"password": "pw"}); err != nil {
		t.Fatal(err)
	}
	if err := a.reconcileInstance(ctx, "settingsdemo"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if a.manager.Lookup("settingsdemo") == nil {
		t.Fatal("once configured the plugin should be mounted")
	}

	// The process was handed all of it.
	w := mcpRequest(t, a.Handler(), "/mcp/settingsdemo", tokenWildcard, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "settingsdemo_get_config", "arguments": map[string]any{}},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("call: %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	var env struct {
		Result struct {
			IsError    bool           `json:"isError"`
			Structured map[string]any `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(body[strings.Index(body, `{"jsonrpc"`):]), &env); err != nil {
		t.Fatal(err)
	}
	got := env.Result.Structured
	if env.Result.IsError || got["greeting"] != "hello" || got["token_set"] != true || got["retries"] != "3" {
		t.Errorf("the plugin did not receive its settings: %+v\n%s", got, body)
	}
	if hosts, _ := got["hosts"].([]any); len(hosts) != 1 || hosts[0] != "alpha" || got["hosts_with_password"] != float64(1) {
		t.Errorf("the table row and its secret should reach the plugin: %+v", got)
	}
	if strings.Contains(body, "tok-secret") || strings.Contains(body, `"pw"`) {
		t.Error("a secret value leaked into the tool result")
	}
}
