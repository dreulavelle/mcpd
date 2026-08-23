package plugins

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/operations"
)

// fakeApprovals records what the propose tool asked for and answers with a
// scripted operation, so a test can see the request the host built rather than
// inferring it from what happened downstream.
type fakeApprovals struct {
	proposed operations.ProposeRequest
	result   *operations.Operation

	awaited      atomic.Int32
	approvedInln atomic.Int32
	rejected     atomic.Int32
}

func (f *fakeApprovals) Propose(_ context.Context, _ *auth.Principal, req operations.ProposeRequest) (*operations.Operation, error) {
	f.proposed = req
	return f.result, nil
}

func (f *fakeApprovals) Approve(_ context.Context, _ *auth.Principal, _, _ string) (*operations.Operation, error) {
	return f.result, nil
}

func (f *fakeApprovals) Reject(_ context.Context, _ *auth.Principal, _, _ string) (*operations.Operation, error) {
	f.rejected.Add(1)
	return f.result, nil
}

func (f *fakeApprovals) Cancel(_ context.Context, _ *auth.Principal, _, _ string) (*operations.Operation, error) {
	return f.result, nil
}

func (f *fakeApprovals) Get(_ context.Context, _ *auth.Principal, _ string) (*operations.Operation, error) {
	return f.result, nil
}

func (f *fakeApprovals) ApproveInline(_ context.Context, _ *auth.Principal, _ string) (*operations.Operation, error) {
	f.approvedInln.Add(1)
	return f.result, nil
}

func (f *fakeApprovals) AwaitOutcome(_ context.Context, _ string, _ time.Duration) (*operations.Operation, error) {
	f.awaited.Add(1)
	return f.result, nil
}

func (f *fakeApprovals) List(context.Context, *auth.Principal, string, []operations.OperationState, int) ([]*operations.Operation, error) {
	return nil, nil
}

// countingInline records whether the inline ceiling was consulted at all.
type countingInline struct {
	allow     bool
	consulted atomic.Int32
}

func (c *countingInline) AllowsInline(operations.RiskLevel) bool {
	c.consulted.Add(1)
	return c.allow
}

type channelParams struct {
	Channel string `json:"channel"`
}

type channelState struct {
	Channel string `json:"channel"`
}

type channelMutation struct{}

func (channelMutation) Plan(_ context.Context, p channelParams) (Plan[channelState], error) {
	return Plan[channelState]{
		Before:        channelState{Channel: "36"},
		Desired:       channelState{Channel: p.Channel},
		Preconditions: map[string]any{"channel": "36"},
		Changes:       []operations.Change{{Field: "channel", From: "36", To: p.Channel}},
		Impact:        "Clients on this radio briefly disconnect.",
	}, nil
}

func (channelMutation) Apply(context.Context, channelParams, Plan[channelState]) (ApplyResult, error) {
	return ApplyResult{}, nil
}

func (channelMutation) Observe(context.Context, channelParams) (channelState, error) {
	return channelState{Channel: "149"}, nil
}

// proposeThrough registers the mutation, mounts it, and calls the propose tool
// the way a client would.
func proposeThrough(t *testing.T, spec MutationSpec, svc ApprovalService, inline InlinePolicy) {
	t.Helper()
	r := newRegistry(Descriptor{Name: "cnmaestro", Version: "1.0.0", Title: "cnMaestro"})
	Mutation(r, spec, channelMutation{})
	if err := r.err(); err != nil {
		t.Fatalf("registration: %v", err)
	}

	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	gate := func(context.Context, string, auth.Capability) error { return nil }
	r.mutations[0].attach(srv, gate, svc, inline, noObserver{})

	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	defer ss.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	defer cs.Close()

	if _, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "cnmaestro_device_set_radio_channel",
		Arguments: map[string]any{"channel": "149"},
	}); err != nil {
		t.Fatalf("call tool: %v", err)
	}
}

func radioSpec(reversible bool) MutationSpec {
	return MutationSpec{
		Action: "device.set_radio_channel", Title: "Set radio channel",
		Description: "Sets the channel on one radio.",
		Risk:        operations.RiskLow, Reversible: reversible, Verifiable: true,
	}
}

// MutationSpec.Reversible existed and reached nothing. The auto-approval
// policy is what reads it, and it can only read it if the host carries the
// declaration into the proposal rather than leaving it on the spec.
func TestProposeTool_CarriesTheMutationsReversibility(t *testing.T) {
	for _, reversible := range []bool{true, false} {
		svc := &fakeApprovals{result: &operations.Operation{
			ID: "op_1", State: operations.StatePendingApproval,
			Plugin: "cnmaestro", Action: "device.set_radio_channel",
			Risk: operations.RiskLow,
		}}
		proposeThrough(t, radioSpec(reversible), svc, &countingInline{})

		if svc.proposed.Reversible != reversible {
			t.Errorf("reversible = %v, want %v", svc.proposed.Reversible, reversible)
		}
	}
}

// A change a standing rule already authorised has nobody left to ask. Raising
// the client's confirmation prompt anyway would put a question to a person
// whose answer changes nothing, which is worse than the interruption the whole
// feature exists to remove.
func TestResolveApproval_DoesNotAskAboutAnAlreadyAuthorisedChange(t *testing.T) {
	approved := &operations.Operation{
		ID: "op_1", State: operations.StateApproved,
		Plugin: "cnmaestro", Action: "device.set_radio_channel",
		Risk: operations.RiskLow, ApprovedBy: operations.PolicyActor,
		AuthorizedByRule: "routine-radio",
	}
	svc := &fakeApprovals{result: approved}
	inline := &countingInline{allow: true}

	view := resolveApproval(context.Background(), nil, svc, inline, approved)

	if inline.consulted.Load() != 0 {
		t.Error("the inline ceiling decides whether to ask; there is nobody to ask")
	}
	if svc.approvedInln.Load() != 0 {
		t.Error("an operation a rule authorised must not be approved a second time")
	}
	if svc.awaited.Load() != 1 {
		t.Errorf("the caller waited %d times, want 1: the outcome is what is still owed",
			svc.awaited.Load())
	}
	if view.AuthorizedByRule != "routine-radio" {
		t.Errorf("authorized_by_rule = %q, want routine-radio", view.AuthorizedByRule)
	}
}

// A proposal still awaiting a decision is unaffected: the ordinary path is
// exactly as it was.
func TestResolveApproval_StillAsksAboutAPendingChange(t *testing.T) {
	pending := &operations.Operation{
		ID: "op_1", State: operations.StatePendingApproval,
		Plugin: "cnmaestro", Action: "device.set_radio_channel",
		Risk: operations.RiskLow,
	}
	svc := &fakeApprovals{result: pending}
	inline := &countingInline{allow: true}

	resolveApproval(context.Background(), nil, svc, inline, pending)

	if inline.consulted.Load() == 0 {
		t.Error("a pending change must still be judged against the inline ceiling")
	}
}

// The note is what a model repeats to a person. "This was approved" said about
// a change no human ever saw describes a decision that was not made; naming
// the rule is what turns it back into something true.
func TestNoteFor_SaysWhenNobodyWasAsked(t *testing.T) {
	op := &operations.Operation{
		State: operations.StateSucceeded, Verifiable: true,
		Preconditions:    json.RawMessage(`{"channel":"36"}`),
		OutcomeVerified:  boolPtr(true),
		AuthorizedByRule: "routine-radio",
	}
	note := noteFor(op)
	for _, want := range []string{
		"Applied and confirmed by re-reading",
		"Nobody was asked",
		"routine-radio",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("note %q does not mention %q", note, want)
		}
	}

	op.AuthorizedByRule = ""
	if strings.Contains(noteFor(op), "Nobody was asked") {
		t.Error("a change a person approved must not be described as unasked")
	}
}

func boolPtr(b bool) *bool { return &b }
