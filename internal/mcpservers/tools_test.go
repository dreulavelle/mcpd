package mcpservers

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestInspect covers the rules a remote descriptor has to pass before it can
// be mounted.
//
// The schema rule is the one worth the most. The host's out-of-process adapter
// quietly substitutes {"type":"object"} for a schema it cannot use, which is
// defensible for a binary an operator dropped in themselves. Doing it here
// would throw away the only argument validation standing between a model and
// someone else's endpoint, so the tool is refused and the reason is recorded.
func TestInspect(t *testing.T) {
	object := json.RawMessage(`{"type":"object","properties":{}}`)

	tests := []struct {
		name       string
		prefix     string
		descriptor Descriptor
		wantIn     string
	}{
		{
			name:       "an ordinary tool",
			prefix:     "weather",
			descriptor: Descriptor{Name: "getWeather", InputSchema: object},
		},
		{
			name:       "dots and hyphens are the specification's business",
			prefix:     "weather",
			descriptor: Descriptor{Name: "search.docs-v2", InputSchema: object},
		},
		{
			name:       "no name at all",
			prefix:     "weather",
			descriptor: Descriptor{InputSchema: object},
			wantIn:     "no name",
		},
		{
			name:       "a space in the name",
			prefix:     "weather",
			descriptor: Descriptor{Name: "read file", InputSchema: object},
			wantIn:     "character set",
		},
		{
			name:       "too long once prefixed",
			prefix:     "weather",
			descriptor: Descriptor{Name: strings.Repeat("a", 130), InputSchema: object},
			wantIn:     "128",
		},
		{
			name:       "no input schema",
			prefix:     "weather",
			descriptor: Descriptor{Name: "getWeather"},
			wantIn:     "publishes no input schema",
		},
		{
			name:   "a schema that is not an object",
			prefix: "weather",
			descriptor: Descriptor{
				Name: "getWeather", InputSchema: json.RawMessage(`{"type":"string"}`),
			},
			wantIn: "throw away the only validation",
		},
		{
			name:   "a schema that is not JSON",
			prefix: "weather",
			descriptor: Descriptor{
				Name: "getWeather", InputSchema: json.RawMessage(`not json`),
			},
			wantIn: "not a JSON object",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Inspect(tc.prefix, tc.descriptor)
			switch {
			case tc.wantIn == "" && got != "":
				t.Errorf("expected no problem, got %q", got)
			case tc.wantIn != "" && !strings.Contains(got, tc.wantIn):
				t.Errorf("problem %q does not mention %q", got, tc.wantIn)
			}
		})
	}
}

// TestHashDescriptor checks the identity a classification is guarded by:
// stable against a server that reorders its JSON, and sensitive to anything an
// administrator actually read.
func TestHashDescriptor(t *testing.T) {
	one := Descriptor{
		Name:        "getWeather",
		Description: "d",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}}}`),
	}
	// The same schema with its keys written the other way round.
	two := Descriptor{
		Name:        "getWeather",
		Description: "d",
		InputSchema: json.RawMessage(`{"properties":{"b":{"type":"string"},"a":{"type":"string"}},"type":"object"}`),
	}

	hashOne, err := HashDescriptor(one)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	hashTwo, err := HashDescriptor(two)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if hashOne != hashTwo {
		t.Error("reordering a schema's keys must not read as a changed tool")
	}

	// A changed description is a changed tool: it is what the model reads to
	// decide, and what an administrator read to approve.
	changed := one
	changed.Description = "d, and also emails your contacts"
	hashChanged, err := HashDescriptor(changed)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if hashChanged == hashOne {
		t.Error("a changed description must change the hash")
	}
}

func TestToolState_Valid(t *testing.T) {
	for _, s := range []ToolState{ToolPending, ToolEnabled, ToolDisabled} {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}
	if ToolState("mounted").Valid() {
		t.Error("an unknown state must not be valid")
	}
}
