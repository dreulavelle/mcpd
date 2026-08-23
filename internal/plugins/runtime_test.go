package plugins

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spoked/mcpd/internal/operations"
)

func remoteRegistry(name string) *Registry {
	return newRegistry(Descriptor{
		Name: name, Version: "1.0.0", Title: "Remote", Runtime: RuntimeMCP,
	})
}

type noopMutation struct{}

func (noopMutation) Plan(context.Context, struct{}) (Plan[struct{}], error) {
	return Plan[struct{}]{}, nil
}
func (noopMutation) Apply(context.Context, struct{}, Plan[struct{}]) (ApplyResult, error) {
	return ApplyResult{}, nil
}
func (noopMutation) Observe(context.Context, struct{}) (struct{}, error) {
	return struct{}{}, nil
}

// TestMutation_RefusedFromARemoteMCPServer is the rule the whole runtime
// discriminator exists for.
//
// Enforced at the registry rather than by the code that builds a remote
// server, so that it holds for every path into registration -- a future
// adapter, a test, a mistake -- instead of holding only for as long as one
// constructor remembers to filter.
func TestMutation_RefusedFromARemoteMCPServer(t *testing.T) {
	r := remoteRegistry("weather")
	Mutation(r, MutationSpec{
		Action: "device.reboot", Title: "Reboot", Description: "d",
		Risk: operations.RiskHigh,
	}, noopMutation{})

	err := r.err()
	if err == nil {
		t.Fatal("a remote MCP server must not be able to register a write")
	}
	if !strings.Contains(err.Error(), "remote MCP server") {
		t.Errorf("the refusal should say why: %v", err)
	}
	if len(r.mutations) != 0 {
		t.Errorf("nothing should have been registered, got %d mutations", len(r.mutations))
	}
}

// A compiled-in plugin is unaffected: the refusal is scoped to the runtime,
// not applied to everyone.
func TestMutation_StillAllowedFromABuiltinPlugin(t *testing.T) {
	r := testRegistry()
	Mutation(r, MutationSpec{
		Action: "device.reboot", Title: "Reboot", Description: "d",
		Risk: operations.RiskHigh,
	}, noopMutation{})

	if err := r.err(); err != nil {
		t.Fatalf("registration: %v", err)
	}
	if len(r.mutations) != 1 {
		t.Fatalf("expected the mutation to register, got %d", len(r.mutations))
	}
}

// TestToolName_RulesAreScopedToTheRuntime defends both halves: the house style
// still applies to code this project ships, and a remote server's names are
// judged by the specification instead.
func TestToolName_RulesAreScopedToTheRuntime(t *testing.T) {
	tests := []struct {
		name        string
		runtime     Runtime
		tool        string
		wantAllowed bool
	}{
		{"house style, builtin", RuntimeBuiltin, "list_devices", true},
		{"camel case, builtin", RuntimeBuiltin, "getWeather", false},
		{"dots, builtin", RuntimeBuiltin, "search.docs", false},
		{"camel case, remote", RuntimeMCP, "getWeather", true},
		{"dots, remote", RuntimeMCP, "search.docs", true},
		{"hyphens, remote", RuntimeMCP, "read-file", true},
		{"digits and underscores, remote", RuntimeMCP, "v2_search", true},
		{"a space, remote", RuntimeMCP, "read file", false},
		{"a slash, remote", RuntimeMCP, "read/file", false},
		{"beyond 47 characters, remote", RuntimeMCP, strings.Repeat("a", 60), true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRegistry(Descriptor{
				Name: "thing", Version: "1", Title: "T", Runtime: tc.runtime,
			})
			Tool(r, ToolSpec{Name: tc.tool, Title: "T", Description: "d"},
				func(context.Context, struct{}) (struct{}, error) { return struct{}{}, nil })

			if allowed := r.err() == nil; allowed != tc.wantAllowed {
				t.Errorf("allowed = %v, want %v (%v)", allowed, tc.wantAllowed, r.err())
			}
		})
	}
}

// TestToolName_QualifiedLengthIsBounded catches the case the charset rule does
// not: a legal upstream name that no longer fits once the instance prefix is
// on the front of it.
func TestToolName_QualifiedLengthIsBounded(t *testing.T) {
	prefix := "weather"
	// One character past the limit, once the prefix and the underscore are on.
	name := strings.Repeat("a", maxQualifiedToolName-len(prefix))

	r := remoteRegistry(prefix)
	Tool(r, ToolSpec{Name: name, Title: "T", Description: "d"},
		func(context.Context, struct{}) (struct{}, error) { return struct{}{}, nil })

	err := r.err()
	if err == nil {
		t.Fatal("a qualified name past the limit must be refused")
	}
	if !strings.Contains(err.Error(), "128") {
		t.Errorf("the refusal should say what the limit is: %v", err)
	}
}

func TestDescriptor_RejectsAnUnknownRuntime(t *testing.T) {
	d := Descriptor{Name: "thing", Version: "1", Title: "T", Runtime: "wizard"}
	if err := d.Validate(); err == nil {
		t.Fatal("an unknown runtime must be refused")
	}
	// The zero value is builtin, so every plugin written before runtimes
	// existed keeps the rules it had.
	if (Descriptor{Name: "thing", Version: "1", Title: "T"}).EffectiveRuntime() != RuntimeBuiltin {
		t.Error("an unset runtime must mean builtin")
	}
}

// TestAttachAll_RemoteRuntimeLosesOnlyTheBadTool is the trap this exists for.
//
// One recover around the whole loop is right for a plugin this project ships.
// For a remote server it would mean one malformed descriptor out of three
// hundred taking the other two hundred and ninety-nine with it, and the far
// end's catalogue is not something an operator can fix.
func TestAttachAll_RemoteRuntimeLosesOnlyTheBadTool(t *testing.T) {
	// A schema the MCP SDK will refuse: it insists a tool's input schema
	// declares an object.
	bad := json.RawMessage(`{"type":"string"}`)

	r := remoteRegistry("weather")
	for _, name := range []string{"first", "second", "third"} {
		schema := json.RawMessage(`{"type":"object"}`)
		if name == "second" {
			schema = bad
		}
		Tool(r, ToolSpec{
			Name: name, Title: name, Description: "d", InputSchema: schema,
		}, func(context.Context, map[string]any) (any, error) { return nil, nil })
	}
	if err := r.err(); err != nil {
		t.Fatalf("registration: %v", err)
	}

	m := testManager(t)
	mounted, err := m.build(context.Background(), r.descriptor, &prebuilt{reg: r}, false)
	if err != nil {
		t.Fatalf("a single bad tool must not fail the mount: %v", err)
	}

	names := mounted.Registry.ToolNames()
	if len(names) != 3 {
		t.Fatalf("the registry should still know about all three, got %v", names)
	}

	// The same thing on a builtin runtime is a failed mount, which is the
	// behaviour that must not change.
	builtin := newRegistry(Descriptor{Name: "weather", Version: "1", Title: "W"})
	Tool(builtin, ToolSpec{Name: "second", Title: "s", Description: "d", InputSchema: bad},
		func(context.Context, map[string]any) (any, error) { return nil, nil })
	if _, err := m.build(context.Background(), builtin.descriptor, &prebuilt{reg: builtin}, false); err == nil {
		t.Error("a builtin plugin with a malformed tool should still fail its mount")
	}
}

// prebuilt hands the manager a registry that was already filled in, so a test
// can control exactly what is attached.
type prebuilt struct{ reg *Registry }

func (p *prebuilt) Descriptor() Descriptor { return p.reg.descriptor }

func (p *prebuilt) Register(_ context.Context, r *Registry) error {
	r.tools = p.reg.tools
	r.mutations = p.reg.mutations
	r.resources = p.reg.resources
	r.prompts = p.reg.prompts
	return nil
}
