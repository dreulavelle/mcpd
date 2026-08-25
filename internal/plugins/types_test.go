package plugins

import (
	"strings"
	"testing"

	"github.com/spoked/mcpd/internal/settings"
)

// A visibility rule that cannot fire produces a field nobody can see, and
// therefore a setting nobody can fill in. The symptom is an integration that
// cannot be configured with no indication why, so these are caught at startup
// rather than waited for.
func TestType_RefusesUnfireableShowWhen(t *testing.T) {
	backend := settings.Field{
		Key: "backend", Label: "Backend", Kind: settings.KindEnum,
		Options: []string{"api", "database"},
	}
	for _, tc := range []struct {
		name   string
		fields []settings.Field
		want   string
	}{
		{
			name: "references a field that does not exist",
			fields: []settings.Field{backend, {
				Key: "db_host", Label: "Host", Kind: settings.KindString,
				ShowWhen: &settings.ShowWhen{Field: "mode", Equals: []string{"database"}},
			}},
			want: "there is no setting",
		},
		{
			name: "names a value the enum does not have",
			fields: []settings.Field{backend, {
				Key: "db_host", Label: "Host", Kind: settings.KindString,
				ShowWhen: &settings.ShowWhen{Field: "backend", Equals: []string{"databse"}},
			}},
			want: "not one of its options",
		},
		{
			name: "names no value at all",
			fields: []settings.Field{backend, {
				Key: "db_host", Label: "Host", Kind: settings.KindString,
				ShowWhen: &settings.ShowWhen{Field: "backend"},
			}},
			want: "names no value",
		},
		{
			name: "depends on itself",
			fields: []settings.Field{{
				Key: "backend", Label: "Backend", Kind: settings.KindEnum,
				Options:  []string{"api"},
				ShowWhen: &settings.ShowWhen{Field: "backend", Equals: []string{"api"}},
			}},
			want: "its own value",
		},
		{
			name: "chains through another conditional field",
			fields: []settings.Field{backend, {
				Key: "db_host", Label: "Host", Kind: settings.KindString,
				ShowWhen: &settings.ShowWhen{Field: "backend", Equals: []string{"database"}},
			}, {
				Key: "db_port", Label: "Port", Kind: settings.KindInt,
				ShowWhen: &settings.ShowWhen{Field: "db_host", Equals: []string{"x"}},
			}},
			want: "itself conditional",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Type{
				Name: "example", Title: "Example", Settings: tc.fields,
				New: func(Deps, map[string]any) (Plugin, error) { return nil, nil },
			}.Validate()
			if err == nil {
				t.Fatalf("want an error mentioning %q, got none", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q should mention %q", err, tc.want)
			}
		})
	}
}

// The ordinary case has to keep working: a control field plus fields revealed
// by each of its values.
func TestType_AcceptsAWorkableShowWhen(t *testing.T) {
	err := Type{
		Name: "example", Title: "Example",
		Settings: []settings.Field{
			{Key: "backend", Label: "Backend", Kind: settings.KindEnum,
				Options: []string{"api", "database"}},
			{Key: "token", Label: "Token", Kind: settings.KindSecret,
				ShowWhen: &settings.ShowWhen{Field: "backend", Equals: []string{"api"}}},
			{Key: "db_host", Label: "Host", Kind: settings.KindString,
				ShowWhen: &settings.ShowWhen{Field: "backend", Equals: []string{"database"}}},
			{Key: "max_items", Label: "Most items", Kind: settings.KindInt},
		},
		New: func(Deps, map[string]any) (Plugin, error) { return nil, nil },
	}.Validate()
	if err != nil {
		t.Fatalf("a workable declaration was refused: %v", err)
	}
}
