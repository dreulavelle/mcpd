package external

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/operations"
	"github.com/spoked/mcpd/internal/plugins"
)

// buildPlugin compiles a package into a plugin directory, the way an operator
// would drop a binary in.
func buildPlugin(t *testing.T, pkg, name string) (dir string, m Manifest) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain unavailable")
	}

	dir = filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", filepath.Join(dir, name), pkg)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building %s failed: %v\n%s", pkg, err, out)
	}
	return dir, Manifest{Name: name, Exec: name}
}

// stubApprovals satisfies plugins.ApprovalService so a mutation can be mounted.
// Nothing here is called: the test mounts the plugin, it does not propose.
type stubApprovals struct{}

func (stubApprovals) Propose(context.Context, *auth.Principal, operations.ProposeRequest) (*operations.Operation, error) {
	return nil, os.ErrInvalid
}
func (stubApprovals) Approve(context.Context, *auth.Principal, string, string) (*operations.Operation, error) {
	return nil, os.ErrInvalid
}
func (stubApprovals) Reject(context.Context, *auth.Principal, string, string) (*operations.Operation, error) {
	return nil, os.ErrInvalid
}
func (stubApprovals) Cancel(context.Context, *auth.Principal, string, string) (*operations.Operation, error) {
	return nil, os.ErrInvalid
}
func (stubApprovals) Get(context.Context, *auth.Principal, string) (*operations.Operation, error) {
	return nil, os.ErrInvalid
}
func (stubApprovals) ApproveInline(context.Context, *auth.Principal, string) (*operations.Operation, error) {
	return nil, os.ErrInvalid
}
func (stubApprovals) AwaitOutcome(context.Context, string, time.Duration) (*operations.Operation, error) {
	return nil, os.ErrInvalid
}
func (stubApprovals) List(context.Context, *auth.Principal, string, []operations.OperationState, int) ([]*operations.Operation, error) {
	return nil, os.ErrInvalid
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// Register had never been called by a test, and it had never worked. Every
// out-of-process tool declares json.RawMessage parameters -- the only honest
// type for a schema known at run time -- and the registry's derived-schema
// check refused that type unconditionally, including when the spec supplied
// its own schema and nothing was derived. So every registration failed, on
// every tool, and the package sat in the tree unable to mount anything.
//
// This is that test. It goes through the manager rather than the registry, so
// it also covers the MCP SDK accepting what the registry produced.
func TestExternalPlugin_RegisterMountsToolsAndMutations(t *testing.T) {
	ctx := context.Background()
	dir, manifest := buildPlugin(t, "github.com/spoked/mcpd/examples/echo", "echo")

	p := NewPlugin(dir, manifest, testDeps())
	if err := p.Handshake(ctx); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

	manager := plugins.NewManager(quietLog(), "test",
		func(context.Context, string, auth.Capability) error { return nil },
		stubApprovals{}, nil)
	if err := manager.Register(ctx, p, "echo", true); err != nil {
		t.Fatalf("register: %v", err)
	}

	mounted := manager.Lookup("echo")
	if mounted == nil {
		t.Fatal("the plugin did not mount")
	}
	if got := mounted.Registry.ToolNames(); !slices.Contains(got, "echo_greet") {
		t.Fatalf("tools = %v, want echo_greet among them", got)
	}
	if got := mounted.Registry.MutationActions(); !slices.Contains(got, "greeting.set") {
		t.Fatalf("mutations = %v, want greeting.set among them", got)
	}
}

// The verifiability a plugin declares has to survive the wire, because the
// host settles the operation on it. A plugin built before the field existed
// omits it and is recorded as unable to confirm its own writes, which is the
// safe direction.
func TestExternalPlugin_CarriesDeclaredVerifiability(t *testing.T) {
	ctx := context.Background()
	dir, manifest := buildPlugin(t, "github.com/spoked/mcpd/examples/echo", "echo")

	p := NewPlugin(dir, manifest, testDeps())
	if err := p.Handshake(ctx); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

	manager := plugins.NewManager(quietLog(), "test",
		func(context.Context, string, auth.Capability) error { return nil },
		stubApprovals{}, nil)
	if err := manager.Register(ctx, p, "echo", true); err != nil {
		t.Fatalf("register: %v", err)
	}

	spec, ok := plugins.NewRunner(manager).MutationSpecFor("echo", "greeting.set")
	if !ok {
		t.Fatal("greeting.set is not registered")
	}
	if !spec.Verifiable {
		t.Fatal("the example plugin declares Verifiable; the host did not record it")
	}
}

// Two live proposals of the same change, with byte-identical parameters, must
// not share a plan.
//
// The bridge used to discard its plan argument and read the plugin's opaque
// state from a map on the plugin keyed on the action and those very
// parameters. Whichever proposal applied first consumed the entry; the second
// received nil and the plugin had no way to tell it had been handed somebody
// else's snapshot, or none.
func TestMutationBridge_IdenticalParamsDoNotCrossPlans(t *testing.T) {
	ctx := context.Background()
	dir, manifest := buildPlugin(t,
		"github.com/spoked/mcpd/internal/plugins/external/testdata/planstate", "planstate")

	p := NewPlugin(dir, manifest, testDeps())
	if err := p.Handshake(ctx); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

	bridge := &mutationBridge{plugin: p, action: "thing.set"}
	params := json.RawMessage(`{"value":"same"}`)

	first, err := bridge.Plan(ctx, params)
	if err != nil {
		t.Fatalf("first plan: %v", err)
	}
	second, err := bridge.Plan(ctx, params)
	if err != nil {
		t.Fatalf("second plan: %v", err)
	}

	firstTicket := ticketOf(t, first.State)
	secondTicket := ticketOf(t, second.State)
	if firstTicket == secondTicket {
		t.Fatalf("both plans carry ticket %q; the test plugin should issue one per plan",
			firstTicket)
	}

	// Applied out of order on purpose: neither result may depend on which plan
	// ran last, which is precisely what the shared map made it depend on.
	secondApply, err := bridge.Apply(ctx, params, second)
	if err != nil {
		t.Fatalf("apply of the second plan: %v", err)
	}
	firstApply, err := bridge.Apply(ctx, params, first)
	if err != nil {
		t.Fatalf("apply of the first plan: %v", err)
	}

	if secondApply.UpstreamRef != secondTicket {
		t.Errorf("the second apply used plan %q, want %q",
			secondApply.UpstreamRef, secondTicket)
	}
	if firstApply.UpstreamRef != firstTicket {
		t.Errorf("the first apply used plan %q, want %q",
			firstApply.UpstreamRef, firstTicket)
	}
}

func ticketOf(t *testing.T, state any) string {
	t.Helper()
	raw, ok := state.(json.RawMessage)
	if !ok {
		t.Fatalf("plan state is %T, want raw JSON", state)
	}
	var decoded struct {
		Ticket string `json:"ticket"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode plan state: %v", err)
	}
	if decoded.Ticket == "" {
		t.Fatal("the plan carries no ticket")
	}
	return decoded.Ticket
}
