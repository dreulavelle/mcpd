// Package external runs plugins as separate processes.
//
// A plugin is a binary in the plugins directory that speaks line-delimited
// JSON-RPC over stdin and stdout. Running out of process is what makes the
// bind-mounted plugins directory work: an integration can be added, upgraded,
// or removed without rebuilding mcpd.
//
// It also buys isolation the in-tree path cannot. A plugin that panics,
// deadlocks, leaks memory, or spins takes only itself down, and the host can
// bound its resources and restart it.
package external

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// ProtocolVersion is the wire contract between host and plugin.
//
// The host refuses a plugin declaring a different major version. A plugin
// built against an older contract is more dangerous than one that will not
// load, because it may misinterpret a mutation payload.
const ProtocolVersion = "1"

// Method names on the wire.
const (
	// MethodDescribe returns the plugin's identity and capabilities. It is the
	// first call, and its response determines everything the host mounts.
	MethodDescribe = "describe"
	// MethodCallTool invokes a read-only tool.
	MethodCallTool = "call_tool"
	// MethodPlan validates parameters and captures current state.
	MethodPlan = "plan"
	// MethodApply performs the upstream write.
	MethodApply = "apply"
	// MethodObserve re-reads state for verification.
	MethodObserve = "observe"
	// MethodHealth reports the plugin's view of its upstream.
	MethodHealth = "health"
	// MethodShutdown asks the plugin to stop cleanly.
	MethodShutdown = "shutdown"
)

// Request is one call from host to plugin.
type Request struct {
	// ID correlates a response with its request. The plugin must echo it.
	ID     uint64          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response is a plugin's reply.
type Response struct {
	ID     uint64          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Error is a plugin-reported failure.
type Error struct {
	// Code is a stable identifier. The host treats INDETERMINATE specially:
	// it marks an outcome the plugin could not establish, which must never be
	// retried.
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

// Error codes a plugin may return.
const (
	// CodeInvalidParams reports parameters the plugin cannot use.
	CodeInvalidParams = "INVALID_PARAMS"
	// CodeNotFound reports a missing upstream resource.
	CodeNotFound = "NOT_FOUND"
	// CodeUpstreamFailed reports a definite upstream failure: the write did
	// not happen.
	CodeUpstreamFailed = "UPSTREAM_FAILED"
	// CodeIndeterminate reports that the plugin cannot establish whether the
	// mutation took effect. The host records the operation as indeterminate
	// and does not retry it.
	CodeIndeterminate = "INDETERMINATE"
	// CodeInternal reports a bug in the plugin.
	CodeInternal = "INTERNAL"
)

// DescribeResult is the plugin's self-description.
type DescribeResult struct {
	Protocol    string               `json:"protocol"`
	Name        string               `json:"name"`
	Version     string               `json:"version"`
	Title       string               `json:"title"`
	Description string               `json:"description"`
	Tools       []ToolDescriptor     `json:"tools"`
	Mutations   []MutationDescriptor `json:"mutations"`
}

// ToolDescriptor describes one read-only tool.
type ToolDescriptor struct {
	Name        string          `json:"name"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	Idempotent  bool            `json:"idempotent"`
}

// MutationDescriptor describes one approval-gated write.
type MutationDescriptor struct {
	Action      string          `json:"action"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	Risk        string          `json:"risk"`
	Reversible  bool            `json:"reversible"`
}

// CallToolParams invokes a tool.
type CallToolParams struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// MutationParams invokes a mutation phase.
type MutationParams struct {
	Action string          `json:"action"`
	Params json.RawMessage `json:"params"`
	// Plan is supplied to apply, carrying what the plugin returned from plan.
	Plan json.RawMessage `json:"plan,omitempty"`
}

// PlanResult is the plugin's plan, in wire form.
type PlanResult struct {
	Before        json.RawMessage `json:"before,omitempty"`
	Desired       json.RawMessage `json:"desired,omitempty"`
	Preconditions json.RawMessage `json:"preconditions,omitempty"`
	Changes       []WireChange    `json:"changes,omitempty"`
	Impact        string          `json:"impact"`
	Rollback      json.RawMessage `json:"rollback,omitempty"`
	RiskOverride  string          `json:"risk_override,omitempty"`
	// State is opaque to the host and returned verbatim to apply, so a plugin
	// can carry anything its own apply needs without the host modelling it.
	State json.RawMessage `json:"state,omitempty"`
}

// WireChange is a field-level diff.
type WireChange struct {
	Field string `json:"field"`
	From  any    `json:"from"`
	To    any    `json:"to"`
}

// ApplyResult reports an upstream write.
type ApplyResult struct {
	UpstreamRef string `json:"upstream_ref,omitempty"`
	Async       bool   `json:"async,omitempty"`
}

// HealthResult is the plugin's health report.
type HealthResult struct {
	State string `json:"state"`
	// Message reaches the unauthenticated readiness endpoint, so it must never
	// contain credentials or full upstream URLs.
	Message string `json:"message,omitempty"`
}

// Manifest is plugin.json in a plugin's directory.
type Manifest struct {
	// Name must match what describe reports; a mismatch means the directory
	// and the binary disagree about which plugin this is.
	Name string `json:"name"`
	// Exec is the executable, relative to the plugin directory. It may not
	// escape it.
	Exec string   `json:"exec"`
	Args []string `json:"args,omitempty"`
	// Env passes configuration to the plugin. Values are secret references
	// resolved by the host, never secrets.
	Env map[string]string `json:"env,omitempty"`
	// Required determines whether a startup failure fails the host.
	Required bool `json:"required,omitempty"`
}

var manifestNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)

// Validate checks a manifest before anything is executed.
func (m *Manifest) Validate() error {
	if !manifestNamePattern.MatchString(m.Name) {
		return fmt.Errorf("external: plugin name %q must match %s", m.Name, manifestNamePattern)
	}
	if strings.TrimSpace(m.Exec) == "" {
		return fmt.Errorf("external: plugin %s declares no exec", m.Name)
	}
	// The executable must stay inside its own directory. Without this, a
	// manifest could point at any binary on the host, and the plugins
	// directory is a bind mount that may be writable by someone other than
	// the operator who reviewed it.
	if strings.HasPrefix(m.Exec, "/") {
		return fmt.Errorf("external: plugin %s exec %q must be relative to its directory",
			m.Name, m.Exec)
	}
	if strings.Contains(m.Exec, "..") {
		return fmt.Errorf("external: plugin %s exec %q must not escape its directory",
			m.Name, m.Exec)
	}
	return nil
}

// errorsAs wraps errors.As so this package's helpers need not each import it.
func errorsAs(err error, target any) bool { return stdErrorsAs(err, target) }
