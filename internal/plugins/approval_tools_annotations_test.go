package plugins

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/spoked/mcpd/internal/auth"
)

// listTools connects an in-memory client so the tools are read back the way a
// real client sees them, rather than from the registration call's arguments.
func listTools(t *testing.T, srv *mcp.Server) map[string]*mcp.Tool {
	t.Helper()
	ctx := context.Background()

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	t.Cleanup(func() { ss.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() { cs.Close() })

	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	out := make(map[string]*mcp.Tool, len(res.Tools))
	for _, tool := range res.Tools {
		out[tool.Name] = tool
	}
	return out
}

// The approval tools' annotations decide whether a client puts a change in
// front of a person or fires it like a getter. They enforce nothing -- the
// approval row does that -- but ChatGPT frames its confirmation from them, so
// getting destructiveHint backwards means a human never sees the change.
//
// approve_operation was annotated non-destructive because one variable holding
// false supplied both ReadOnlyHint, where false is right, and DestructiveHint,
// where it is not.
func TestApprovalToolAnnotations(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	gate := func(context.Context, string, auth.Capability) error { return nil }
	attachApprovalTools(srv, "echo", nil, gate)

	tools := listTools(t, srv)

	for _, tc := range []struct {
		name        string
		readOnly    bool
		destructive bool
	}{
		{"echo_list_operations", true, false},
		{"echo_get_operation", true, false},
		// Approving is the act that lets a change reach live infrastructure.
		{"echo_approve_operation", false, true},
		// Rejecting and cancelling settle a proposal without applying it.
		{"echo_reject_operation", false, false},
		{"echo_cancel_operation", false, false},
	} {
		tool, ok := tools[tc.name]
		if !ok {
			t.Fatalf("tool %s was not registered", tc.name)
		}
		a := tool.Annotations
		if a == nil {
			t.Fatalf("%s has no annotations; a client needs them to frame confirmation", tc.name)
		}
		if a.ReadOnlyHint != tc.readOnly {
			t.Errorf("%s readOnlyHint = %v, want %v", tc.name, a.ReadOnlyHint, tc.readOnly)
		}
		got := a.DestructiveHint != nil && *a.DestructiveHint
		if got != tc.destructive {
			t.Errorf("%s destructiveHint = %v, want %v", tc.name, got, tc.destructive)
		}
	}
}

// The propose tool's annotations decide whether ChatGPT frames the call as a
// confirmation or fires it like a getter, and that is the only prompt a user
// sees when a standing rule has already authorised the change.
//
// They were chosen on the premise that proposing "changes nothing upstream",
// which stopped being true when standing rules arrived: under a matching rule
// this call is approved and executed before it returns. destructiveHint was
// hardcoded false, so a client was told not to bother confirming a mutation
// that declares no way back, and openWorldHint was absent entirely even though
// read tools beside it set it and ChatGPT wants all three.
func TestProposeToolAnnotations(t *testing.T) {
	for _, tc := range []struct {
		name        string
		reversible  bool
		destructive bool
	}{
		// A change that can be undone is not destructive, whatever else it is.
		{"reversible", true, false},
		// One that declares no way back is exactly what a person should see.
		{"irreversible", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRegistry(Descriptor{Name: "cnmaestro", Version: "1.0.0", Title: "cnMaestro"})
			Mutation(r, radioSpec(tc.reversible), &countingMutation{})
			if err := r.err(); err != nil {
				t.Fatalf("registration: %v", err)
			}

			srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
			gate := func(context.Context, string, auth.Capability) error { return nil }
			r.mutations[0].attach(srv, gate, &fakeApprovals{}, &countingInline{}, noObserver{})

			tool, ok := listTools(t, srv)["cnmaestro_device_set_radio_channel"]
			if !ok {
				t.Fatal("the propose tool was not registered")
			}
			a := tool.Annotations
			if a == nil {
				t.Fatal("no annotations; a client needs them to frame confirmation")
			}
			if a.ReadOnlyHint {
				t.Error("readOnlyHint = true; a mutation is not a read")
			}
			got := a.DestructiveHint != nil && *a.DestructiveHint
			if got != tc.destructive {
				t.Errorf("destructiveHint = %v, want %v", got, tc.destructive)
			}
			if a.OpenWorldHint == nil || !*a.OpenWorldHint {
				t.Error("openWorldHint must be true: a mutation exists to reach the integration")
			}
		})
	}
}
