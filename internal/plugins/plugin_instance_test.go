package plugins

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/spoked/mcpd/internal/auth"
)

// stubPlugin declares one constant name, the way a real plugin does.
type stubPlugin struct{ registered string }

func (s *stubPlugin) Descriptor() Descriptor {
	return Descriptor{Name: "synology", Version: "1.0.0", Title: "Synology"}
}

func (s *stubPlugin) Register(_ context.Context, r *Registry) error {
	s.registered = r.Descriptor().Name
	Tool(r, ToolSpec{
		Name: "shares", Title: "List shares", Description: "Lists shares.",
	}, func(_ context.Context, _ struct{}) (struct{}, error) { return struct{}{}, nil })
	return nil
}

func testManager(t *testing.T) *Manager {
	t.Helper()
	return NewManager(slog.New(slog.NewTextHandler(io.Discard, nil)), "test",
		func(context.Context, string, auth.Capability) error { return nil }, nil, nil, nil)
}

// The configured name wins over the one the plugin declares. A plugin knows
// what it is; only the host knows which of it this is, and the name is what
// the endpoint and the tool prefix are keyed on.
func TestRegister_InstanceNameOverridesTheDescriptor(t *testing.T) {
	m := testManager(t)
	p := &stubPlugin{}

	if err := m.Register(context.Background(), p, "nas-primary", false); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if p.registered != "nas-primary" {
		t.Errorf("registry saw %q, want the configured instance name", p.registered)
	}
	if mounted := m.Lookup("nas-primary"); mounted == nil {
		t.Fatal("plugin was not mounted under its instance name")
	}
	if mounted := m.Lookup("synology"); mounted != nil {
		t.Error("plugin was mounted under its type as well as its instance name")
	}
}

// Two instances of one integration are two plugins to the host: two endpoints,
// two entries in a credential's plugin list, and a history that says which one
// acted.
func TestRegister_TwoInstancesOfOneType(t *testing.T) {
	m := testManager(t)
	ctx := context.Background()

	for _, name := range []string{"nas-primary", "nas-backup"} {
		if err := m.Register(ctx, &stubPlugin{}, name, false); err != nil {
			t.Fatalf("Register(%s): %v", name, err)
		}
	}
	names := m.Names()
	if len(names) != 2 {
		t.Fatalf("mounted %v, want both instances", names)
	}
	for _, name := range names {
		mounted := m.Lookup(name)
		if mounted == nil {
			t.Fatalf("%s is not mounted", name)
		}
		if got := mounted.Descriptor.Endpoint(); got != "/mcp/"+name {
			t.Errorf("%s serves %q, want its own endpoint", name, got)
		}
	}
}

// Registering the same name twice is a configuration error, not two plugins.
func TestRegister_RefusesADuplicateInstanceName(t *testing.T) {
	m := testManager(t)
	ctx := context.Background()

	if err := m.Register(ctx, &stubPlugin{}, "nas", false); err != nil {
		t.Fatal(err)
	}
	if err := m.Register(ctx, &stubPlugin{}, "nas", false); err == nil {
		t.Fatal("the same instance name twice must be refused")
	}
}
