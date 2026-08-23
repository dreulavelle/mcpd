// Command planstate is a test plugin whose apply reports the plan state it was
// handed.
//
// It exists for one defect. The host used to drop the plan argument and read
// the plugin's opaque state out of a process-local map keyed on the action and
// the parameters, which two live proposals of the same change share: the first
// apply took the entry and the second silently received nothing. Making the
// state visible in the apply result is what lets a test see whose plan
// arrived.
package main

import (
	"context"
	"fmt"
	"strconv"
	"sync/atomic"

	"github.com/spoked/mcpd/sdk"
)

func main() {
	p := sdk.New("planstate", "1.0.0", "Plan state",
		"Reports the plan state each apply was given.")

	sdk.Tool(p, sdk.ToolSpec{
		Name:        "ping",
		Title:       "Ping",
		Description: "Returns ok, so the plugin exposes a readable tool.",
		Idempotent:  true,
	}, func(context.Context, struct{}) (map[string]string, error) {
		return map[string]string{"status": "ok"}, nil
	})

	sdk.RegisterMutation(p, sdk.MutationSpec{
		Action:      "thing.set",
		Title:       "Set the thing",
		Description: "Records a value, and reports which plan the write used.",
		Risk:        sdk.RiskLow,
		Verifiable:  true,
	}, &handler{})

	p.OnHealth(func(context.Context) sdk.Health { return sdk.Healthy() })
	sdk.Serve(p)
}

// Params is the mutation's parameter object. Two proposals carrying the same
// value are the case that used to cross.
type Params struct {
	Value string `json:"value" jsonschema:"the value to set"`
}

// State is what observe returns.
type State struct {
	Value string `json:"value"`
}

// planCounter makes every plan distinguishable even when the parameters are
// identical.
var planCounter atomic.Int64

type handler struct{}

func (h *handler) Plan(_ context.Context, in Params) (sdk.Plan[State], error) {
	ticket := "plan-" + strconv.FormatInt(planCounter.Add(1), 10)
	return sdk.Plan[State]{
		Before:        State{Value: ""},
		Desired:       State{Value: in.Value},
		Preconditions: map[string]any{"value": ""},
		Changes:       []sdk.Change{{Field: "value", From: "", To: in.Value}},
		Impact:        "Records a value in memory.",
		State:         map[string]string{"ticket": ticket},
	}, nil
}

func (h *handler) Apply(_ context.Context, _ Params, plan sdk.Plan[State]) (sdk.ApplyResult, error) {
	carried, ok := plan.State.(map[string]any)
	if !ok {
		return sdk.ApplyResult{}, fmt.Errorf("apply received no plan state (%T)", plan.State)
	}
	ticket, _ := carried["ticket"].(string)
	if ticket == "" {
		return sdk.ApplyResult{}, fmt.Errorf("apply received a plan with no ticket")
	}
	return sdk.ApplyResult{UpstreamRef: ticket}, nil
}

func (h *handler) Observe(context.Context, Params) (State, error) {
	return State{}, nil
}
