// Command hello is a complete mcpd plugin in one file.
//
// Build it and drop it into the plugins directory:
//
//	go build -o /var/lib/mcpd/plugins/hello/hello ./examples/hello
//	cat > /var/lib/mcpd/plugins/hello/plugin.json <<'JSON'
//	{"name": "hello", "exec": "hello"}
//	JSON
//
// mcpd mounts it at /mcp/hello on the next restart. Nothing about the host
// changes.
package main

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/spoked/mcpd/sdk"
)

func main() {
	p := sdk.New("hello", "1.0.0", "Hello",
		"A worked example of an out-of-process mcpd plugin. It keeps a greeting "+
			"in memory and lets you read it immediately or change it with approval.")

	state := &greeting{value: "hello"}

	sdk.Tool(p, sdk.ToolSpec{
		Name:        "greet",
		Title:       "Greet someone",
		Description: "Returns the current greeting addressed to a name.",
		Idempotent:  true,
	}, func(_ context.Context, in GreetInput) (GreetOutput, error) {
		if strings.TrimSpace(in.Name) == "" {
			return GreetOutput{}, fmt.Errorf("name must not be empty")
		}
		return GreetOutput{Message: state.get() + ", " + in.Name}, nil
	})

	sdk.RegisterMutation(p, sdk.MutationSpec{
		Action:      "greeting.set",
		Title:       "Change the greeting",
		Description: "Changes the greeting used by the greet tool.",
		Risk:        sdk.RiskLow,
		Reversible:  true,
	}, &setGreeting{state: state})

	p.OnHealth(func(context.Context) sdk.Health { return sdk.Healthy() })

	sdk.Serve(p)
}

// GreetInput is the greet tool's parameters.
type GreetInput struct {
	Name string `json:"name" jsonschema:"who to greet"`
}

// GreetOutput is the greet tool's result.
type GreetOutput struct {
	Message string `json:"message"`
}

// SetGreetingParams is the mutation's parameters.
type SetGreetingParams struct {
	Greeting string `json:"greeting" jsonschema:"the new greeting word, e.g. \"hello\" or \"howdy\""`
}

// GreetingState is what the mutation observes.
type GreetingState struct {
	Greeting string `json:"greeting"`
}

type greeting struct {
	mu    sync.RWMutex
	value string
}

func (g *greeting) get() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.value
}

func (g *greeting) set(v string) {
	g.mu.Lock()
	g.value = v
	g.mu.Unlock()
}

// setGreeting implements the three-phase mutation contract.
type setGreeting struct{ state *greeting }

// Plan validates and captures current state. It changes nothing, and runs both
// at proposal and again immediately before Apply so the host can detect drift.
func (s *setGreeting) Plan(_ context.Context, in SetGreetingParams) (sdk.Plan[GreetingState], error) {
	var zero sdk.Plan[GreetingState]

	value := strings.TrimSpace(in.Greeting)
	if value == "" {
		return zero, fmt.Errorf("greeting must not be empty")
	}
	if len(value) > 32 {
		return zero, fmt.Errorf("greeting must be at most 32 characters")
	}

	current := s.state.get()
	if current == value {
		return zero, fmt.Errorf("the greeting is already %q", value)
	}

	return sdk.Plan[GreetingState]{
		Before:        GreetingState{Greeting: current},
		Desired:       GreetingState{Greeting: value},
		Preconditions: map[string]any{"greeting": current},
		Changes: []sdk.Change{
			{Field: "greeting", From: current, To: value},
		},
		Impact:   "Changes the wording returned by hello_greet. Nothing else is affected.",
		Rollback: SetGreetingParams{Greeting: current},
	}, nil
}

// Apply performs the change, at most once, and only after approval.
func (s *setGreeting) Apply(_ context.Context, in SetGreetingParams, _ sdk.Plan[GreetingState]) (sdk.ApplyResult, error) {
	s.state.set(strings.TrimSpace(in.Greeting))
	return sdk.ApplyResult{UpstreamRef: "in-memory"}, nil
}

// Observe reads state back so the host can verify the change took effect.
func (s *setGreeting) Observe(context.Context, SetGreetingParams) (GreetingState, error) {
	return GreetingState{Greeting: s.state.get()}, nil
}
