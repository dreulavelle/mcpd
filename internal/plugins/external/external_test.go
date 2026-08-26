package external

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spoked/mcpd/internal/plugins"
)

// buildExample compiles the worked example into a plugin directory, exactly
// as an operator would.
func buildExample(t *testing.T) (root, name string) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain unavailable")
	}

	root = t.TempDir()
	dir := filepath.Join(root, "echo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("go", "build", "-o", filepath.Join(dir, "echo"),
		"github.com/spoked/mcpd/examples/echo")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the example plugin failed: %v\n%s", err, out)
	}

	manifest := `{"name":"echo","exec":"echo"}`
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, "echo"
}

func testDeps() plugins.Deps {
	return plugins.Deps{
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Secrets: noSecrets{},
	}
}

type noSecrets struct{}

func (noSecrets) Secret(string) (string, error) { return "", os.ErrNotExist }

// The whole point of the external path: a plugin binary in a directory is
// discovered, started, described, and mounted with no host change.
func TestExternalPlugin_EndToEnd(t *testing.T) {
	root, name := buildExample(t)
	ctx := context.Background()

	manifests, dirs, err := Discover(root, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifests) != 1 || manifests[0].Name != name {
		t.Fatalf("discovered %+v, want one plugin named %s", manifests, name)
	}

	p := NewPlugin(dirs[name], manifests[0], testDeps())
	if err := p.Handshake(ctx); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

	d := p.Descriptor()
	if d.Name != "echo" || d.Version != "1.0.0" {
		t.Fatalf("descriptor = %+v", d)
	}

	// A read tool.
	out, err := p.callTool(ctx, "get_greeting", json.RawMessage(`{"name":"world"}`))
	if err != nil {
		t.Fatalf("greet: %v", err)
	}
	if !strings.Contains(string(out), "hello, world") {
		t.Fatalf("greet returned %s", out)
	}

	// The three mutation phases.
	bridge := &mutationBridge{plugin: p, action: "greeting.set"}
	params := json.RawMessage(`{"greeting":"howdy"}`)

	plan, err := bridge.Plan(ctx, params)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Changes) != 1 || plan.Changes[0].Field != "greeting" {
		t.Fatalf("plan changes = %+v", plan.Changes)
	}
	if plan.Preconditions["greeting"] != "hello" {
		t.Fatalf("preconditions = %+v, want the current greeting captured", plan.Preconditions)
	}
	if plan.Impact == "" {
		t.Fatal("a plan must describe its impact for whoever approves it")
	}

	// Planning must not have changed anything.
	out, _ = p.callTool(ctx, "get_greeting", json.RawMessage(`{"name":"world"}`))
	if !strings.Contains(string(out), "hello, world") {
		t.Fatalf("planning changed state: %s", out)
	}

	if _, err := bridge.Apply(ctx, params, plan); err != nil {
		t.Fatalf("apply: %v", err)
	}

	observed, err := bridge.Observe(ctx, params)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if !strings.Contains(string(observed), "howdy") {
		t.Fatalf("observed = %s, want the change reflected", observed)
	}

	// Health.
	if h := p.Check(ctx); h.State != plugins.HealthyState {
		t.Fatalf("health = %+v", h)
	}
}

// A plugin's own validation must reach the caller rather than being swallowed.
func TestExternalPlugin_PropagatesPluginErrors(t *testing.T) {
	root, name := buildExample(t)
	ctx := context.Background()

	manifests, dirs, _ := Discover(root, slog.New(slog.NewTextHandler(io.Discard, nil)))
	p := NewPlugin(dirs[name], manifests[0], testDeps())
	if err := p.Handshake(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

	if _, err := p.callTool(ctx, "get_greeting", json.RawMessage(`{"name":""}`)); err == nil {
		t.Fatal("the plugin's own validation error must reach the caller")
	}
	if _, err := p.callTool(ctx, "nonexistent", json.RawMessage(`{}`)); err == nil {
		t.Fatal("an unknown tool must be an error")
	}

	bridge := &mutationBridge{plugin: p, action: "greeting.set"}
	if _, err := bridge.Plan(ctx, json.RawMessage(`{"greeting":"hello"}`)); err == nil {
		t.Fatal("a no-op change must be refused by the plugin")
	}
}

func TestShutdown_StopsTheProcess(t *testing.T) {
	root, name := buildExample(t)
	ctx := context.Background()

	manifests, dirs, _ := Discover(root, slog.New(slog.NewTextHandler(io.Discard, nil)))
	p := NewPlugin(dirs[name], manifests[0], testDeps())
	if err := p.Handshake(ctx); err != nil {
		t.Fatal(err)
	}
	if !p.proc.Running() {
		t.Fatal("process should be running after handshake")
	}
	if err := p.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if p.proc.Running() {
		t.Fatal("process should have exited after shutdown")
	}
	// A call after shutdown must fail rather than hang.
	if _, err := p.callTool(ctx, "get_greeting", json.RawMessage(`{"name":"x"}`)); err == nil {
		t.Fatal("calling a stopped plugin must fail")
	}
}

// The manifest points at an executable the host will run, and the plugins
// directory is a bind mount that may be writable by someone other than the
// operator who reviewed it.
func TestManifest_Validate(t *testing.T) {
	tests := []struct {
		name  string
		m     Manifest
		valid bool
	}{
		{"ordinary", Manifest{Name: "netbox", Exec: "netbox"}, true},
		{"subdirectory", Manifest{Name: "netbox", Exec: "bin/netbox"}, true},
		{"bad name", Manifest{Name: "Net Box", Exec: "netbox"}, false},
		{"no exec", Manifest{Name: "netbox"}, false},
		{"absolute path", Manifest{Name: "netbox", Exec: "/bin/sh"}, false},
		{"parent traversal", Manifest{Name: "netbox", Exec: "../../bin/sh"}, false},
		{"embedded traversal", Manifest{Name: "netbox", Exec: "bin/../../sh"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.m.Validate()
			if tc.valid && err != nil {
				t.Fatalf("should be valid: %v", err)
			}
			if !tc.valid && err == nil {
				t.Fatal("should be rejected")
			}
		})
	}
}

// One bad directory must not stop the others from loading.
func TestDiscover_SkipsBadPluginsIndividually(t *testing.T) {
	root := t.TempDir()

	write := func(dir, manifest string) {
		full := filepath.Join(root, dir)
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatal(err)
		}
		if manifest != "" {
			if err := os.WriteFile(filepath.Join(full, "plugin.json"),
				[]byte(manifest), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	write("good", `{"name":"good","exec":"good"}`)
	write("broken", `{ not json`)
	write("escaping", `{"name":"escaping","exec":"../../../bin/sh"}`)
	write("mismatched", `{"name":"different","exec":"x"}`)
	write("nomanifest", "")

	manifests, _, err := Discover(root, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifests) != 1 || manifests[0].Name != "good" {
		t.Fatalf("discovered %+v, want only the valid plugin", manifests)
	}
}

// A missing plugins directory is normal, not an error.
func TestDiscover_MissingDirectory(t *testing.T) {
	manifests, _, err := Discover(filepath.Join(t.TempDir(), "absent"),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("a missing plugins directory should not be an error: %v", err)
	}
	if len(manifests) != 0 {
		t.Fatal("expected no plugins")
	}
}

// A plugin built against a different contract is more dangerous than one that
// will not load: it may misread a mutation payload.
func TestValidateDescribe(t *testing.T) {
	valid := DescribeResult{
		Protocol: ProtocolVersion, Name: "x", Version: "1.0.0",
		Tools: []ToolDescriptor{{Name: "t"}},
	}
	if err := validateDescribe("x", valid); err != nil {
		t.Fatalf("valid description rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*DescribeResult)
		want   string
	}{
		{"wrong protocol", func(d *DescribeResult) { d.Protocol = "99" }, "protocol"},
		{"name mismatch", func(d *DescribeResult) { d.Name = "other" }, "must match"},
		{"nothing exposed", func(d *DescribeResult) { d.Tools = nil }, "no tools or mutations"},
		{"invalid risk", func(d *DescribeResult) {
			d.Mutations = []MutationDescriptor{{Action: "a.b", Risk: "catastrophic"}}
		}, "invalid risk"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := valid
			tc.mutate(&d)
			err := validateDescribe("x", d)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want mention of %q", err, tc.want)
			}
		})
	}
}
