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
