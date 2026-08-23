package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/spoked/mcpd/internal/operations"
)

var (
	// toolNamePattern is the house style for a tool this project ships: one
	// word, lowercase, underscored. It is a convention, and worth keeping for
	// code we write.
	toolNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,47}$`)
	// remoteToolNamePattern is the MCP specification's charset, which is what
	// a remote server's tools are entitled to use.
	//
	// getWeather, search.docs and read-file are all valid names upstream, and
	// this host's convention rejects every one of them. Normalising instead
	// would be worse than rejecting: the model would be shown a name the far
	// end does not answer to, and every call would fail at the last hop with
	// nothing saying why.
	remoteToolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	actionPattern         = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
)

// maxQualifiedToolName bounds the name a model actually sees, which is the
// plugin prefix, an underscore, and the tool's own name. The MCP
// specification's limit; the house pattern stays well inside it, but an
// upstream name plus an instance prefix does not necessarily.
const maxQualifiedToolName = 128

// checkToolName applies the naming rule for the plugin's runtime.
func checkToolName(d Descriptor, name string) error {
	if d.EffectiveRuntime() == RuntimeMCP {
		if !remoteToolNamePattern.MatchString(name) {
			return fmt.Errorf("plugins: %s tool name %q is outside the character set "+
				"the MCP specification allows (%s)", d.Name, name, remoteToolNamePattern)
		}
		if n := len(d.Name) + 1 + len(name); n > maxQualifiedToolName {
			return fmt.Errorf("plugins: %s_%s is %d characters, past the %d a tool "+
				"name may be", d.Name, name, n, maxQualifiedToolName)
		}
		return nil
	}
	if !toolNamePattern.MatchString(name) {
		return fmt.Errorf("plugins: %s tool name %q must match %s",
			d.Name, name, toolNamePattern)
	}
	return nil
}

// Plan is what a mutation handler produces before anything is changed. It is
// both what an approver reads and what the executor re-checks.
type Plan[S any] struct {
	// Before is the observed current state of the target.
	Before S
	// Desired is the state Apply intends to produce.
	Desired S
	// Preconditions is the snapshot re-checked immediately before execution.
	// Prefer an ETag, revision or generation where the upstream provides one;
	// fall back to the specific fields the mutation depends on.
	Preconditions map[string]any
	// Changes is the field-level diff shown to the approver. It must describe
	// the mutation completely: an approver shown one field while five change
	// has not meaningfully approved anything.
	Changes []operations.Change
	// Impact is plain language describing the consequence, e.g. "Clients on
	// this radio will briefly disconnect."
	Impact string
	// Rollback holds parameters for the inverse mutation, if one exists.
	// A rollback is itself approval-gated; this is not an automatic undo.
	Rollback any
	// RiskOverride raises the risk for these specific parameters. It cannot
	// lower the spec's declared risk.
	RiskOverride *operations.RiskLevel
	// State is opaque data the handler needs in Apply and that is not part of
	// what the approver reads: a session token, an upstream revision handle,
	// anything the plugin would otherwise have to keep beside the operation.
	//
	// The host carries the typed plan straight through to Apply, so whatever
	// is put here arrives unchanged and without a JSON round trip. It is not
	// persisted, not shown to the approver, and not part of the payload hash;
	// a plan is rebuilt immediately before every execution, so there is
	// nothing here for a restart to lose. It mirrors sdk.Plan.State, which is
	// how an out-of-process plugin says the same thing on the wire.
	State any
}

// ApplyResult reports what an upstream write produced.
type ApplyResult struct {
	// UpstreamRef is a job or change identifier used to reconcile an
	// indeterminate outcome.
	UpstreamRef string
	// Async reports that upstream accepted the request but has not yet applied
	// it. When true, success requires Observe to confirm the desired state;
	// an HTTP 200 alone does not settle the operation.
	Async bool
}

// MutationHandler is a typed, approval-gated write.
//
// Splitting it into three methods is what makes precondition checking,
// verification, and at-most-once execution possible:
//
//   - Plan runs twice — once at proposal to capture Before and build the diff,
//     and again immediately before Apply to detect drift. Because it is the
//     same code both times, the snapshot shown to the approver and the one
//     checked at execution cannot diverge.
//   - Observe backs both verification and the reconciliation of indeterminate
//     outcomes, so both share one implementation.
//   - Apply is the only method that mutates, which makes the audit rule
//     mechanical: exactly one call site to wrap.
type MutationHandler[P, S any] interface {
	// Plan validates params and reads current state. It must not mutate.
	Plan(ctx context.Context, params P) (Plan[S], error)

	// Apply performs the upstream write. It is called at most once per
	// attempt, only by the executor, only after the state machine granted a
	// claim.
	//
	// On an ambiguous failure — a timeout, a dropped connection, any outcome
	// where the write may or may not have landed — it must return an error
	// wrapping operations.ErrIndeterminate. Returning an ordinary error
	// implies the write did not happen, and a retry on that basis
	// double-applies the mutation.
	Apply(ctx context.Context, params P, plan Plan[S]) (ApplyResult, error)

	// Observe re-reads the target's current state. It must not mutate.
	Observe(ctx context.Context, params P) (S, error)
}

// handlerAdapter erases the type parameters of a MutationHandler so the host
// can store handlers of differing types in one registry, while keeping the
// typed contract at the plugin's own boundary.
//
// Decoding happens inside the adapter, so a payload that does not fit the
// plugin's parameter type is rejected before any plugin code runs.
type handlerAdapter struct {
	plan    func(ctx context.Context, params json.RawMessage) (planResult, error)
	apply   func(ctx context.Context, params json.RawMessage, pl planResult) (ApplyResult, error)
	observe func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)
}

// planResult is the type-erased form of Plan.
type planResult struct {
	Before        json.RawMessage
	Desired       json.RawMessage
	Preconditions json.RawMessage
	Changes       []operations.Change
	Impact        string
	Rollback      json.RawMessage
	RiskOverride  *operations.RiskLevel
	// typed retains the original Plan so Apply receives it without a
	// re-decode, preserving any state that does not survive a JSON round trip.
	typed any
}

func newAdapter[P, S any](h MutationHandler[P, S]) *handlerAdapter {
	decode := func(raw json.RawMessage) (P, error) {
		var p P
		if len(raw) == 0 {
			return p, fmt.Errorf("plugins: mutation parameters are required")
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return p, fmt.Errorf("plugins: decode mutation parameters: %w", err)
		}
		return p, nil
	}

	return &handlerAdapter{
		plan: func(ctx context.Context, raw json.RawMessage) (planResult, error) {
			params, err := decode(raw)
			if err != nil {
				return planResult{}, err
			}
			pl, err := h.Plan(ctx, params)
			if err != nil {
				return planResult{}, err
			}
			return erasePlan(pl)
		},
		apply: func(ctx context.Context, raw json.RawMessage, pr planResult) (ApplyResult, error) {
			params, err := decode(raw)
			if err != nil {
				return ApplyResult{}, err
			}
			pl, ok := pr.typed.(Plan[S])
			if !ok {
				return ApplyResult{}, fmt.Errorf("plugins: plan type mismatch for apply")
			}
			return h.Apply(ctx, params, pl)
		},
		observe: func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
			params, err := decode(raw)
			if err != nil {
				return nil, err
			}
			s, err := h.Observe(ctx, params)
			if err != nil {
				return nil, err
			}
			return rawJSON(s)
		},
	}
}

func erasePlan[S any](pl Plan[S]) (planResult, error) {
	before, err := rawJSON(pl.Before)
	if err != nil {
		return planResult{}, err
	}
	desired, err := rawJSON(pl.Desired)
	if err != nil {
		return planResult{}, err
	}
	pre, err := rawJSON(pl.Preconditions)
	if err != nil {
		return planResult{}, err
	}
	rollback, err := rawJSON(pl.Rollback)
	if err != nil {
		return planResult{}, err
	}
	return planResult{
		Before:        before,
		Desired:       desired,
		Preconditions: pre,
		Changes:       pl.Changes,
		Impact:        pl.Impact,
		Rollback:      rollback,
		RiskOverride:  pl.RiskOverride,
		typed:         pl,
	}, nil
}
