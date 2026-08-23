package mcpservers

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestParse_EarlierFormats reads a real published document at every format
// this build vendors, and checks it against the same document with only its
// $schema changed to the current one.
//
// The comparison is the point. Asserting a URL and a settings key would show
// that the parser produced something; asserting that it produced exactly what
// the current format produces from the same bytes is what says the translation
// is faithful. Anything a format genuinely moved has to be moved back before
// this passes, and anything a format left alone is proved to have been left
// alone rather than assumed to have been.
//
// Every fixture is a document somebody else published, fetched from the
// official registry, not a shape written from a reading of the schema.
func TestParse_EarlierFormats(t *testing.T) {
	tests := []struct {
		file       string
		label      string
		wantURL    string
		wantTitle  string
		wantInputs []string
		wantSecret []string
	}{
		{
			// 2025-07-09 is the format that spelled an input's flags with
			// underscores. This document uses the newer spelling anyway --
			// every 2025-07-09 document in the live registry that sets the
			// flags at all does -- which is exactly why both are read. See
			// TestParse_LegacyInputFlagSpelling for the other spelling.
			file:       "tw-market-data-2025-07-09.json",
			label:      "2025-07-09",
			wantURL:    "https://mcp.twmarketdata.com/mcp",
			wantTitle:  "tw-market-data",
			wantInputs: []string{"header_x_api_key"},
			wantSecret: []string{"header_x_api_key"},
		},
		{
			// A document with packages as well as remotes. The packages are
			// not this host's business and must not turn into form fields.
			file:       "keenable-2025-09-16.json",
			label:      "2025-09-16",
			wantURL:    "https://api.keenable.ai/mcp?keenable_title=mcp-registry",
			wantTitle:  "web-search",
			wantInputs: []string{"header_x_api_key"},
			wantSecret: []string{"header_x_api_key"},
		},
		{
			file:       "echoloc-2025-09-29.json",
			label:      "2025-09-29",
			wantURL:    "https://api.echoloc.ai/mcp",
			wantTitle:  "company-technographics",
			wantInputs: []string{"header_x_api_key"},
			wantSecret: []string{"header_x_api_key"},
		},
		{
			// 2025-10-17 added `title` and `icons`. The title has to reach
			// DisplayTitle rather than falling back to the name's last
			// segment, which is what it would do if the field were dropped.
			file:       "egnyte-2025-10-17.json",
			label:      "2025-10-17",
			wantURL:    "https://mcp-server.egnyte.com/mcp",
			wantTitle:  "Egnyte Remote MCP Server",
			wantInputs: []string{"header_authorization"},
			wantSecret: []string{"header_authorization"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.label, func(t *testing.T) {
			raw := fixture(t, tc.file)
			doc, err := Parse(raw)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got := SchemaLabel(doc.Schema); got != tc.label {
				t.Errorf("schema label = %q, want %q", got, tc.label)
			}

			remote, err := doc.Remote()
			if err != nil {
				t.Fatalf("remote: %v", err)
			}
			if remote.URL != tc.wantURL {
				t.Errorf("url = %q, want %q", remote.URL, tc.wantURL)
			}
			if got := doc.DisplayTitle(); got != tc.wantTitle {
				t.Errorf("title = %q, want %q", got, tc.wantTitle)
			}

			inputs, err := doc.Inputs()
			if err != nil {
				t.Fatalf("inputs: %v", err)
			}
			var keys, secrets []string
			for _, in := range inputs {
				keys = append(keys, in.Key)
				if in.Input.IsSecret {
					secrets = append(secrets, in.Key)
				}
			}
			if strings.Join(keys, ",") != strings.Join(tc.wantInputs, ",") {
				t.Errorf("inputs = %v, want %v", keys, tc.wantInputs)
			}
			if strings.Join(secrets, ",") != strings.Join(tc.wantSecret, ",") {
				t.Errorf("secrets = %v, want %v", secrets, tc.wantSecret)
			}

			// The same bytes, declared as the current format. Everything this
			// host reads has to come out the same, or the earlier format was
			// not translated -- it was guessed at.
			current, err := Parse(restated(t, raw, doc.Schema, SchemaURI))
			if err != nil {
				t.Fatalf("the same document as %s does not parse: %v",
					SchemaLabel(SchemaURI), err)
			}
			currentRemote, err := current.Remote()
			if err != nil {
				t.Fatalf("remote of the restated document: %v", err)
			}
			if !reflect.DeepEqual(remote, currentRemote) {
				t.Errorf("remote differs from the %s equivalent:\n %s: %+v\n %s: %+v",
					SchemaLabel(SchemaURI), tc.label, remote, SchemaLabel(SchemaURI), currentRemote)
			}
			currentInputs, err := current.Inputs()
			if err != nil {
				t.Fatalf("inputs of the restated document: %v", err)
			}
			if !reflect.DeepEqual(inputs, currentInputs) {
				t.Errorf("inputs differ from the %s equivalent:\n %s: %+v\n %s: %+v",
					SchemaLabel(SchemaURI), tc.label, inputs, SchemaLabel(SchemaURI), currentInputs)
			}
			if !reflect.DeepEqual(doc.DisplayTitle(), current.DisplayTitle()) {
				t.Errorf("title differs from the %s equivalent: %q against %q",
					SchemaLabel(SchemaURI), doc.DisplayTitle(), current.DisplayTitle())
			}
		})
	}
}

// TestParse_LegacyInputFlagSpelling defends the one field rename in five
// formats: 2025-07-09 wrote is_required and is_secret, and 2025-09-16 renamed
// them to isRequired and isSecret.
//
// Getting this wrong is not a cosmetic loss. isSecret false is what decides
// that a credential written into a document is *not* refused, and that the
// operator's value is *not* encrypted at rest -- so a document read by the
// wrong spelling looks like one asking for a public value and is handled like
// one. The document below is a published 2025-07-09 fixture with its flags put
// back into that format's own spelling, because no document in the live
// registry uses it: publishers emit the newer names and leave the older
// $schema, which is why both are read and OR-ed rather than switched between.
func TestParse_LegacyInputFlagSpelling(t *testing.T) {
	raw := fixture(t, "tw-market-data-2025-07-09.json")
	underscored := strings.NewReplacer(
		`"isRequired"`, `"is_required"`,
		`"isSecret"`, `"is_secret"`,
	).Replace(string(raw))
	if !strings.Contains(underscored, `"is_secret"`) {
		t.Fatal("the fixture no longer carries the flags this test is about")
	}

	doc, err := Parse([]byte(underscored))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	inputs, err := doc.Inputs()
	if err != nil {
		t.Fatalf("inputs: %v", err)
	}
	if len(inputs) != 1 {
		t.Fatalf("inputs = %+v, want one", inputs)
	}
	if !inputs[0].Input.IsSecret {
		t.Error("is_secret did not survive the translation; the operator's API key " +
			"would be stored and rendered as though it were public")
	}
	if !inputs[0].Input.IsRequired {
		t.Error("is_required did not survive the translation")
	}

	// And the same document declared as a format that never had the
	// underscored spelling must not read it, because there it means nothing.
	current, err := Parse([]byte(strings.Replace(underscored,
		"https://static.modelcontextprotocol.io/schemas/2025-07-09/server.schema.json",
		SchemaURI, 1)))
	if err != nil {
		t.Fatalf("parse as the current format: %v", err)
	}
	currentInputs, err := current.Inputs()
	if err != nil {
		t.Fatalf("inputs: %v", err)
	}
	if currentInputs[0].Input.IsSecret {
		t.Error("is_secret was read from a document whose format does not define it")
	}
}

// TestParse_RefusesAFormatThisBuildDoesNotRead keeps the pin.
//
// Five dated formats exist and all five are vendored, so what reaches this is
// a $schema that is none of them: a typo, a bundle URL, a registry's own
// endpoint standing in for the format, or a version published after this
// build. All four shapes are in the live registry today. The refusal names
// what was declared, because "unsupported schema" with no value in it leaves
// the publisher nothing to fix.
func TestParse_RefusesAFormatThisBuildDoesNotRead(t *testing.T) {
	tests := []struct {
		name     string
		schema   string
		wantSaid string
	}{
		{
			name:     "a version newer than this build",
			schema:   "https://static.modelcontextprotocol.io/schemas/2026-03-01/server.schema.json",
			wantSaid: "2026-03-01",
		},
		{
			// Real: the right date at an address this build did not vendor.
			// The pin is by URI and not by the date inside it, because a
			// document naming a date at some other host is not a document
			// published against that format.
			name:     "the right date at the wrong address",
			schema:   "https://registry.modelcontextprotocol.io/v0/schema/2025-12-11",
			wantSaid: "registry.modelcontextprotocol.io",
		},
		{
			// Also real: a bundle, which is a different document entirely.
			name:     "a bundle rather than a server",
			schema:   "https://static.modelcontextprotocol.io/schemas/2025-12-11/server-bundle.json",
			wantSaid: "server-bundle.json",
		},
		{
			name:     "no schema at all",
			schema:   "",
			wantSaid: "declares no $schema",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw := strings.Replace(string(fixture(t, "egnyte-2025-10-17.json")),
				"https://static.modelcontextprotocol.io/schemas/2025-10-17/server.schema.json",
				tc.schema, 1)
			_, err := Parse([]byte(raw))
			if !errors.Is(err, ErrUnsupportedSchema) {
				t.Fatalf("err = %v, want an unsupported-schema refusal", err)
			}
			if !strings.Contains(err.Error(), tc.wantSaid) {
				t.Errorf("err = %q, want it to name %q", err, tc.wantSaid)
			}
			// And it has to say what would be accepted, or the publisher is
			// told they are wrong without being told what right looks like.
			for _, label := range SupportedSchemaLabels() {
				if !strings.Contains(err.Error(), label) {
					t.Errorf("err = %q, want it to offer %s", err, label)
				}
			}
		})
	}
}

// TestParse_RefusesAURLPlaceholderAnEarlierFormatCannotExplain is the one
// place an earlier format is refused for what it says rather than for how it
// spells it.
//
// remotes[].variables arrived with 2025-12-11. Eight documents in the live
// registry declare 2025-10-17 and put a {placeholder} in a remote's url
// anyway, all eight of them Microsoft's, and all eight carry a variables map
// their declared format does not define. Reading it would be this host
// inventing the meaning of somebody else's field and then dialling the address
// it produced; ignoring it and resolving from nothing would give an import
// that succeeds and a server that can never start. So the document is refused
// at the moment of paste, with the version named -- and the identical document
// republished against 2025-12-11 is accepted, which is what makes the refusal
// about the format rather than about the server.
func TestParse_RefusesAURLPlaceholderAnEarlierFormatCannotExplain(t *testing.T) {
	raw := fixture(t, "workiq-teamsserver-2025-10-17.json")

	_, err := Parse(raw)
	if err == nil {
		t.Fatal("parse succeeded; a placeholder the format cannot describe was resolved from nothing")
	}
	for _, want := range []string{"tenant_id", "2025-10-17", "2025-12-11"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to name %q", err, want)
		}
	}

	// The same bytes at 2025-12-11, where the variables map is part of the
	// format, are accepted and produce the field an operator fills in.
	current, err := Parse(restated(t, raw,
		"https://static.modelcontextprotocol.io/schemas/2025-10-17/server.schema.json", SchemaURI))
	if err != nil {
		t.Fatalf("the same document as %s: %v", SchemaLabel(SchemaURI), err)
	}
	inputs, err := current.Inputs()
	if err != nil {
		t.Fatalf("inputs: %v", err)
	}
	if len(inputs) != 1 || inputs[0].Key != "var_tenant_id" {
		t.Fatalf("inputs = %+v, want one var_tenant_id", inputs)
	}
	if inputs[0].Role != RoleVariable {
		t.Errorf("role = %q, want %q", inputs[0].Role, RoleVariable)
	}
}

// TestParse_DropsARemoteVariablesMapAnEarlierFormatDoesNotDefine covers the
// quieter half of the same rule: a variables map on an earlier document that
// has no placeholder to fill must not become a form field either. An operator
// asked for a value that substitutes nowhere is an operator being asked a
// question with no answer.
func TestParse_DropsARemoteVariablesMapAnEarlierFormatDoesNotDefine(t *testing.T) {
	raw := strings.Replace(string(fixture(t, "echoloc-2025-09-29.json")),
		`"url": "https://api.echoloc.ai/mcp",`,
		`"url": "https://api.echoloc.ai/mcp", "variables": {"region": {"description": "x"}},`, 1)

	doc, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	remote, err := doc.Remote()
	if err != nil {
		t.Fatalf("remote: %v", err)
	}
	if remote.Variables != nil {
		t.Errorf("variables = %+v, want them dropped: 2025-09-29 does not define the field",
			remote.Variables)
	}
	inputs, err := doc.Inputs()
	if err != nil {
		t.Fatalf("inputs: %v", err)
	}
	for _, in := range inputs {
		if in.Key == "var_region" {
			t.Error("a variable the declared format does not define became a form field")
		}
	}
}

// restated returns a document with its $schema swapped, so that the same bytes
// can be read as two formats.
func restated(t *testing.T, raw []byte, from, to string) []byte {
	t.Helper()
	out := strings.Replace(string(raw), from, to, 1)
	if out == string(raw) {
		t.Fatalf("the fixture does not declare %q", from)
	}
	return []byte(out)
}
