package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/spoked/mcpd/internal/operations"
)

// Runner adapts registered mutation handlers to the executor's view of them.
//
// It is the boundary where the domain's type-erased world meets the plugin's
// typed one. Decoding happens inside the adapter, so a payload that does not
// fit the plugin's parameter type is rejected before any plugin code runs.
type Runner struct {
	mu      sync.RWMutex
	manager *Manager
	// plans caches the typed plan between Plan and Apply within one execution,
	// keyed by plugin.action plus payload hash. Round-tripping the plan
	// through JSON would lose any state that does not serialise.
	plans map[string]any
}

// NewRunner builds the executor's bridge to the plugin registry.
func NewRunner(m *Manager) *Runner {
	return &Runner{manager: m, plans: make(map[string]any)}
}

// resolve finds a registered mutation.
func (r *Runner) resolve(plugin, action string) (*handlerAdapter, error) {
	mounted := r.manager.Lookup(plugin)
	if mounted == nil {
		return nil, fmt.Errorf("plugins: %q is not mounted", plugin)
	}
	m, ok := mounted.Registry.mutationByAction(action)
	if !ok {
		return nil, fmt.Errorf("plugins: %s registers no mutation %q", plugin, action)
	}
	return m.adapter, nil
}

// Plan implements operations.MutationRunner.
func (r *Runner) Plan(ctx context.Context, plugin, action string, params json.RawMessage) (operations.PlanResult, error) {
	adapter, err := r.resolve(plugin, action)
	if err != nil {
		return operations.PlanResult{}, err
	}
	pr, err := adapter.plan(ctx, params)
	if err != nil {
		return operations.PlanResult{}, err
	}

	// Retain the typed plan so Apply receives exactly what the handler built.
	key := planKey(plugin, action, params)
	r.mu.Lock()
	r.plans[key] = pr.typed
	r.mu.Unlock()

	return operations.PlanResult{
		Before:        pr.Before,
		Desired:       pr.Desired,
		Preconditions: pr.Preconditions,
		Changes:       pr.Changes,
		Impact:        pr.Impact,
		Rollback:      pr.Rollback,
		RiskOverride:  pr.RiskOverride,
		Handle:        pr.typed,
	}, nil
}

// Apply implements operations.MutationRunner.
func (r *Runner) Apply(ctx context.Context, plugin, action string, params json.RawMessage, plan operations.PlanResult) (operations.ApplyOutcome, error) {
	adapter, err := r.resolve(plugin, action)
	if err != nil {
		return operations.ApplyOutcome{}, err
	}

	typed := plan.Handle
	if typed == nil {
		// Fall back to the cache when the caller did not carry the handle,
		// then drop it: a plan is valid for exactly one execution.
		key := planKey(plugin, action, params)
		r.mu.Lock()
		typed = r.plans[key]
		delete(r.plans, key)
		r.mu.Unlock()
	}
	if typed == nil {
		return operations.ApplyOutcome{}, fmt.Errorf(
			"plugins: no plan is available for %s.%s; Plan must run immediately before Apply",
			plugin, action)
	}

	res, err := adapter.apply(ctx, params, planResult{typed: typed})
	return operations.ApplyOutcome{
		UpstreamRef: res.UpstreamRef,
		Async:       res.Async,
	}, err
}

// Observe implements operations.MutationRunner.
func (r *Runner) Observe(ctx context.Context, plugin, action string, params json.RawMessage) (json.RawMessage, error) {
	adapter, err := r.resolve(plugin, action)
	if err != nil {
		return nil, err
	}
	return adapter.observe(ctx, params)
}

// MutationSpecFor returns the registered spec for an action, so the propose
// path can read the declared risk without reaching into the registry.
func (r *Runner) MutationSpecFor(plugin, action string) (MutationSpec, bool) {
	mounted := r.manager.Lookup(plugin)
	if mounted == nil {
		return MutationSpec{}, false
	}
	m, ok := mounted.Registry.mutationByAction(action)
	if !ok {
		return MutationSpec{}, false
	}
	return m.spec, true
}

func planKey(plugin, action string, params json.RawMessage) string {
	return plugin + "." + action + "|" + string(params)
}
