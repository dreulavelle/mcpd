package app

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spoked/mcpd/internal/config"
	"github.com/spoked/mcpd/internal/settings"
)

func instanceApp(t *testing.T) *App {
	t.Helper()
	return newSettingsApp(t)
}

// fileApp builds a host whose configuration file declares the given plugins,
// on a database path the caller controls -- so a test can start a second host
// over the same database and assert what survived the restart, which is the
// whole claim a removal makes.
func fileApp(t *testing.T, dbPath string, plugins map[string]config.PluginConfig) *App {
	t.Helper()
	t.Setenv("MCPD_SECRET_KEY", "test-encryption-key-at-least-32-chars-long")

	cfg := config.Default()
	cfg.Storage.Path = dbPath
	cfg.Storage.RelaxedDurability = true
	cfg.Server.PublicURL = "http://localhost:9080"
	cfg.SecretKeyRef = "env:MCPD_SECRET_KEY"
	cfg.Plugins = plugins
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config invalid: %v", err)
	}
	a, err := New(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { a.db.Close() })
	return a
}

func instanceNamed(t *testing.T, a *App, name string) Instance {
	t.Helper()
	for _, inst := range a.instances(context.Background()) {
		if inst.Name == name {
			return inst
		}
	}
	t.Fatalf("no instance named %q", name)
	return Instance{}
}

func settingsChange(key, value string) []settings.Change {
	return []settings.Change{{Key: key, Value: value}}
}

// An instance added in the dashboard exists alongside the file's, and knows
// which it came from -- because the two can be removed in different places.
func TestInstances_StoreLayersOverFile(t *testing.T) {
	a := instanceApp(t)
	ctx := context.Background()

	before := a.instances(ctx)
	if len(before) == 0 {
		t.Fatal("the file's plugins should be listed")
	}
	for _, inst := range before {
		if !inst.FromFile {
			t.Errorf("%s should be marked as coming from the file", inst.Name)
		}
	}

	if err := a.AddInstance(ctx, "user:test", "echo-two", "echo"); err != nil {
		t.Fatalf("AddInstance: %v", err)
	}
	after := a.instances(ctx)
	if len(after) != len(before)+1 {
		t.Fatalf("got %d instances, want one more than %d", len(after), len(before))
	}

	var added *Instance
	for i := range after {
		if after[i].Name == "echo-two" {
			added = &after[i]
		}
	}
	if added == nil {
		t.Fatal("the added instance is missing")
	}
	if added.Type != "echo" || added.FromFile || !added.Enabled {
		t.Fatalf("added = %+v, want an enabled echo not from the file", *added)
	}
}

// Every name becomes a URL path segment, a tool prefix and a database value,
// so it is checked before anything is written rather than after.
func TestAddInstance_RefusesABadName(t *testing.T) {
	a := instanceApp(t)
	ctx := context.Background()

	for _, name := range []string{"", "A", "has space", "UPPER", "x", strings.Repeat("a", 40)} {
		if err := a.AddInstance(ctx, "user:test", name, "echo"); err == nil {
			t.Errorf("name %q must be refused", name)
		}
	}
}

func TestAddInstance_RefusesAnUnknownType(t *testing.T) {
	a := instanceApp(t)
	if err := a.AddInstance(context.Background(), "user:test", "thing", "nonesuch"); err == nil {
		t.Fatal("a type this build does not have must be refused")
	}
}

func TestAddInstance_RefusesADuplicateName(t *testing.T) {
	a := instanceApp(t)
	ctx := context.Background()

	if err := a.AddInstance(ctx, "user:test", "echo", "echo"); err == nil {
		t.Fatal("a name the file already uses must be refused")
	}
	if err := a.AddInstance(ctx, "user:test", "echo-two", "echo"); err != nil {
		t.Fatal(err)
	}
	if err := a.AddInstance(ctx, "user:test", "echo-two", "echo"); err == nil {
		t.Fatal("the same name twice must be refused")
	}
}

// The bug this test exists for: a plugin declared in config.yaml could not be
// removed from the dashboard at all, and the refusal told the operator to edit
// a file that is mounted read-only in every deployment this ships as. The
// removal is now recorded in the database, it beats the file, and it survives
// the restart -- which is the entire claim.
func TestRemoveInstance_OverridesTheFileAndSurvivesARestart(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "mcpd.db")
	declared := map[string]config.PluginConfig{"echo": {Enabled: true}}

	a := fileApp(t, dbPath, declared)
	if err := a.RemoveInstance(ctx, "user:test", "echo", false); err != nil {
		t.Fatalf("RemoveInstance: %v", err)
	}

	got := instanceNamed(t, a, "echo")
	if !got.Removed || got.Enabled || !got.FromFile {
		t.Fatalf("instance = %+v, want it listed as removed, off, still from the file", got)
	}
	if got.RemovedBy != "user:test" || got.RemovedAt.IsZero() {
		t.Fatalf("instance = %+v, want the removal to say who and when", got)
	}
	for _, inst := range a.enabledInstances(ctx) {
		if inst.Name == "echo" {
			t.Fatal("a removed instance must not be in the enabled set")
		}
	}
	a.db.Close()

	// A second host, the same database, the same unchanged file.
	restarted := fileApp(t, dbPath, declared)
	if got := instanceNamed(t, restarted, "echo"); !got.Removed || got.Enabled {
		t.Fatalf("after restart instance = %+v, want the removal to still hold", got)
	}
}

// Reversible from the same page, because a one-way door that needs SSH to
// undo is the problem this replaced rather than a fix for it.
func TestRestoreInstance_PutsItBackUnderTheFile(t *testing.T) {
	ctx := context.Background()
	a := fileApp(t, filepath.Join(t.TempDir(), "mcpd.db"),
		map[string]config.PluginConfig{"echo": {Enabled: true}})

	if err := a.RemoveInstance(ctx, "user:test", "echo", false); err != nil {
		t.Fatal(err)
	}
	if err := a.RestoreInstance(ctx, "user:test", "echo"); err != nil {
		t.Fatalf("RestoreInstance: %v", err)
	}
	got := instanceNamed(t, a, "echo")
	if got.Removed || !got.Enabled {
		t.Fatalf("instance = %+v, want it back and enabled as the file declares", got)
	}
	if err := a.RestoreInstance(ctx, "user:test", "echo"); err == nil {
		t.Fatal("restoring something that is not removed must be refused")
	}
}

// `required: true` is the deployment saying the host should not run without
// this integration. Overriding that is allowed and is not a click-through.
func TestRemoveInstance_RequiredTakesAnAcknowledgement(t *testing.T) {
	ctx := context.Background()
	a := fileApp(t, filepath.Join(t.TempDir(), "mcpd.db"),
		map[string]config.PluginConfig{"echo": {Enabled: true, Required: true}})

	err := a.RemoveInstance(ctx, "user:test", "echo", false)
	if err == nil {
		t.Fatal("removing a required plugin without acknowledging it must be refused")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Fatalf("error = %q, want it to say what the operator is overriding", err)
	}
	if got := instanceNamed(t, a, "echo"); got.Removed {
		t.Fatal("the refusal must not have removed it anyway")
	}

	if err := a.RemoveInstance(ctx, "user:test", "echo", true); err != nil {
		t.Fatalf("an acknowledged removal must be allowed: %v", err)
	}
	if got := instanceNamed(t, a, "echo"); !got.Removed {
		t.Fatal("the acknowledged removal did not take")
	}
}

// Only the file can mark a plugin required, so the flag has to survive the
// store's record of the same name -- otherwise touching an instance in the
// dashboard quietly removes the acknowledgement that flag exists to require.
func TestRemoveInstance_RequiredSurvivesAStoreRecord(t *testing.T) {
	ctx := context.Background()
	a := fileApp(t, filepath.Join(t.TempDir(), "mcpd.db"),
		map[string]config.PluginConfig{"echo": {Enabled: true, Required: true}})

	// What a toggle in the dashboard leaves behind for a file-declared name.
	if err := a.settings.Apply(ctx, "user:test", settingsChange(
		"instances.echo", `{"type":"echo","enabled":true}`)); err != nil {
		t.Fatal(err)
	}
	if got := instanceNamed(t, a, "echo"); !got.Required {
		t.Fatal("the file's required flag must survive the store's record")
	}
	if err := a.RemoveInstance(ctx, "user:test", "echo", false); err == nil {
		t.Fatal("removing it must still take an acknowledgement")
	}
}

// The same dead end one step smaller: `enabled: false` in a file nobody can
// edit is as unreachable as the entry itself.
func TestSetInstanceEnabled_OverridesTheFile(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "mcpd.db")
	declared := map[string]config.PluginConfig{"echo": {Enabled: false}}

	a := fileApp(t, dbPath, declared)
	if got := instanceNamed(t, a, "echo"); got.Enabled {
		t.Fatal("the file says it is off")
	}
	if err := a.SetInstanceEnabled(ctx, "user:test", "echo", true); err != nil {
		t.Fatalf("SetInstanceEnabled: %v", err)
	}
	if got := instanceNamed(t, a, "echo"); !got.Enabled {
		t.Fatal("the store must beat the file")
	}
	a.db.Close()

	restarted := fileApp(t, dbPath, declared)
	if got := instanceNamed(t, restarted, "echo"); !got.Enabled {
		t.Fatal("the override must survive a restart")
	}
}

// Switching a removed plugin on would be a toggle over something nothing is
// serving either way. The refusal names the way forward, which is what
// separates it from the dead end this feature exists to remove.
func TestSetInstanceEnabled_RefusesARemovedInstance(t *testing.T) {
	ctx := context.Background()
	a := fileApp(t, filepath.Join(t.TempDir(), "mcpd.db"),
		map[string]config.PluginConfig{"echo": {Enabled: true}})

	if err := a.RemoveInstance(ctx, "user:test", "echo", false); err != nil {
		t.Fatal(err)
	}
	err := a.SetInstanceEnabled(ctx, "user:test", "echo", true)
	if err == nil || !strings.Contains(err.Error(), "restore") {
		t.Fatalf("error = %v, want a refusal that names the restore", err)
	}
}

// The name is still the file's. Adding a second instance under it would leave
// two answers to what it means, one of which returns on a restore.
func TestAddInstance_RefusesTheNameOfARemovedFilePlugin(t *testing.T) {
	ctx := context.Background()
	a := fileApp(t, filepath.Join(t.TempDir(), "mcpd.db"),
		map[string]config.PluginConfig{"echo": {Enabled: true}})

	if err := a.RemoveInstance(ctx, "user:test", "echo", false); err != nil {
		t.Fatal(err)
	}
	err := a.AddInstance(ctx, "user:test", "echo", "echo")
	if err == nil || !strings.Contains(err.Error(), "restore") {
		t.Fatalf("error = %v, want a refusal that points at the restore", err)
	}
}

// A removal whose declaration has since gone stays recorded -- a host that
// started once with a truncated file must not forget every removal an
// operator made -- and is reported so it can be forgotten deliberately.
func TestStaleRemovals_AreKeptAndReported(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "mcpd.db")

	a := fileApp(t, dbPath, map[string]config.PluginConfig{"echo": {Enabled: true}})
	if err := a.RemoveInstance(ctx, "user:test", "echo", false); err != nil {
		t.Fatal(err)
	}
	a.db.Close()

	// The operator has since deleted the entry from their YAML.
	tidied := fileApp(t, dbPath, map[string]config.PluginConfig{})
	stale := tidied.staleRemovals(ctx)
	if len(stale) != 1 || stale[0].Name != "echo" {
		t.Fatalf("staleRemovals = %+v, want the orphaned removal reported", stale)
	}
	for _, inst := range tidied.instances(ctx) {
		if inst.Name == "echo" {
			t.Fatal("a removal with no declaration must not invent an instance")
		}
	}
	if err := tidied.RestoreInstance(ctx, "user:test", "echo"); err != nil {
		t.Fatalf("forgetting a stale removal: %v", err)
	}
	if len(tidied.staleRemovals(ctx)) != 0 {
		t.Fatal("the stale removal should be gone")
	}
}

// The file's declaration is shown read-only so an operator who does want to
// tidy their YAML can see what to delete -- keys only, because a settings
// block is where a credential is and this travels on a read endpoint.
func TestDeclarationFor_NamesTheKeysAndNotTheValues(t *testing.T) {
	a := fileApp(t, filepath.Join(t.TempDir(), "mcpd.db"), map[string]config.PluginConfig{
		"echo": {Enabled: true, Required: true, Settings: map[string]any{
			"api_token": "super-secret", "base_url": "https://x.test",
		}},
	})

	d := a.declarationFor("echo")
	if d == nil {
		t.Fatal("a declared plugin must have a declaration")
	}
	if d.Type != "echo" || !d.Enabled || !d.Required {
		t.Fatalf("declaration = %+v, want what the file says", d)
	}
	if len(d.SettingsKeys) != 2 || d.SettingsKeys[0] != "api_token" {
		t.Fatalf("settings keys = %v, want them sorted and complete", d.SettingsKeys)
	}
	if a.declarationFor("nonesuch") != nil {
		t.Error("an instance the file does not declare has no declaration")
	}
}

// Settings go with the instance. Leaving them would mean a name reused later
// silently inheriting someone else's credentials.
func TestRemoveInstance_ForgetsItsSettings(t *testing.T) {
	a := instanceApp(t)
	ctx := context.Background()

	if err := a.AddInstance(ctx, "user:test", "gone", "cnmaestro"); err != nil {
		t.Fatal(err)
	}
	key := "plugins.gone.base_url"
	if err := a.settings.Apply(ctx, "user:test", settingsChange(key, `"https://x.test"`)); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := a.settings.Get(ctx, key); !ok {
		t.Fatal("the setting was not stored")
	}

	if err := a.RemoveInstance(ctx, "user:test", "gone", false); err != nil {
		t.Fatalf("RemoveInstance: %v", err)
	}
	if _, ok, _ := a.settings.Get(ctx, key); ok {
		t.Error("the instance's settings outlived it")
	}
	for _, inst := range a.instances(ctx) {
		if inst.Name == "gone" {
			t.Error("the instance is still listed")
		}
	}
}

// A disabled instance stays configured and stops being mounted.
func TestSetInstanceEnabled(t *testing.T) {
	a := instanceApp(t)
	ctx := context.Background()

	if err := a.AddInstance(ctx, "user:test", "toggle", "echo"); err != nil {
		t.Fatal(err)
	}
	if err := a.SetInstanceEnabled(ctx, "user:test", "toggle", false); err != nil {
		t.Fatalf("SetInstanceEnabled: %v", err)
	}
	for _, inst := range a.enabledInstances(ctx) {
		if inst.Name == "toggle" {
			t.Fatal("a disabled instance must not be in the enabled set")
		}
	}
	// Still configured, so its settings survive being switched off.
	var present bool
	for _, inst := range a.instances(ctx) {
		if inst.Name == "toggle" {
			present = true
		}
	}
	if !present {
		t.Error("a disabled instance must still be listed")
	}
}

// Each instance gets its own settings group, which is what lets two of one
// integration hold different credentials.
func TestSettingsCatalog_CoversAddedInstances(t *testing.T) {
	a := instanceApp(t)
	ctx := context.Background()

	if err := a.AddInstance(ctx, "user:test", "cn-two", "cnmaestro"); err != nil {
		t.Fatal(err)
	}
	c := a.settingsCatalog()
	if _, ok := c.FieldFor("plugins.cn-two.client_id"); !ok {
		t.Fatal("the added instance's fields are not in the catalog")
	}
	if !c.IsSecret("plugins.cn-two.client_id") {
		t.Error("its credential must still be treated as a secret")
	}
}
