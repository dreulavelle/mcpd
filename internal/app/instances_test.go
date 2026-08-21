package app

import (
	"context"
	"strings"
	"testing"

	"github.com/spoked/mcpd/internal/settings"
)

func instanceApp(t *testing.T) *App {
	t.Helper()
	return newSettingsApp(t)
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

// Removing a file-defined instance would not stick: it returns on the next
// start, which reads as the removal having failed.
func TestRemoveInstance_RefusesOneFromTheFile(t *testing.T) {
	a := instanceApp(t)
	err := a.RemoveInstance(context.Background(), "user:test", "echo")
	if err == nil {
		t.Fatal("a file-defined instance must not be removable here")
	}
	if !strings.Contains(err.Error(), "configuration file") {
		t.Fatalf("error = %q, want it to say where to remove it", err)
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

	if err := a.RemoveInstance(ctx, "user:test", "gone"); err != nil {
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
