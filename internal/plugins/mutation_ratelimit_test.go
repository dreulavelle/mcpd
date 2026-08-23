package plugins

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/operations"
)

// countingMutation reports how far a refused proposal actually got. Plan is
// the upstream read; if it ran, the refusal cost the far end a request it
// should not have made.
type countingMutation struct {
	planned atomic.Int32
}

func (c *countingMutation) Plan(_ context.Context, p channelParams) (Plan[channelState], error) {
	c.planned.Add(1)
	return Plan[channelState]{
		Before:  channelState{Channel: "36"},
		Desired: channelState{Channel: p.Channel},
		Impact:  "Clients on this radio briefly disconnect.",
	}, nil
}

func (c *countingMutation) Apply(context.Context, channelParams, Plan[channelState]) (ApplyResult, error) {
	return ApplyResult{}, nil
}

func (c *countingMutation) Observe(context.Context, channelParams) (channelState, error) {
	return channelState{Channel: "149"}, nil
}

// countingApprovals counts how many operations were actually recorded.
type countingApprovals struct {
	fakeApprovals
	proposals atomic.Int32
}

func (c *countingApprovals) Propose(ctx context.Context, p *auth.Principal, req operations.ProposeRequest) (*operations.Operation, error) {
	c.proposals.Add(1)
	return c.fakeApprovals.Propose(ctx, p, req)
}

// mountedProposer keeps one mounted mutation so a test can call it repeatedly,
// which is the whole point when the behaviour under test is a rate limit.
type mountedProposer struct {
	session  *mcp.ClientSession
	mutation *countingMutation
	svc      *countingApprovals
}

func mountProposer(t *testing.T, spec MutationSpec) *mountedProposer {
	t.Helper()

	handler := &countingMutation{}
	svc := &countingApprovals{fakeApprovals: fakeApprovals{result: &operations.Operation{
		ID: "op_1", State: operations.StatePendingApproval,
		Plugin: "cnmaestro", Action: spec.Action, Risk: operations.RiskLow,
	}}}

	r := newRegistry(Descriptor{Name: "cnmaestro", Version: "1.0.0", Title: "cnMaestro"})
	Mutation(r, spec, handler)
	if err := r.err(); err != nil {
		t.Fatalf("registration: %v", err)
	}

	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	gate := func(context.Context, string, auth.Capability) error { return nil }
	r.mutations[0].attach(srv, gate, svc, &countingInline{}, noObserver{})

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

	return &mountedProposer{session: cs, mutation: handler, svc: svc}
}

// propose calls the tool and reports the error text the model would read, or
// "" when the call succeeded.
func (m *mountedProposer) propose(t *testing.T) string {
	t.Helper()
	res, err := m.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "cnmaestro_device_set_radio_channel",
		Arguments: map[string]any{"channel": "149"},
	})
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	if !res.IsError {
		return ""
	}
	var text strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	return text.String()
}

// The gap the approval policy opened: nothing rate-limited a mutation.
// ToolSpec.RateLimit bounded read tools and a propose tool had no equivalent,
// so under a permissive standing rule an agent could land writes as fast as it
// could call.
func TestProposeTool_IsRateLimited(t *testing.T) {
	p := mountProposer(t, radioSpec(true))

	if msg := p.propose(t); msg != "" {
		t.Fatalf("the first proposal must be allowed: %s", msg)
	}
	msg := p.propose(t)
	if msg == "" {
		t.Fatal("a second proposal in the same instant must be refused")
	}
	for _, want := range []string{"rate limited", "try again in"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal %q should contain %q", msg, want)
		}
	}
}

// A refused proposal must cost nothing a retry would find already spent. It
// must not read upstream, and above all it must not record an operation --
// because the idempotency key derived from the payload would then belong to an
// operation nobody asked for, and the retry the caller is being told to make
// would return that instead of proposing the change.
func TestProposeTool_ARefusalRecordsNothingAndReadsNothing(t *testing.T) {
	p := mountProposer(t, radioSpec(true))

	if msg := p.propose(t); msg != "" {
		t.Fatalf("first: %s", msg)
	}
	for range 5 {
		if msg := p.propose(t); msg == "" {
			t.Fatal("expected a refusal")
		}
	}

	if got := p.svc.proposals.Load(); got != 1 {
		t.Errorf("%d operations were recorded, want 1: a refusal must not spend "+
			"the idempotency of the operation it refused", got)
	}
	if got := p.mutation.planned.Load(); got != 1 {
		t.Errorf("plan ran %d times, want 1: a refused proposal must not reach "+
			"the upstream it is being refused to protect", got)
	}
}

// A mutation that declares a limit gets the one it declared. A plugin author
// knows what its own upstream costs: a label change can afford more than a
// reboot, and one that takes a site down should afford less than the default.
func TestProposeTool_HonoursADeclaredRateLimit(t *testing.T) {
	spec := radioSpec(true)
	spec.RateLimit = 0.5 // one every two seconds
	p := mountProposer(t, spec)

	if msg := p.propose(t); msg != "" {
		t.Fatalf("the first proposal: %s", msg)
	}
	msg := p.propose(t)
	if msg == "" {
		t.Fatal("a second proposal must be refused under a limit of one every two seconds")
	}
	// The declaration reached the limiter, and the number the model is given
	// is the one that actually applies rather than the host's default.
	if !strings.Contains(msg, "one every 2s") {
		t.Errorf("refusal %q should name the declared limit", msg)
	}
}

// A negative limit is a mistake, and a mistake in this direction would read as
// "no limit" if it were tolerated.
func TestMutation_RejectsANegativeRateLimit(t *testing.T) {
	r := newRegistry(Descriptor{Name: "cnmaestro", Version: "1.0.0", Title: "cnMaestro"})
	spec := radioSpec(true)
	spec.RateLimit = -1
	Mutation(r, spec, &countingMutation{})

	err := r.err()
	if err == nil {
		t.Fatal("a negative rate limit must be refused at registration")
	}
	if !strings.Contains(err.Error(), "rate limit") {
		t.Errorf("the refusal should say why: %v", err)
	}
}
