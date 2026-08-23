package mcpremote

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spoked/mcpd/internal/mcpservers"
	"github.com/spoked/mcpd/internal/settings"
)

// fixture reads a real published server.json. The parsing package keeps the
// copies; this one uses them to check the form they turn into.
func fixture(t *testing.T, name string) *mcpservers.Document {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "mcpservers", "testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	doc, err := mcpservers.Parse(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return doc
}

// TestFields_FromPublishedDocuments checks the whole mapping from server.json
// inputs to a settings form, against documents other people published.
//
// The property that matters most is the first one asserted: a value the
// document marks secret becomes KindSecret, which is what decides that it is
// encrypted at rest and withheld when read back. Everything else is a form
// that renders badly; that one is a credential in the clear.
func TestFields_FromPublishedDocuments(t *testing.T) {
	tests := []struct {
		file string
		want []settings.Field
	}{
		{
			file: "adadvisor.json",
			want: []settings.Field{
				{
					Key: "header_authorization", Label: "Authorization header",
					Kind: settings.KindSecret, Apply: settings.ApplyReconnect,
					Required: true,
				},
			},
		},
		{
			file: "autorfp.json",
			want: []settings.Field{
				{
					Key: "var_api_host", Label: "api_host",
					Kind: settings.KindEnum, Apply: settings.ApplyReconnect,
					Required: true,
					Options: []string{
						"api.autorfp.ai", "api.eu.autorfp.ai", "api.us.autorfp.ai",
					},
				},
			},
		},
		{
			file: "contabo.json",
			want: []settings.Field{
				{
					Key: "header_authorization", Label: "Authorization header",
					Kind: settings.KindSecret, Apply: settings.ApplyReconnect,
					Required: true,
				},
				{
					Key: "header_x_request_id", Label: "x-request-id header",
					Kind: settings.KindString, Apply: settings.ApplyReconnect,
				},
			},
		},
		{
			file: "biel.json",
			want: []settings.Field{
				{
					Key: "var_project_slug", Label: "project_slug",
					Kind: settings.KindString, Apply: settings.ApplyReconnect,
					Required: true,
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			got, err := Fields(fixture(t, tc.file))
			if err != nil {
				t.Fatalf("fields: %v", err)
			}
			// The budget is the host's own and is on every server, so it is
			// checked once here rather than repeated in every expectation.
			if len(got) != len(tc.want)+1 {
				t.Fatalf("got %d fields, want %d", len(got), len(tc.want)+1)
			}
			last := got[len(got)-1]
			if last.Key != KeyRequestsPerSecond || last.Kind != settings.KindInt {
				t.Errorf("last field should be the per-server budget, got %+v", last)
			}
			if last.Default != DefaultRequestsPerSecond {
				t.Errorf("budget default = %v, want %d", last.Default, DefaultRequestsPerSecond)
			}

			for i, want := range tc.want {
				got := got[i]
				if got.Key != want.Key {
					t.Errorf("field %d key = %q, want %q", i, got.Key, want.Key)
				}
				if got.Label != want.Label {
					t.Errorf("%s label = %q, want %q", want.Key, got.Label, want.Label)
				}
				if got.Kind != want.Kind {
					t.Errorf("%s kind = %q, want %q", want.Key, got.Kind, want.Kind)
				}
				if got.Apply != want.Apply {
					t.Errorf("%s apply = %q, want %q", want.Key, got.Apply, want.Apply)
				}
				if got.Required != want.Required {
					t.Errorf("%s required = %v, want %v", want.Key, got.Required, want.Required)
				}
				if len(got.Options) != len(want.Options) {
					t.Fatalf("%s options = %v, want %v", want.Key, got.Options, want.Options)
				}
				for j := range want.Options {
					if got.Options[j] != want.Options[j] {
						t.Errorf("%s options = %v, want %v", want.Key, got.Options, want.Options)
						break
					}
				}
				if got.Kind == settings.KindSecret && got.Default != nil {
					t.Errorf("%s is a secret and must not carry a default", want.Key)
				}
				// A field the host cannot namespace is a field the settings
				// page cannot draw.
				if err := settings.ValidatePluginField(got); err != nil {
					t.Errorf("%s: %v", want.Key, err)
				}
			}
		})
	}
}

// TestFields_MapsFormatsAndDefaults covers the parts of the input model the
// published fixtures happen not to exercise.
func TestFields_MapsFormatsAndDefaults(t *testing.T) {
	doc := parseInline(t, `{
		"$schema":"`+mcpservers.SchemaURI+`",
		"name":"io.example/x","description":"d","version":"1",
		"remotes":[{"type":"streamable-http","url":"https://x.test/{count}/{flag}/{region}/mcp",
			"variables":{
				"count":{"format":"number","default":"5","description":"how many"},
				"flag":{"format":"boolean","default":"true"},
				"region":{"choices":["eu","us"],"default":"eu","placeholder":"eu"}
			}}]}`)

	fields, err := Fields(doc)
	if err != nil {
		t.Fatalf("fields: %v", err)
	}
	byKey := map[string]settings.Field{}
	for _, f := range fields {
		byKey[f.Key] = f
	}

	if got := byKey["var_count"]; got.Kind != settings.KindInt {
		t.Errorf("format number should map to int, got %q", got.Kind)
	}
	if got := byKey["var_count"]; got.Help != "how many" {
		t.Errorf("description should become help, got %q", got.Help)
	}
	if got := byKey["var_flag"]; got.Kind != settings.KindBool || got.Default != true {
		t.Errorf("format boolean should map to bool with a decoded default, got %+v", got)
	}
	if got := byKey["var_region"]; got.Kind != settings.KindEnum ||
		got.Default != "eu" || got.Placeholder != "eu" {
		t.Errorf("choices should map to enum with its default and placeholder, got %+v", got)
	}
}

// TestFields_RefusesADefaultOutsideItsChoices catches a document that would
// render a form whose initial value the form itself rejects.
func TestFields_RefusesADefaultOutsideItsChoices(t *testing.T) {
	doc := parseInline(t, `{
		"$schema":"`+mcpservers.SchemaURI+`",
		"name":"io.example/x","description":"d","version":"1",
		"remotes":[{"type":"streamable-http","url":"https://x.test/{region}/mcp",
			"variables":{"region":{"choices":["eu","us"],"default":"apac"}}}]}`)

	if _, err := Fields(doc); err == nil {
		t.Fatal("expected a refusal")
	}
}

func parseInline(t *testing.T, body string) *mcpservers.Document {
	t.Helper()
	doc, err := mcpservers.Parse([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return doc
}
