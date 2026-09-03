package external

import (
	"encoding/json"
	"testing"

	"github.com/spoked/mcpd/internal/operations"
	"github.com/spoked/mcpd/internal/plugins"
	"github.com/spoked/mcpd/internal/settings"
)

// A discovered plugin becomes a type whose settings are the ones it declared,
// columns included, so the dashboard renders the same form for it that a
// compiled-in integration gets.
func TestTypeFor_CarriesSettingsAndColumns(t *testing.T) {
	d := DescribeResult{
		Protocol: ProtocolVersion, Name: "weather", Title: "Weather", Description: "Reads weather.",
		Tools: []ToolDescriptor{{Name: "get_forecast", Description: "x"}},
		Settings: []SettingDescriptor{
			{Key: "api_token", Label: "API token", Kind: "secret", Required: true},
			{Key: "stations", Label: "Stations", Kind: "collection", Columns: []SettingDescriptor{
				{Key: "name", Label: "Name", Kind: "string", Required: true},
				{Key: "key", Label: "Key", Kind: "secret"},
			}},
		},
	}
	typ, err := TypeFor("/plugins/weather", Manifest{Name: "weather", Exec: "weather"}, d)
	if err != nil {
		t.Fatal(err)
	}
	if typ.Name != "weather" || typ.Title != "Weather" || len(typ.Settings) != 2 {
		t.Fatalf("type: %+v", typ)
	}
	if typ.Settings[0].Kind != settings.KindSecret || !typ.Settings[0].Required {
		t.Errorf("secret field: %+v", typ.Settings[0])
	}
	stations := typ.Settings[1]
	if stations.Kind != settings.KindCollection || len(stations.Columns) != 2 || stations.Columns[1].Kind != settings.KindSecret {
		t.Errorf("collection field: %+v", stations)
	}

	// A declaration the host cannot offer -- a collection whose first column
	// is not a required string -- is refused at discovery, not at mount.
	bad := d
	bad.Settings = []SettingDescriptor{{Key: "rows", Label: "Rows", Kind: "collection", Columns: []SettingDescriptor{
		{Key: "count", Label: "Count", Kind: "int"},
	}}}
	if _, err := TypeFor("/plugins/weather", Manifest{Name: "weather", Exec: "weather"}, bad); err == nil {
		t.Error("a collection with a non-string identity column should be refused")
	}

	// A type with no title falls back to its name, since the host requires one.
	untitled := d
	untitled.Title = ""
	typ, err = TypeFor("/plugins/weather", Manifest{Name: "weather", Exec: "weather"}, untitled)
	if err != nil || typ.Title != "weather" {
		t.Errorf("an untitled plugin takes its name as title, got %q %v", typ.Title, err)
	}
}

// Apply is handed the whole plan, not only the opaque state, and the two agree
// on the state.
func TestPlanResultOf_CarriesTheWholePlan(t *testing.T) {
	risk := operations.RiskHigh
	plan := plugins.Plan[json.RawMessage]{
		Before:        json.RawMessage(`{"v":1}`),
		Desired:       json.RawMessage(`{"v":2}`),
		Preconditions: map[string]any{"rev": "abc"},
		Changes:       []operations.Change{{Field: "v", From: 1, To: 2}},
		Impact:        "changes v",
		Rollback:      map[string]any{"v": 1},
		RiskOverride:  &risk,
		State:         json.RawMessage(`{"plan":7}`),
	}
	full := planResultOf(plan, json.RawMessage(`{"plan":7}`))
	if string(full.Before) != `{"v":1}` || string(full.Desired) != `{"v":2}` || full.Impact != "changes v" {
		t.Errorf("plan: %+v", full)
	}
	if string(full.Preconditions) != `{"rev":"abc"}` || string(full.Rollback) != `{"v":1}` {
		t.Errorf("preconditions %s rollback %s", full.Preconditions, full.Rollback)
	}
	if len(full.Changes) != 1 || full.Changes[0].Field != "v" || full.RiskOverride != "high" {
		t.Errorf("changes %+v risk %q", full.Changes, full.RiskOverride)
	}
	if string(full.State) != `{"plan":7}` {
		t.Errorf("state %s", full.State)
	}
}

// The adapter names itself after the instance it was built for, so two
// instances of one plugin are two endpoints; without one it keeps the
// manifest's name, which is what a probe uses.
func TestDescriptor_UsesTheInstanceName(t *testing.T) {
	m := Manifest{Name: "weather", Exec: "weather"}
	p := NewPlugin("/plugins/weather", m, plugins.Deps{Instance: "weather_north"})
	if p.Descriptor().Name != "weather_north" {
		t.Errorf("descriptor should carry the instance name, got %q", p.Descriptor().Name)
	}
	p = NewPlugin("/plugins/weather", m, plugins.Deps{})
	if p.Descriptor().Name != "weather" {
		t.Errorf("without an instance the manifest name stands, got %q", p.Descriptor().Name)
	}
	if h := p.Health(); h.State != plugins.UnhealthyState {
		t.Errorf("before the handshake a plugin is not healthy, got %+v", h)
	}
}
