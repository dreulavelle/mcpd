package settings

import (
	"strings"
	"testing"
)

// A plugin's fields are namespaced by instance, because two instances of one
// integration have different credentials and that is the point of having two.
func TestPluginGroup_NamespacesByInstance(t *testing.T) {
	fields := []Field{
		{Key: "client_id", Label: "Client ID", Kind: KindSecret},
		{Key: "base_url", Label: "Address", Kind: KindString},
	}
	a := PluginGroup("nas-primary", "Synology", fields)
	b := PluginGroup("nas-backup", "Synology", fields)

	if a.Fields[0].Key != "plugins.nas-primary.client_id" {
		t.Fatalf("key = %q, want it namespaced", a.Fields[0].Key)
	}
	if a.Fields[0].Key == b.Fields[0].Key {
		t.Fatal("two instances share a key; one would overwrite the other")
	}
	if a.Section != SectionPlugins {
		t.Errorf("section = %q, want the plugins page", a.Section)
	}
	// The plugin is rebuilt on the spot, so nothing has to be restarted.
	if a.Fields[0].Apply != ApplyReconnect {
		t.Errorf("apply = %q, want reconnect", a.Fields[0].Apply)
	}
}

func TestPluginFromSettingKey(t *testing.T) {
	for _, tc := range []struct{ key, instance, field string }{
		{"plugins.nas-primary.client_id", "nas-primary", "client_id"},
		{"plugins.echo.enabled", "echo", "enabled"},
		{"tunnel.api_key", "", ""},
		{"plugins.bad name.x", "", ""},
		{"plugins.x", "", ""},
	} {
		gotI, gotF := PluginFromSettingKey(tc.key)
		if gotI != tc.instance || gotF != tc.field {
			t.Errorf("PluginFromSettingKey(%q) = %q,%q want %q,%q",
				tc.key, gotI, gotF, tc.instance, tc.field)
		}
	}
}

// The catalog exists so a plugin's own settings can be validated and encrypted
// like any other. A static schema could not describe them, which is why they
// had to live in a file.
func TestCatalog_KnowsPluginFields(t *testing.T) {
	c := NewCatalog(PluginGroup("nas", "Synology", []Field{
		{Key: "client_id", Label: "Client ID", Kind: KindSecret, Required: true},
		{Key: "port", Label: "Port", Kind: KindInt, Min: intPtr(1), Max: intPtr(65535)},
	}))

	key := PluginSettingKey("nas", "client_id")
	if !c.IsSecret(key) {
		t.Error("a plugin's secret field must be treated as a secret")
	}
	if err := c.Validate(key, ""); err == nil {
		t.Error("a required field must reject an empty value")
	}
	if err := c.Validate(PluginSettingKey("nas", "port"), "70000"); err == nil {
		t.Error("an out-of-range int must be refused")
	}
	if err := c.Validate(PluginSettingKey("nas", "port"), "443"); err != nil {
		t.Errorf("a valid port was refused: %v", err)
	}

	// And the host's own settings still work.
	if err := c.Validate(KeyTunnelEnabled, "true"); err != nil {
		t.Errorf("host setting was refused: %v", err)
	}
	if err := c.Validate("plugins.nas.nonesuch", "x"); err == nil {
		t.Error("an undeclared plugin field must be refused")
	}
}

// Plugins are compiled in, so a bad field is a developer's mistake and worth
// catching at registration rather than when someone opens the page.
func TestValidatePluginField(t *testing.T) {
	for _, tc := range []struct {
		name  string
		field Field
		ok    bool
	}{
		{"good", Field{Key: "client_id", Label: "Client ID", Kind: KindSecret}, true},
		{"no key", Field{Label: "X", Kind: KindString}, false},
		{"key would not namespace", Field{Key: "Client-ID", Label: "X", Kind: KindString}, false},
		{"no label", Field{Key: "x", Kind: KindString}, false},
		{"unknown kind", Field{Key: "x", Label: "X", Kind: "colour"}, false},
	} {
		err := ValidatePluginField(tc.field)
		if tc.ok && err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s: expected a refusal", tc.name)
		}
	}
}

// Two instances must not be able to collide, since a collision would mean one
// plugin reading another's credentials.
func TestCatalog_TwoInstancesStaySeparate(t *testing.T) {
	fields := []Field{{Key: "token", Label: "Token", Kind: KindSecret}}
	c := NewCatalog(
		PluginGroup("a", "Thing", fields),
		PluginGroup("b", "Thing", fields),
	)
	seen := map[string]bool{}
	for _, g := range c.Groups() {
		for _, f := range g.Fields {
			if seen[f.Key] {
				t.Fatalf("duplicate key %q across groups", f.Key)
			}
			seen[f.Key] = true
		}
	}
	if !strings.Contains(strings.Join(keysOf(c), " "), "plugins.a.token") {
		t.Error("instance a's field is missing")
	}
}

func keysOf(c *Catalog) []string {
	var out []string
	for _, g := range c.Groups() {
		for _, f := range g.Fields {
			out = append(out, f.Key)
		}
	}
	return out
}

// A visibility rule names the field it depends on, and PluginGroup namespaces
// every key in the form. If the rule is not namespaced too it points at a key
// the form does not contain -- the dashboard reads an empty value, matches
// nothing, and hides the field forever. The symptom is an integration whose
// credentials cannot be entered at all, which is worse than not hiding them.
func TestPluginGroup_NamespacesTheFieldAShowWhenPointsAt(t *testing.T) {
	gate := &ShowWhen{Field: "backend", Equals: []string{"api"}}
	group := PluginGroup("observium", "Observium", []Field{
		{Key: "backend", Label: "Backend", Kind: KindEnum, Options: []string{"api", "database"}},
		{Key: "token", Label: "Token", Kind: KindSecret, ShowWhen: gate},
	})

	var token Field
	for _, f := range group.Fields {
		if strings.HasSuffix(f.Key, ".token") {
			token = f
		}
	}
	if token.ShowWhen == nil {
		t.Fatal("the rule was dropped")
	}
	want := PluginSettingKey("observium", "backend")
	if token.ShowWhen.Field != want {
		t.Fatalf("ShowWhen.Field = %q, want %q — it must name the key as the "+
			"form actually holds it", token.ShowWhen.Field, want)
	}
	// The controlling field has to be a key that is really in this group,
	// otherwise the lookup finds nothing whatever it is named.
	var found bool
	for _, f := range group.Fields {
		if f.Key == token.ShowWhen.Field {
			found = true
		}
	}
	if !found {
		t.Fatalf("no field in the group has key %q", token.ShowWhen.Field)
	}
}

// A plugin declares one rule value and shares it across every field it gates,
// and the same declaration builds the form for a second instance. Rewriting it
// in place would namespace it twice and corrupt the first instance's form.
func TestPluginGroup_DoesNotMutateTheDeclaredRule(t *testing.T) {
	gate := &ShowWhen{Field: "backend", Equals: []string{"api"}}
	fields := []Field{
		{Key: "backend", Label: "Backend", Kind: KindEnum, Options: []string{"api"}},
		{Key: "token", Label: "Token", Kind: KindSecret, ShowWhen: gate},
		{Key: "base_url", Label: "Address", Kind: KindString, ShowWhen: gate},
	}

	first := PluginGroup("obs-hq", "HQ", fields)
	if gate.Field != "backend" {
		t.Fatalf("the declared rule was rewritten to %q; it is shared across "+
			"fields and instances and must not be mutated", gate.Field)
	}

	second := PluginGroup("obs-dc2", "DC2", fields)
	for _, group := range []Group{first, second} {
		instance := strings.TrimPrefix(group.Name, "plugin:")
		want := PluginSettingKey(instance, "backend")
		for _, f := range group.Fields {
			if f.ShowWhen != nil && f.ShowWhen.Field != want {
				t.Errorf("%s: %s points at %q, want %q",
					instance, f.Key, f.ShowWhen.Field, want)
			}
		}
	}
}

// A field with no rule is untouched, so the common case cannot regress.
func TestPluginGroup_LeavesUnconditionalFieldsAlone(t *testing.T) {
	group := PluginGroup("observium", "Observium", []Field{
		{Key: "max_items", Label: "Most items", Kind: KindInt},
	})
	if group.Fields[0].ShowWhen != nil {
		t.Fatal("a field with no rule gained one")
	}
}
