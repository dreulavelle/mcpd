package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/observability"
	"github.com/spoked/mcpd/internal/operations"
)

// Registry collects what a plugin declares during Register.
//
// It is a concrete type rather than an interface because registration uses
// generics, and Go interfaces cannot carry type parameters. The registration
// entry points are therefore the free functions Tool and Mutation below.
type Registry struct {
	descriptor Descriptor
	tools      []registeredTool
	mutations  []registeredMutation
	resources  []registeredResource
	prompts    []registeredPrompt
	errs       []error
}

// newRegistry is called by the host, not by plugins.
func newRegistry(d Descriptor) *Registry { return &Registry{descriptor: d} }

// Descriptor returns the owning plugin's identity, so a plugin can build tool
// descriptions without duplicating its own name.
func (r *Registry) Descriptor() Descriptor { return r.descriptor }

// ToolSpec describes a read-only tool.
type ToolSpec struct {
	// Name is the bare tool name; the host prefixes it with the plugin name so
	// that two plugins can both expose "list_devices" without colliding.
	Name string
	// Title is the human-readable label.
	Title string
	// Description tells the model what the tool does and when to use it.
	Description string
	// Idempotent marks a tool whose repeated invocation has no additional
	// effect. Read tools are idempotent by definition; this exists for the
	// annotation.
	Idempotent bool

	// Capability is what a caller must hold to invoke this tool. Empty means
	// read, which is what a tool registered here is unless it says otherwise.
	//
	// It exists for the read that is not merely a read: a credential dump, a
	// billing figure, anything where seeing it is itself the privilege. Such a
	// tool can ask for more without becoming a mutation, which it is not --
	// nothing changes, so there is nothing to approve.
	Capability auth.Capability

	// RateLimit bounds calls to this tool, in requests per second. Zero leaves
	// it unbounded.
	//
	// Per tool rather than per plugin, because the one expensive call is
	// usually one endpoint rather than an integration. Bounding the plugin to
	// protect it would slow every cheap call beside it.
	RateLimit float64

	// InputSchema overrides the schema derived from the handler's parameter
	// type.
	//
	// An in-tree plugin leaves this empty and the schema is inferred. An
	// out-of-process plugin must set it: its parameters arrive as raw JSON,
	// which would otherwise be described as a byte array rather than the
	// object it actually carries.
	InputSchema json.RawMessage
}

func (s ToolSpec) validate(plugin string) error {
	if s.Capability != "" && !s.Capability.Valid() {
		return fmt.Errorf("plugins: %s tool %q has unknown capability %q",
			plugin, s.Name, s.Capability)
	}
	if s.RateLimit < 0 {
		return fmt.Errorf("plugins: %s tool %q has a negative rate limit", plugin, s.Name)
	}
	if !toolNamePattern.MatchString(s.Name) {
		return fmt.Errorf("plugins: %s tool name %q must match %s",
			plugin, s.Name, toolNamePattern)
	}
	if s.Description == "" {
		return fmt.Errorf("plugins: %s tool %q requires a description; "+
			"the model relies on it to choose correctly", plugin, s.Name)
	}
	return nil
}

// MutationSpec describes an approval-gated write.
type MutationSpec struct {
	// Action is the stable mutation identifier, e.g.
	// "device.set_radio_channel". It is persisted on every operation and must
	// not change once operations referencing it exist.
	Action string
	// Title is the human-readable label.
	Title string
	// Description tells the model what proposing this mutation will do — and
	// should state plainly that it changes nothing until approved.
	Description string
	// Risk is the default classification. Policy may raise it; a Plan may
	// raise it for specific parameters. Nothing lowers it.
	Risk operations.RiskLevel
	// Reversible reports whether a rollback operation can be derived. A
	// rollback is itself an approval-gated mutation, never an automatic undo.
	Reversible bool

	// InputSchema overrides the schema derived from the handler's parameter
	// type, for the same reason as ToolSpec.InputSchema.
	InputSchema json.RawMessage
}

func (s MutationSpec) validate(plugin string) error {
	if !actionPattern.MatchString(s.Action) {
		return fmt.Errorf("plugins: %s mutation action %q must match %s",
			plugin, s.Action, actionPattern)
	}
	if s.Description == "" {
		return fmt.Errorf("plugins: %s mutation %q requires a description", plugin, s.Action)
	}
	if !s.Risk.Valid() {
		return fmt.Errorf("plugins: %s mutation %q has invalid risk %q",
			plugin, s.Action, s.Risk)
	}
	return nil
}

// Tool registers a read-only tool.
//
// The host wraps the handler with authorization, rate limiting, correlation
// IDs, audit, and panic recovery before it reaches the transport, so plugin
// handlers contain only their own logic.
func Tool[In, Out any](r *Registry, spec ToolSpec, fn func(context.Context, In) (Out, error)) {
	if err := spec.validate(r.descriptor.Name); err != nil {
		r.errs = append(r.errs, err)
		return
	}
	if r.hasTool(spec.Name) {
		r.errs = append(r.errs, fmt.Errorf(
			"plugins: %s registers tool %q twice", r.descriptor.Name, spec.Name))
		return
	}

	var in In
	var out Out
	for _, check := range []struct {
		role string
		typ  reflect.Type
	}{{"input", reflect.TypeOf(in)}, {"output", reflect.TypeOf(out)}} {
		if err := checkSchemaType(r.descriptor.Name, spec.Name, check.role, check.typ); err != nil {
			r.errs = append(r.errs, err)
			return
		}
	}

	qualified := r.descriptor.Name + "_" + spec.Name
	openWorld := true
	capability := spec.Capability
	if capability == "" {
		capability = auth.CapRead
	}
	// A tool registered here changes nothing, whatever it takes to call it.
	// The annotation describes the effect, not the permission.
	readOnly := true
	limiter := newToolLimiter(spec.RateLimit)

	r.tools = append(r.tools, registeredTool{
		spec:       spec,
		qualified:  qualified,
		capability: capability,
		attach: func(s *mcp.Server, mw ToolMiddleware) {
			tool := &mcp.Tool{
				Name:        qualified,
				Description: spec.Description,
				Annotations: &mcp.ToolAnnotations{
					Title:          spec.Title,
					ReadOnlyHint:   readOnly,
					IdempotentHint: spec.Idempotent,
					OpenWorldHint:  &openWorld,
				},
			}
			if len(spec.InputSchema) > 0 {
				tool.InputSchema = spec.InputSchema
			}
			mcp.AddTool(s, tool, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
				var zero Out
				if err := mw(ctx, qualified, capability); err != nil {
					return nil, zero, err
				}
				if err := limiter.wait(ctx); err != nil {
					return nil, zero, err
				}
				out, err := fn(ctx, in)
				if err != nil {
					return nil, zero, err
				}
				return nil, out, nil
			})
		},
	})
}

// Mutation registers an approval-gated write.
//
// This does not create a tool that performs the write. It creates a tool that
// *proposes* it: the model can describe a change and have it recorded, but
// execution happens later, from the executor, only after the operations
// service says so.
func Mutation[P, S any](r *Registry, spec MutationSpec, h MutationHandler[P, S]) {
	if err := spec.validate(r.descriptor.Name); err != nil {
		r.errs = append(r.errs, err)
		return
	}
	if h == nil {
		r.errs = append(r.errs, fmt.Errorf(
			"plugins: %s mutation %q has a nil handler", r.descriptor.Name, spec.Action))
		return
	}
	if r.hasMutation(spec.Action) {
		r.errs = append(r.errs, fmt.Errorf(
			"plugins: %s registers mutation %q twice", r.descriptor.Name, spec.Action))
		return
	}
	adapter := newAdapter(h)
	qualified := r.descriptor.Name + "_" + strings.ReplaceAll(spec.Action, ".", "_")

	r.mutations = append(r.mutations, registeredMutation{
		spec:      spec,
		plugin:    r.descriptor.Name,
		qualified: qualified,
		adapter:   adapter,
		attach: func(s *mcp.Server, gate ToolMiddleware, svc ApprovalService, inline InlinePolicy) {
			attachProposeTool[P](s, r.descriptor.Name, qualified, spec, adapter, gate, svc, inline)
		},
	})
}

// attachProposeTool registers the tool that proposes a mutation.
//
// It is emphatically not the tool that performs one. The description says so,
// the annotations say so, and the returned operation says so in a note field,
// because a model that reads only a state string can still mistake "proposed"
// for "done".
func attachProposeTool[P any](srv *mcp.Server, plugin, qualified string, spec MutationSpec, adapter *handlerAdapter, gate ToolMiddleware, svc ApprovalService, inline InlinePolicy) {
	mutating := false
	description := spec.Description + "\n\n" +
		"IMPORTANT: this only records a proposal. Nothing changes until a human " +
		"approves it with " + plugin + "_approve_operation. This call returns an " +
		"operation_id and leaves the system untouched."

	tool := &mcp.Tool{
		Name:        qualified,
		Description: description,
		Annotations: &mcp.ToolAnnotations{
			Title: spec.Title,
			// Proposing genuinely changes nothing upstream, but it is not a
			// read either: it creates a durable record a human must act on.
			// Marking it non-destructive and non-idempotent is the honest
			// reading.
			ReadOnlyHint:    false,
			DestructiveHint: &mutating,
			IdempotentHint:  false,
		},
	}
	if len(spec.InputSchema) > 0 {
		tool.InputSchema = spec.InputSchema
	}

	mcp.AddTool(srv, tool, func(ctx context.Context, req *mcp.CallToolRequest, in P) (*mcp.CallToolResult, operationView, error) {
		if err := gate(ctx, qualified, auth.CapPropose); err != nil {
			return nil, operationView{}, err
		}

		params, err := json.Marshal(in)
		if err != nil {
			return nil, operationView{}, fmt.Errorf("encode parameters: %w", err)
		}

		// Plan runs here to capture the current state and build the diff the
		// approver will read. It runs again before execution, and the two are
		// compared -- which is what makes drift detectable.
		plan, err := adapter.plan(ctx, params)
		if err != nil {
			return nil, operationView{}, err
		}

		op, err := svc.Propose(ctx, auth.FromContext(ctx), operations.ProposeRequest{
			Plugin:        plugin,
			Action:        spec.Action,
			Risk:          operations.MaxRisk(spec.Risk, derefRisk(plan.RiskOverride)),
			Target:        plan.Before,
			Params:        params,
			Before:        plan.Before,
			Desired:       plan.Desired,
			Preconditions: plan.Preconditions,
			Rollback:      plan.Rollback,
			Changes:       plan.Changes,
			Impact:        plan.Impact,
			CorrelationID: observability.CorrelationID(ctx),
		})
		if err != nil {
			return nil, operationView{}, err
		}

		// The operation is recorded before anyone is asked, so a change that
		// is declined -- or one where the client vanishes mid-question --
		// still leaves a durable record of what was proposed.
		return nil, resolveApproval(ctx, req, svc, inline, op), nil
	})
}

// resolveApproval asks the user in the conversation when that is possible and
// permitted, and otherwise leaves it for an explicit approve_operation call.
//
// Whichever path is taken, execution requires an approval recorded here.
// A client that cannot ask does not get an unguarded write; it gets the
// two-step flow.
func resolveApproval(ctx context.Context, req *mcp.CallToolRequest, svc ApprovalService, inline InlinePolicy, op *operations.Operation) operationView {
	if inline == nil || !inline.AllowsInline(op.Risk) {
		view := viewOf(op)
		if inline != nil {
			// Above the ceiling the shortcut is withheld, not the decision.
			// A yes/no prompt is too thin a thing to hang a consequential
			// change on, so the model has to show the change and be told
			// explicitly -- but the person still decides in the conversation.
			view.Note = "NOTHING HAS CHANGED YET. This change is consequential " +
				"enough that a yes/no prompt is not sufficient: show the person " +
				"exactly what will change, in full, and only if they explicitly " +
				"tell you to go ahead, call approve_operation with this " +
				"operation_id. Never call it on your own judgement. " + view.Note
		}
		return view
	}

	decision, err := askUser(ctx, req, op)
	switch {
	case err != nil, decision == decisionUnavailable:
		// The client could not ask. Leave it pending rather than assuming
		// either answer.
		return viewOf(op)

	case decision == decisionDeclined:
		if rejected, rErr := svc.Reject(ctx, auth.FromContext(ctx), op.ID,
			"declined by the user when asked"); rErr == nil {
			return viewOf(rejected)
		}
		return viewOf(op)
	}

	approved, err := svc.ApproveInline(ctx, auth.FromContext(ctx), op.ID)
	if err != nil {
		view := viewOf(op)
		// A refusal here is the system working -- separation of duties, an
		// expired proposal -- and the reason matters more than the state.
		view.Note = "Could not approve: " + err.Error()
		return view
	}

	// Waiting turns "approved" into "done" for the user, who otherwise has to
	// ask whether it worked. The wait is bounded; a slow change reports as
	// still running rather than hanging the conversation.
	final, err := svc.AwaitOutcome(ctx, approved.ID, 30*time.Second)
	if err != nil || final == nil {
		return viewOf(approved)
	}
	return viewOf(final)
}

func derefRisk(r *operations.RiskLevel) operations.RiskLevel {
	if r == nil {
		return operations.RiskLow
	}
	return *r
}

// --- internal plumbing ----------------------------------------------------

// ToolMiddleware is the host-supplied gate that runs before any plugin handler.
// It performs authorization and rate limiting, and returns an error that the
// SDK reports to the caller as a tool error.
type ToolMiddleware func(ctx context.Context, tool string, required auth.Capability) error

type registeredTool struct {
	spec       ToolSpec
	qualified  string
	capability auth.Capability
	attach     func(*mcp.Server, ToolMiddleware)
}

type registeredMutation struct {
	spec      MutationSpec
	plugin    string
	qualified string
	adapter   *handlerAdapter
	attach    func(*mcp.Server, ToolMiddleware, ApprovalService, InlinePolicy)
}

func (r *Registry) hasTool(name string) bool {
	for _, t := range r.tools {
		if t.spec.Name == name {
			return true
		}
	}
	return false
}

func (r *Registry) hasMutation(action string) bool {
	for _, m := range r.mutations {
		if m.spec.Action == action {
			return true
		}
	}
	return false
}

// err returns the accumulated registration errors.
//
// Errors are collected rather than returned immediately so that a plugin with
// several mistakes reports all of them at once, instead of forcing an operator
// through one restart per typo.
func (r *Registry) err() error {
	if len(r.errs) == 0 {
		return nil
	}
	msgs := make([]string, len(r.errs))
	for i, e := range r.errs {
		msgs[i] = e.Error()
	}
	sort.Strings(msgs)
	return fmt.Errorf("plugin %s failed registration:\n  - %s",
		r.descriptor.Name, strings.Join(msgs, "\n  - "))
}

// ToolNames returns the qualified names of every registered tool, for the
// dashboard and for diagnostics.
func (r *Registry) ToolNames() []string {
	out := make([]string, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t.qualified)
	}
	return out
}

// MutationActions returns every registered mutation action.
func (r *Registry) MutationActions() []string {
	out := make([]string, 0, len(r.mutations))
	for _, m := range r.mutations {
		out = append(out, m.spec.Action)
	}
	return out
}

// mutationByAction resolves a registered mutation for the executor.
func (r *Registry) mutationByAction(action string) (registeredMutation, bool) {
	for _, m := range r.mutations {
		if m.spec.Action == action {
			return m, true
		}
	}
	return registeredMutation{}, false
}

// rawJSON is a helper for encoding plugin values into operation payloads.
func rawJSON(v any) (json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("plugins: encode value: %w", err)
	}
	return b, nil
}
