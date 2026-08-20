// Package echo is a minimal reference plugin.
//
// It exists to exercise the host end to end — registration, endpoint routing,
// per-plugin authorization, tool dispatch — without depending on an external
// system. It is the template a real integration follows, and it is what the
// host's integration tests run against.
package echoplugin

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/spoked/mcpd/internal/operations"
	"github.com/spoked/mcpd/internal/plugins"
)

// Plugin is the echo integration.
type Plugin struct {
	deps  plugins.Deps
	start time.Time

	mu           sync.RWMutex
	currentLabel string
}

// New constructs the plugin.
func New(deps plugins.Deps) *Plugin {
	return &Plugin{deps: deps, start: deps.Now()}
}

// Descriptor implements plugins.Plugin.
func (p *Plugin) Descriptor() plugins.Descriptor {
	return plugins.Descriptor{
		Name:    "echo",
		Version: "1.0.0",
		Title:   "Echo",
		Description: "A test integration for checking that a connection works. " +
			"It has one read tool and one harmless change you can practise " +
			"approving. It touches nothing outside mcpd.",
	}
}

// EchoInput is the argument to the echo tool.
type EchoInput struct {
	Message string `json:"message" jsonschema:"the text to echo back"`
}

// EchoOutput is the echo tool's result.
type EchoOutput struct {
	Message string `json:"message"`
	Length  int    `json:"length"`
}

// StatusInput takes no arguments.
type StatusInput struct{}

// StatusOutput reports host-visible state.
type StatusOutput struct {
	Plugin   string `json:"plugin"`
	Version  string `json:"version"`
	Label    string `json:"label"`
	UptimeMS int64  `json:"uptime_ms"`
}

// Register implements plugins.Plugin.
func (p *Plugin) Register(_ context.Context, r *plugins.Registry) error {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "echo",
		Title: "Echo a message",
		Description: "Returns the supplied message unchanged, with its length. " +
			"Use this to verify that the connection and credentials work.",
		Idempotent: true,
	}, func(_ context.Context, in EchoInput) (EchoOutput, error) {
		if strings.TrimSpace(in.Message) == "" {
			return EchoOutput{}, fmt.Errorf("message must not be empty")
		}
		return EchoOutput{Message: in.Message, Length: len(in.Message)}, nil
	})

	plugins.Mutation(r, plugins.MutationSpec{
		Action:      "label.set",
		Title:       "Set the echo label",
		Description: "Changes the label reported by echo_status.",
		Risk:        operations.RiskLow,
		Reversible:  true,
	}, &labelHandler{p: p})

	plugins.Tool(r, plugins.ToolSpec{
		Name:        "status",
		Title:       "Plugin status",
		Description: "Reports this plugin's version and how long it has been running.",
		Idempotent:  true,
	}, func(_ context.Context, _ StatusInput) (StatusOutput, error) {
		d := p.Descriptor()
		return StatusOutput{
			Plugin:   d.Name,
			Version:  d.Version,
			Label:    p.label(),
			UptimeMS: p.deps.Now().Sub(p.start).Milliseconds(),
		}, nil
	})

	return nil
}

// Check implements plugins.Checker. The echo plugin has no upstream
// dependency, so it is healthy whenever the process is.
func (p *Plugin) Check(context.Context) plugins.Health { return plugins.Healthy() }

// --- a mutation, so the approval path has something to exercise -------------

// SetLabelParams changes the plugin's stored label.
type SetLabelParams struct {
	Label string `json:"label" jsonschema:"the new label"`
}

// LabelState is the observable state the mutation acts on.
type LabelState struct {
	Label string `json:"label"`
}

// labelHandler implements the three-phase mutation contract against the
// plugin's own in-memory state. It is a stand-in for a real upstream API, and
// exists so the approval, execution and verification path can be exercised
// without one.
type labelHandler struct {
	p *Plugin
}

// Plan reads current state and describes the change. It mutates nothing, and
// runs both at proposal and again immediately before Apply.
func (h *labelHandler) Plan(_ context.Context, params SetLabelParams) (plugins.Plan[LabelState], error) {
	if strings.TrimSpace(params.Label) == "" {
		return plugins.Plan[LabelState]{}, fmt.Errorf("label must not be empty")
	}
	current := h.p.label()
	return plugins.Plan[LabelState]{
		Before:  LabelState{Label: current},
		Desired: LabelState{Label: params.Label},
		Preconditions: map[string]any{
			"label": current,
		},
		Changes: []operations.Change{
			{Field: "label", From: current, To: params.Label},
		},
		Impact:   "Changes the label reported by echo_status. Harmless; it exists to exercise the approval path.",
		Rollback: SetLabelParams{Label: current},
	}, nil
}

// Apply performs the change. It is called at most once per attempt, only by
// the executor, only after the state machine granted a claim.
func (h *labelHandler) Apply(_ context.Context, params SetLabelParams, _ plugins.Plan[LabelState]) (plugins.ApplyResult, error) {
	h.p.setLabel(params.Label)
	return plugins.ApplyResult{UpstreamRef: "echo-local"}, nil
}

// Observe re-reads state for verification.
func (h *labelHandler) Observe(_ context.Context, _ SetLabelParams) (LabelState, error) {
	return LabelState{Label: h.p.label()}, nil
}

func (p *Plugin) label() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.currentLabel == "" {
		return "default"
	}
	return p.currentLabel
}

func (p *Plugin) setLabel(v string) {
	p.mu.Lock()
	p.currentLabel = v
	p.mu.Unlock()
}
