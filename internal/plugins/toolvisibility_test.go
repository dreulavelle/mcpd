package plugins

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/operations"
)

// visibilityPlugin is a minimal plugin with one read tool and one propose
// tool, for exercising the listing filter in isolation from any real
// integration.
type visibilityPlugin struct{}

func (visibilityPlugin) Descriptor() Descriptor {
	return Descriptor{Name: "widget", Version: "1.0.0", Title: "Widget"}
}

func (visibilityPlugin) Register(_ context.Context, r *Registry) error {
	Tool(r, ToolSpec{
		Name: "get_widget", Title: "Get widget", Description: "Reads the widget.",
	}, func(_ context.Context, _ struct{}) (struct{}, error) { return struct{}{}, nil })

	Mutation(r, MutationSpec{
		Action:      "widget.set",
		Title:       "Set widget",
		Description: "Changes the widget.",
		Risk:        operations.RiskLow,
		Reversible:  true,
	}, visibilityMutation{})
	return nil
}

type visibilityMutation struct{}

func (visibilityMutation) Plan(_ context.Context, _ struct{}) (Plan[struct{}], error) {
	return Plan[struct{}]{}, nil
}

func (visibilityMutation) Apply(_ context.Context, _ struct{}, _ Plan[struct{}]) (ApplyResult, error) {
	return ApplyResult{}, nil
}

func (visibilityMutation) Observe(_ context.Context, _ struct{}) (struct{}, error) {
	return struct{}{}, nil
}

// FilterTools hides a propose tool from a caller whose grant on the plugin is
// read, and shows it once the grant reaches write.
//
// Every tool of a reachable plugin used to be listed regardless, and a
// propose tool was refused only when called -- so a read-only credential was
// shown a tool it could never invoke, with no way to tell which without
// trying. The listing has to say only what the gate would allow.
func TestFilterTools_HidesProposeToolFromAReadOnlyGrant(t *testing.T) {
	m := NewManager(slog.New(slog.NewTextHandler(io.Discard, nil)), "test",
		func(context.Context, string, auth.Capability) error { return nil },
		&fakeApprovals{}, nil, nil)

	// The stub the listing filter is installed with: it reads the caller's
	// principal from the context the same way the gate does, and asks the
	// same authorizer the same question.
	authz := auth.NewAuthorizer()
	m.SetToolVisibility(func(ctx context.Context, tool string, required auth.Capability) bool {
		p := auth.FromContext(ctx)
		return authz.AuthorizeTool(p, "widget", required).Allowed
	})

	if err := m.Register(context.Background(), visibilityPlugin{}, "widget", false); err != nil {
		t.Fatalf("Register: %v", err)
	}
	mounted := m.Lookup("widget")
	if mounted == nil {
		t.Fatal("widget was not mounted")
	}

	operator, ok := auth.BuiltinRole(auth.RoleOperator)
	if !ok {
		t.Fatal("role_operator must be a built-in role")
	}
	reader := &auth.Principal{
		ID: "user:reader", RoleID: operator.ID, RoleName: operator.Name,
		Permissions: operator.Permissions,
		Grants:      auth.GrantsAt([]string{"widget"}, auth.LevelRead),
	}
	writer := &auth.Principal{
		ID: "user:writer", RoleID: operator.ID, RoleName: operator.Name,
		Permissions: operator.Permissions,
		Grants:      auth.GrantsAt([]string{"widget"}, auth.LevelWrite),
	}

	readTools := listToolsAs(t, mounted.Server, reader)
	if _, ok := readTools["widget_get_widget"]; !ok {
		t.Error("a read grant could not even see the read tool")
	}
	if _, ok := readTools["widget_widget_set"]; ok {
		t.Error("a read-only grant was shown the propose tool")
	}

	writeTools := listToolsAs(t, mounted.Server, writer)
	if _, ok := writeTools["widget_get_widget"]; !ok {
		t.Error("a write grant lost the read tool")
	}
	if _, ok := writeTools["widget_widget_set"]; !ok {
		t.Error("a write grant was not shown the propose tool")
	}
}

// listToolsAs connects an in-memory client under a context carrying p, so the
// listing filter -- which reads the principal from context, exactly as the
// gate does -- sees what a real request would.
func listToolsAs(t *testing.T, srv *mcp.Server, p *auth.Principal) map[string]*mcp.Tool {
	t.Helper()
	ctx := auth.WithPrincipal(context.Background(), p)

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
