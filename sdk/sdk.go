// Package sdk builds mcpd plugins.
//
// A plugin is an ordinary Go program. It declares its tools and mutations,
// calls Serve, and the SDK handles the wire protocol, dispatch, parameter
// decoding, and schema generation.
//
// The smallest useful plugin:
//
//	func main() {
//		p := sdk.New("weather", "1.0.0", "Weather", "Reads local weather.")
//
//		sdk.Tool(p, sdk.ToolSpec{
//			Name:        "forecast",
//			Description: "Get the forecast for a city.",
//		}, func(ctx context.Context, in ForecastInput) (Forecast, error) {
//			return lookup(ctx, in.City)
//		})
//
//		sdk.Serve(p)
//	}
//
// Drop the compiled binary and a plugin.json into the plugins directory and
// mcpd mounts it at /mcp/weather. Nothing about the host needs to change.
//
// # Settings
//
// A plugin declares what it needs configured and the host does the rest:
// renders the form, validates what is typed, encrypts the secrets, and hands
// back resolved values.
//
//	sdk.Settings(p,
//		sdk.SettingField{Key: "api_token", Label: "API token", Kind: sdk.KindSecret, Required: true},
//		sdk.SettingField{Key: "host", Label: "Address", Kind: sdk.KindString},
//	)
//
//	token, ok := p.Configured("api_token")
//
// A plugin never reads a file, an environment variable, or a credential
// reference. What it receives is the value, whichever of those it came from,
// which is one fewer thing every plugin has to get right.
//
// # Resources and prompts
//
// A tool is an action a model chooses and reasons about choosing. Two other
// things are worth expressing differently.
//
// A resource is reference material read by address — a config dump, a
// topology, a status document. Keeping it out of the tool catalogue matters
// because every tool costs the model attention on every call.
//
//	sdk.Resource(p, sdk.ResourceSpec{
//		Path: "state", Name: "state", Description: "Current state.",
//	}, func(ctx context.Context) (string, error) { return dump(ctx) })
//
// A prompt is the integration saying "here is how to ask me something useful".
// Diagnosing a device is a sequence of reads and a way of reading them; the
// reads are tools, and the sequence is knowledge that otherwise lives only in
// whoever wrote the plugin. A prompt returns text and performs nothing.
//
//	sdk.Prompt(p, sdk.PromptSpec{
//		Name: "diagnose", Description: "Work through an offline device.",
//		Args: []sdk.PromptArg{{Name: "mac", Required: true}},
//	}, func(ctx context.Context, args map[string]string) (string, error) { ... })
//
// # What the host guarantees
//
// Everything a plugin would otherwise have to get right itself: authentication,
// per-plugin authorization, the approval workflow, the audit trail, correlation
// IDs, and at-most-once execution. A plugin cannot reach operation state, so it
// cannot corrupt it.
//
// # Mutations
//
// A mutation is registered as three methods rather than one, and the split is
// what makes safe execution possible:
//
//   - Plan validates parameters and reads current state. It runs twice -- once
//     when the change is proposed and again immediately before it is applied --
//     and the host compares the two. Because it is the same code both times,
//     the diff a human approves and the state checked at execution cannot
//     drift apart. It must not mutate anything.
//
//   - Apply performs the write. The host calls it at most once per attempt,
//     only after a human approved the exact payload that was proposed.
//
//   - Observe re-reads state so the host can confirm the change actually took
//     effect. An HTTP 200 is not evidence that anything changed.
//
// # The one rule that matters most
//
// If Apply cannot establish whether the write landed -- a timeout, a dropped
// connection, a 5xx -- it must return Indeterminate. Returning an ordinary
// error tells the host the write did not happen, and the host may then let the
// change be retried, applying it twice. Returning Indeterminate stops that: the
// operation is recorded as unknown and a human resolves it.
package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sync"
)

// protocolVersion must match the host's. It is set by the SDK, not by plugin
// authors.
const protocolVersion = "1"

var (
	toolNamePattern   = regexp.MustCompile(`^[a-z][a-z0-9_]{1,47}$`)
	actionPattern     = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
	pluginNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)
)

// Risk classifies the blast radius of a mutation. The host may raise it by
// policy; nothing lowers it.
type Risk string

const (
	RiskLow      Risk = "low"
	RiskMedium   Risk = "medium"
	RiskHigh     Risk = "high"
	RiskCritical Risk = "critical"
)

// Health is a plugin's view of its own upstream.
type Health struct {
	State string `json:"state"`
	// Message appears on the host's unauthenticated readiness endpoint, so it
	// must never contain credentials or a URL with embedded userinfo.
	Message string `json:"message,omitempty"`
}

// Healthy reports full operation.
func Healthy() Health { return Health{State: "healthy"} }

// Degraded reports impaired but usable operation, such as reads working while
// writes are unreachable.
func Degraded(msg string) Health { return Health{State: "degraded", Message: msg} }

// Unhealthy reports that the plugin cannot serve.
func Unhealthy(msg string) Health { return Health{State: "unhealthy", Message: msg} }

// Change is a field-level difference shown to whoever approves a mutation.
//
// It must describe the change completely. Someone shown one field while five
// change has not meaningfully approved anything.
type Change struct {
	Field string `json:"field"`
	From  any    `json:"from"`
	To    any    `json:"to"`
}

// Plan is what a mutation produces before anything is changed.
type Plan[S any] struct {
	// Before is the observed current state.
	Before S
	// Desired is the state Apply intends to produce. The host verifies against
	// it; leave it zero when there is no single target state, as with a reboot.
	Desired S
	// Preconditions is re-checked immediately before the write. Prefer an
	// ETag, revision, or generation where the upstream provides one; otherwise
	// snapshot the specific fields the change depends on.
	Preconditions map[string]any
	// Changes is the diff a human reads.
	Changes []Change
	// Impact is plain language about the consequence, e.g. "Clients on this
	// radio will briefly disconnect."
	Impact string
	// Rollback holds parameters for the inverse mutation, if one exists. A
	// rollback is itself approval-gated; it is never an automatic undo.
	Rollback any
	// RiskOverride raises risk for these specific parameters. It cannot lower
	// the declared risk.
	RiskOverride Risk
	// State carries anything Apply needs that does not belong in the
	// parameters. The host stores it opaquely and hands it back.
	State any
}

// ApplyResult reports what a write produced.
type ApplyResult struct {
	// UpstreamRef is a job or change id, used to reconcile an unknown outcome.
	UpstreamRef string
	// Async reports that the upstream accepted the request but has not applied
	// it yet, so success requires Observe rather than a status code.
	Async bool
}

// Mutation is the three-phase contract for an approval-gated write.
type Mutation[P, S any] interface {
	Plan(ctx context.Context, params P) (Plan[S], error)
	Apply(ctx context.Context, params P, plan Plan[S]) (ApplyResult, error)
	Observe(ctx context.Context, params P) (S, error)
}

// ToolSpec describes a read-only tool.
type ToolSpec struct {
	// Name is bare; the host prefixes it with the plugin name.
	Name string
	// Title is a human-readable label.
	Title string
	// Description tells the model what the tool does and when to use it. The
	// model relies on it to choose correctly, so it is required.
	Description string
	// Idempotent marks a tool whose repeated invocation has no extra effect.
	Idempotent bool
	// Capability is what a caller must hold to invoke this tool. Empty means
	// read, which is what a tool is unless it says otherwise.
	//
	// For the read that is not merely a read: a credential dump, a billing
	// figure, anything where seeing it is itself the privilege.
	Capability string
	// RateLimit bounds calls to this tool, in requests per second. Zero is
	// unbounded. Per tool rather than per plugin, because the expensive call
	// is usually one endpoint rather than an integration.
	RateLimit float64
}

// MutationSpec describes an approval-gated write.
type MutationSpec struct {
	// Action is the stable identifier, e.g. "device.set_radio_channel". It is
	// persisted on every operation and must not change once operations
	// referencing it exist.
	Action string
	Title  string
	// Description should say plainly that calling it changes nothing until
	// someone approves.
	Description string
	Risk        Risk
	// Reversible reports whether a rollback mutation can be derived. A
	// rollback is itself approval-gated, never an automatic undo.
	//
	// It is also the floor under automatic authorisation: the host never lets
	// a standing rule approve a mutation that declares no way back, whatever
	// the rule says. False by default, so a spec that forgets to say claims
	// nothing.
	Reversible bool
	// Verifiable declares that Observe, run after Apply, confirms the outcome:
	// what it returns can be compared against the plan's Desired state and the
	// comparison means something.
	//
	// False by default, and the host believes it. A mutation that says nothing
	// is settled as applied-but-unconfirmed rather than being reported to the
	// model as "confirmed by re-reading the target", which is what the host
	// used to say about every mutation whether or not anything was compared.
	// Say true only if Observe really does read back what Apply wrote.
	Verifiable bool
	// RateLimit bounds how often one caller may propose this mutation, in
	// requests per second. Zero takes the host's default, which is a real
	// limit rather than an absence.
	//
	// There is no value meaning unbounded, and that is deliberate. A standing
	// rule on the host can authorise a class of change with nobody being
	// asked, so the only thing standing between an agent in a retry loop and a
	// stream of writes against live equipment is this. Raise it for a cheap
	// change; lower it for one that takes a site down.
	RateLimit float64
}

// Plugin holds a plugin's declarations.
type Plugin struct {
	name        string
	version     string
	title       string
	description string

	mu        sync.RWMutex
	tools     map[string]*registeredTool
	mutations map[string]*registeredMutation
	order     []string
	mutOrder  []string

	resources   map[string]*registeredResource
	resOrder    []string
	prompts     map[string]*registeredPrompt
	promptOrder []string

	// settings is what this plugin needs configured; config is what the host
	// resolved and handed back. A plugin declares the first and reads the
	// second, and never has to know where a value came from.
	settings []SettingField
	config   map[string]string

	healthFn   func(context.Context) Health
	shutdownFn func(context.Context) error

	errs []error
}

// New creates a plugin.
func New(name, version, title, description string) *Plugin {
	p := &Plugin{
		name: name, version: version, title: title, description: description,
		tools:     make(map[string]*registeredTool),
		mutations: make(map[string]*registeredMutation),
	}
	if !pluginNamePattern.MatchString(name) {
		p.errs = append(p.errs, fmt.Errorf(
			"plugin name %q must match %s; it becomes a URL path segment", name, pluginNamePattern))
	}
	if version == "" {
		p.errs = append(p.errs, fmt.Errorf("plugin %s requires a version", name))
	}
	return p
}

// OnHealth registers a health check. Without one the plugin reports healthy
// whenever its process is running.
func (p *Plugin) OnHealth(fn func(context.Context) Health) {
	p.mu.Lock()
	p.healthFn = fn
	p.mu.Unlock()
}

// OnShutdown registers cleanup, called when the host stops the plugin.
func (p *Plugin) OnShutdown(fn func(context.Context) error) {
	p.mu.Lock()
	p.shutdownFn = fn
	p.mu.Unlock()
}

type registeredTool struct {
	spec        ToolSpec
	inputSchema json.RawMessage
	invoke      func(context.Context, json.RawMessage) (json.RawMessage, error)
}

type registeredMutation struct {
	spec        MutationSpec
	inputSchema json.RawMessage
	plan        func(context.Context, json.RawMessage) (wirePlan, error)
	apply       func(context.Context, json.RawMessage, json.RawMessage) (ApplyResult, error)
	observe     func(context.Context, json.RawMessage) (json.RawMessage, error)
}

// wirePlan is a Plan with its type parameter erased, ready for the wire.
type wirePlan struct {
	Before        json.RawMessage `json:"before,omitempty"`
	Desired       json.RawMessage `json:"desired,omitempty"`
	Preconditions json.RawMessage `json:"preconditions,omitempty"`
	Changes       []Change        `json:"changes,omitempty"`
	Impact        string          `json:"impact"`
	Rollback      json.RawMessage `json:"rollback,omitempty"`
	RiskOverride  string          `json:"risk_override,omitempty"`
	State         json.RawMessage `json:"state,omitempty"`
}

// Tool registers a read-only tool.
//
// The input schema is derived from In, so a caller cannot reach the handler
// with parameters it cannot accept.
func Tool[In, Out any](p *Plugin, spec ToolSpec, fn func(context.Context, In) (Out, error)) {
	if !toolNamePattern.MatchString(spec.Name) {
		p.addErr(fmt.Errorf("tool name %q must match %s", spec.Name, toolNamePattern))
		return
	}
	if spec.Description == "" {
		p.addErr(fmt.Errorf("tool %q requires a description; the model relies on it "+
			"to choose correctly", spec.Name))
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if _, dup := p.tools[spec.Name]; dup {
		p.errs = append(p.errs, fmt.Errorf("tool %q is registered twice", spec.Name))
		return
	}

	p.tools[spec.Name] = &registeredTool{
		spec:        spec,
		inputSchema: schemaFor[In](),
		invoke: func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
			var in In
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &in); err != nil {
					return nil, invalidParams("could not decode parameters: %v", err)
				}
			}
			out, err := fn(ctx, in)
			if err != nil {
				return nil, err
			}
			return json.Marshal(out)
		},
	}
	p.order = append(p.order, spec.Name)
}

// RegisterMutation registers an approval-gated write.
//
// This does not create a tool that performs the write. It creates one that
// proposes it: the host records the intent and returns an operation id, and
// nothing happens upstream until a human approves.
func RegisterMutation[P, S any](p *Plugin, spec MutationSpec, m Mutation[P, S]) {
	if !actionPattern.MatchString(spec.Action) {
		p.addErr(fmt.Errorf("mutation action %q must match %s", spec.Action, actionPattern))
		return
	}
	if spec.Description == "" {
		p.addErr(fmt.Errorf("mutation %q requires a description", spec.Action))
		return
	}
	if !validRisk(spec.Risk) {
		p.addErr(fmt.Errorf("mutation %q declares invalid risk %q; use low, medium, high or critical",
			spec.Action, spec.Risk))
		return
	}
	if spec.RateLimit < 0 {
		p.addErr(fmt.Errorf("mutation %q declares a negative rate limit; "+
			"leave it zero to take the host's default", spec.Action))
		return
	}
	if m == nil {
		p.addErr(fmt.Errorf("mutation %q has a nil handler", spec.Action))
		return
	}

	decode := func(raw json.RawMessage) (P, error) {
		var params P
		if len(raw) == 0 {
			return params, invalidParams("parameters are required")
		}
		if err := json.Unmarshal(raw, &params); err != nil {
			return params, invalidParams("could not decode parameters: %v", err)
		}
		return params, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if _, dup := p.mutations[spec.Action]; dup {
		p.errs = append(p.errs, fmt.Errorf("mutation %q is registered twice", spec.Action))
		return
	}

	p.mutations[spec.Action] = &registeredMutation{
		spec:        spec,
		inputSchema: schemaFor[P](),

		plan: func(ctx context.Context, raw json.RawMessage) (wirePlan, error) {
			params, err := decode(raw)
			if err != nil {
				return wirePlan{}, err
			}
			plan, err := m.Plan(ctx, params)
			if err != nil {
				return wirePlan{}, err
			}
			return erase(plan)
		},

		apply: func(ctx context.Context, raw, state json.RawMessage) (ApplyResult, error) {
			params, err := decode(raw)
			if err != nil {
				return ApplyResult{}, err
			}
			// Apply receives the plan's opaque state back. The typed portions
			// are re-derived from parameters, which the host guarantees are
			// byte-identical to what was approved.
			var plan Plan[S]
			if len(state) > 0 {
				_ = json.Unmarshal(state, &plan.State)
			}
			return m.Apply(ctx, params, plan)
		},

		observe: func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
			params, err := decode(raw)
			if err != nil {
				return nil, err
			}
			observed, err := m.Observe(ctx, params)
			if err != nil {
				return nil, err
			}
			return json.Marshal(observed)
		},
	}
	p.mutOrder = append(p.mutOrder, spec.Action)
}

func erase[S any](p Plan[S]) (wirePlan, error) {
	out := wirePlan{Changes: p.Changes, Impact: p.Impact}

	for _, pair := range []struct {
		value  any
		target *json.RawMessage
	}{
		{p.Before, &out.Before},
		{p.Desired, &out.Desired},
		{p.Preconditions, &out.Preconditions},
		{p.Rollback, &out.Rollback},
		{p.State, &out.State},
	} {
		if pair.value == nil {
			continue
		}
		encoded, err := json.Marshal(pair.value)
		if err != nil {
			return wirePlan{}, fmt.Errorf("could not encode part of the plan: %w", err)
		}
		*pair.target = encoded
	}

	if validRisk(p.RiskOverride) {
		out.RiskOverride = string(p.RiskOverride)
	}
	return out, nil
}

func validRisk(r Risk) bool {
	switch r {
	case RiskLow, RiskMedium, RiskHigh, RiskCritical:
		return true
	}
	return false
}

func (p *Plugin) addErr(err error) {
	p.mu.Lock()
	p.errs = append(p.errs, err)
	p.mu.Unlock()
}
