package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spoked/mcpd/internal/auth"
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
}

func (s ToolSpec) validate(plugin string) error {
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

	qualified := r.descriptor.Name + "_" + spec.Name
	readOnly := true
	openWorld := true

	r.tools = append(r.tools, registeredTool{
		spec:       spec,
		qualified:  qualified,
		capability: auth.CapRead,
		attach: func(s *mcp.Server, mw ToolMiddleware) {
			mcp.AddTool(s, &mcp.Tool{
				Name:        qualified,
				Description: spec.Description,
				Annotations: &mcp.ToolAnnotations{
					Title:          spec.Title,
					ReadOnlyHint:   readOnly,
					IdempotentHint: spec.Idempotent,
					OpenWorldHint:  &openWorld,
				},
			}, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
				var zero Out
				if err := mw(ctx, qualified, auth.CapRead); err != nil {
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
	r.mutations = append(r.mutations, registeredMutation{
		spec:    spec,
		plugin:  r.descriptor.Name,
		adapter: newAdapter(h),
	})
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
	spec    MutationSpec
	plugin  string
	adapter *handlerAdapter
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
