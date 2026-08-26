package plugins

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// A tool whose output carried json.RawMessage advertised "array of integers
// 0-255" and marshalled an object, so every call failed output validation
// inside the SDK and the caller saw only a transport error. Registration
// refuses the type now, which turns a runtime mystery into a startup message.
func TestTool_RejectsByteSlicesInSchemaTypes(t *testing.T) {
	type rawOut struct {
		Records []json.RawMessage `json:"records"`
	}
	type nestedOut struct {
		Inner struct {
			Blob []byte `json:"blob"`
		} `json:"inner"`
	}
	type rawIn struct {
		Payload json.RawMessage `json:"payload"`
	}
	type okOut struct {
		Records []map[string]any `json:"records"`
		Count   int              `json:"count"`
	}

	tests := []struct {
		name     string
		register func(*Registry)
		wantErr  string
	}{
		{
			name: "raw message in output",
			register: func(r *Registry) {
				Tool(r, ToolSpec{Name: "get_raw", Title: "Raw", Description: "d"},
					func(context.Context, struct{}) (rawOut, error) { return rawOut{}, nil })
			},
			wantErr: "output output.Records",
		},
		{
			name: "byte slice nested in output",
			register: func(r *Registry) {
				Tool(r, ToolSpec{Name: "get_nested", Title: "Nested", Description: "d"},
					func(context.Context, struct{}) (nestedOut, error) { return nestedOut{}, nil })
			},
			wantErr: "output output.Inner.Blob",
		},
		{
			name: "raw message in input",
			register: func(r *Registry) {
				Tool(r, ToolSpec{Name: "get_rawin", Title: "RawIn", Description: "d"},
					func(context.Context, rawIn) (struct{}, error) { return struct{}{}, nil })
			},
			wantErr: "input input.Payload",
		},
		{
			name: "a map of records is fine",
			register: func(r *Registry) {
				Tool(r, ToolSpec{Name: "get_ok", Title: "OK", Description: "d"},
					func(context.Context, struct{}) (okOut, error) { return okOut{}, nil })
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := &Registry{descriptor: Descriptor{Name: "probe", Version: "1.0.0"}}
			tc.register(r)

			if tc.wantErr == "" {
				if len(r.errs) != 0 {
					t.Fatalf("errs = %v, want none", r.errs)
				}
				if len(r.tools) != 1 {
					t.Fatalf("tools = %d, want 1", len(r.tools))
				}
				return
			}
			if len(r.errs) != 1 {
				t.Fatalf("errs = %v, want exactly one", r.errs)
			}
			if got := r.errs[0].Error(); !strings.Contains(got, tc.wantErr) {
				t.Errorf("error = %q, want it to name %q", got, tc.wantErr)
			}
			if len(r.tools) != 0 {
				t.Errorf("a rejected tool was still registered")
			}
		})
	}
}

// A type that refers to itself must not send the walk into a loop.
func TestCheckSchemaType_HandlesRecursiveTypes(t *testing.T) {
	type node struct {
		Name     string
		Children []*node
	}
	if err := checkSchemaType("p", "t", "output", reflect.TypeOf(node{})); err != nil {
		t.Fatalf("checkSchemaType: %v", err)
	}
}
