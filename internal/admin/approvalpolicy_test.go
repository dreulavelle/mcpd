package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"strings"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/operations"
	"github.com/spoked/mcpd/internal/plugins"
	"github.com/spoked/mcpd/internal/settings"
	"github.com/spoked/mcpd/internal/storage/sqlite"
)

func newPolicyDashboard(t *testing.T, role auth.Role) (*Server, *settings.Store) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.Open(ctx, sqlite.Options{
		Path:              filepath.Join(t.TempDir(), "policy.db"),
		RelaxedDurability: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := sqlite.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	store := settings.NewStore(db, nil, time.Now)
	return NewServer(Options{
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Verifier: roleVerifier{role: role},
		Settings: store,
	}), store
}

func decodePolicy(t *testing.T, body []byte) approvalPolicyResponse {
	t.Helper()
	var out approvalPolicyResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("response is not readable: %v (%s)", err, body)
	}
	return out
}

// A host nobody has configured has no rules, and the page has to be able to
// say so rather than showing an empty list that might mean anything.
func TestApprovalPolicy_StartsEmpty(t *testing.T) {
	s, _ := newPolicyDashboard(t, auth.RoleAdmin)

	w := request(t, s, http.MethodGet, "/api/approval-policy", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body)
	}
	got := decodePolicy(t, w.Body.Bytes())
	if len(got.Rules) != 0 {
		t.Errorf("a fresh host has %d rules, want none", len(got.Rules))
	}
	if got.Wildcard != operations.RuleAny {
		t.Errorf("wildcard = %q, want %q", got.Wildcard, operations.RuleAny)
	}
	if len(got.Ceilings) == 0 {
		t.Error("the page needs the ceilings it may offer")
	}
}

// The written set is what the host reads back, canonicalised, and the change
// is in settings history like any other configuration change -- which is the
// reason these live in the settings store rather than a file of their own.
func TestApprovalPolicy_WritesAreStoredAndRecorded(t *testing.T) {
	s, store := newPolicyDashboard(t, auth.RoleAdmin)

	w := request(t, s, http.MethodPut, "/api/approval-policy", map[string]any{
		"rules": []map[string]any{
			// Written with the principal omitted, which means "anybody" and
			// has to come back saying so.
			{"id": "routine-radio", "plugin": "cnmaestro", "max_risk": "low",
				"note": "channel changes are undone by another channel change"},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body)
	}
	got := decodePolicy(t, w.Body.Bytes())
	if len(got.Rules) != 1 {
		t.Fatalf("stored %d rules, want 1", len(got.Rules))
	}
	if got.Rules[0].Principal != operations.RuleAny || got.Rules[0].Action != operations.RuleAny {
		t.Errorf("omitted selectors = %s, want wildcards", got.Rules[0].Scope())
	}

	reread := decodePolicy(t, request(t, s, http.MethodGet, "/api/approval-policy", nil).Body.Bytes())
	if len(reread.Rules) != 1 || reread.Rules[0].ID != "routine-radio" {
		t.Fatalf("reading back gave %+v", reread.Rules)
	}

	history, err := store.History(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	var recorded bool
	for _, h := range history {
		if h.Key == settings.KeyApprovalAutoRules {
			recorded = true
			if h.ChangedBy != "svc:test" {
				t.Errorf("recorded against %q, want the administrator who wrote it", h.ChangedBy)
			}
		}
	}
	if !recorded {
		t.Error("changing when a human is skipped must be in the settings history")
	}
}

// Validation happens before anything is stored, so a bad rule leaves the
// policy exactly as it was rather than half-applied.
func TestApprovalPolicy_RefusesAnInvalidSetWithoutStoringIt(t *testing.T) {
	s, _ := newPolicyDashboard(t, auth.RoleAdmin)

	for _, tc := range []struct {
		name  string
		rules []map[string]any
	}{
		{"two rules on one scope", []map[string]any{
			{"id": "a", "plugin": "cnmaestro", "max_risk": "low"},
			{"id": "b", "plugin": "cnmaestro", "max_risk": "high"},
		}},
		{"a rule covering critical", []map[string]any{
			{"id": "bold", "max_risk": "critical"},
		}},
		{"a rule with no id", []map[string]any{
			{"plugin": "cnmaestro", "max_risk": "low"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := request(t, s, http.MethodPut, "/api/approval-policy",
				map[string]any{"rules": tc.rules})
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (%s)", w.Code, w.Body)
			}
			after := decodePolicy(t, request(t, s, http.MethodGet, "/api/approval-policy", nil).Body.Bytes())
			if len(after.Rules) != 0 {
				t.Fatalf("a refused write left %d rules behind", len(after.Rules))
			}
		})
	}
}

// Reading the policy answers "why was I not asked", which is an operator's
// question. Changing it decides when the gate is skipped, which is not.
func TestApprovalPolicy_CapabilityGating(t *testing.T) {
	operator, _ := newPolicyDashboard(t, auth.RoleUser)

	if w := request(t, operator, http.MethodGet, "/api/approval-policy", nil); w.Code != http.StatusOK {
		t.Errorf("an operator reading the policy = %d, want 200", w.Code)
	}
	w := request(t, operator, http.MethodPut, "/api/approval-policy",
		map[string]any{"rules": []map[string]any{{"id": "a", "max_risk": "low"}}})
	if w.Code != http.StatusForbidden {
		t.Errorf("an operator writing the policy = %d, want 403", w.Code)
	}
}

// Which rule applies is a question an operator has to be able to ask before a
// change is proposed, not only afterwards from the record.
func TestApprovalPolicy_ExplainsWhatWouldHappen(t *testing.T) {
	s, _ := newPolicyDashboard(t, auth.RoleAdmin)
	if w := request(t, s, http.MethodPut, "/api/approval-policy", map[string]any{
		"rules": []map[string]any{
			{"id": "routine-radio", "plugin": "cnmaestro", "max_risk": "low"},
			{"id": "never-reboot", "plugin": "cnmaestro", "action": "device.reboot"},
		},
	}); w.Code != http.StatusOK {
		t.Fatalf("seeding rules = %d (%s)", w.Code, w.Body)
	}

	for _, tc := range []struct {
		name     string
		body     map[string]any
		want     bool
		wantRule string
	}{
		{
			name: "a routine change",
			body: map[string]any{
				"plugin": "cnmaestro", "action": "device.set_radio_channel",
				"principal": "user:alice", "risk": "low", "reversible": true,
			},
			want: true, wantRule: "routine-radio",
		},
		{
			name: "the carved-out action",
			body: map[string]any{
				"plugin": "cnmaestro", "action": "device.reboot",
				"principal": "user:alice", "risk": "low", "reversible": true,
			},
			want: false, wantRule: "never-reboot",
		},
		{
			name: "another plugin entirely",
			body: map[string]any{
				"plugin": "echo", "action": "label.set",
				"principal": "user:alice", "risk": "low", "reversible": true,
			},
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := request(t, s, http.MethodPost, "/api/approval-policy/evaluate", tc.body)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d (%s)", w.Code, w.Body)
			}
			var got evaluateResponse
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.AutoApprove != tc.want {
				t.Errorf("auto_approve = %v, want %v (%s)", got.AutoApprove, tc.want, got.Reason)
			}
			ruleID := ""
			if got.Rule != nil {
				ruleID = got.Rule.ID
			}
			if ruleID != tc.wantRule {
				t.Errorf("rule = %q, want %q", ruleID, tc.wantRule)
			}
			if got.Reason == "" {
				t.Error("an explanation with no reason explains nothing")
			}
		})
	}
}

// The rules are stored in the settings table but are not a settings field, and
// the generic form must not be a second way in. A JSON blob typed into a text
// box would be validated by nothing, which is the wrong shape for the setting
// that decides when a human is skipped.
func TestApprovalPolicy_TheGenericSettingsFormCannotWriteRules(t *testing.T) {
	s, _ := newPolicyDashboard(t, auth.RoleAdmin)

	w := request(t, s, http.MethodPut, "/api/settings", map[string]any{
		"values": map[string]string{
			settings.KeyApprovalAutoRules: `[{"id":"sneaky","max_risk":"high"}]`,
		},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", w.Code, w.Body)
	}
	after := decodePolicy(t, request(t, s, http.MethodGet, "/api/approval-policy", nil).Body.Bytes())
	if len(after.Rules) != 0 {
		t.Fatalf("the settings form wrote %d rules", len(after.Rules))
	}
}

// A misspelled selector must reach the operator as an error naming the field,
// not as a rule that silently covers everybody.
func TestApprovalPolicy_RefusesAMisspelledSelector(t *testing.T) {
	s, _ := newPolicyDashboard(t, auth.RoleAdmin)

	for _, tc := range []struct {
		name string
		rule map[string]any
	}{
		{"a misspelled principal", map[string]any{
			"id": "x", "principle": "svc:agent", "max_risk": "low"}},
		{"an explicit null selector", map[string]any{
			"id": "x", "plugin": nil, "max_risk": "low"}},
		{"an empty selector", map[string]any{
			"id": "x", "plugin": "", "max_risk": "low"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := request(t, s, http.MethodPut, "/api/approval-policy",
				map[string]any{"rules": []map[string]any{tc.rule}})
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (%s)", w.Code, w.Body)
			}
			after := decodePolicy(t, request(t, s, http.MethodGet, "/api/approval-policy", nil).Body.Bytes())
			if len(after.Rules) != 0 {
				t.Fatalf("a refused write left %d rules behind", len(after.Rules))
			}
		})
	}
}

// Misspelling the wrapper would otherwise read as an empty set and quietly
// delete the whole policy.
func TestApprovalPolicy_RefusesAMisspelledWrapper(t *testing.T) {
	s, _ := newPolicyDashboard(t, auth.RoleAdmin)
	if w := request(t, s, http.MethodPut, "/api/approval-policy", map[string]any{
		"rules": []map[string]any{{"id": "routine", "plugin": "cnmaestro", "max_risk": "low"}},
	}); w.Code != http.StatusOK {
		t.Fatalf("seeding = %d (%s)", w.Code, w.Body)
	}

	if w := request(t, s, http.MethodPut, "/api/approval-policy",
		map[string]any{"rulez": []map[string]any{}}); w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", w.Code, w.Body)
	}
	after := decodePolicy(t, request(t, s, http.MethodGet, "/api/approval-policy", nil).Body.Bytes())
	if len(after.Rules) != 1 {
		t.Fatalf("the policy now has %d rules; a typo must not clear it", len(after.Rules))
	}
}

// The exclusion an operator writes has to hold at the endpoint too, not only
// in the resolver. This is the reproduction that started as an auto-approved
// device reboot.
func TestApprovalPolicy_AnExclusionBeatsABroaderGrant(t *testing.T) {
	s, _ := newPolicyDashboard(t, auth.RoleAdmin)
	if w := request(t, s, http.MethodPut, "/api/approval-policy", map[string]any{
		"rules": []map[string]any{
			{"id": "plugin-wide", "plugin": "cnmaestro", "max_risk": "high"},
			{"id": "never-reboot", "action": "device.reboot", "max_risk": ""},
		},
	}); w.Code != http.StatusOK {
		t.Fatalf("seeding = %d (%s)", w.Code, w.Body)
	}

	w := request(t, s, http.MethodPost, "/api/approval-policy/evaluate", map[string]any{
		"plugin": "cnmaestro", "action": "device.reboot",
		"principal": "user:alice", "risk": "low", "reversible": true,
	})
	var got evaluateResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.AutoApprove {
		t.Fatalf("a device reboot auto-approved under rule %q", got.Rule.ID)
	}
	if got.Rule == nil || got.Rule.ID != "never-reboot" {
		t.Fatalf("blamed %+v, want never-reboot", got.Rule)
	}
}

// A typo in an exclusion is the case worth warning about. Deny-wins makes it
// harmless in itself -- an exclusion authorises nothing however it is spelled
// -- but it silently stops protecting the action it was written for, and the
// grant beside it decides instead.
func TestApprovalPolicy_WarnsAboutARuleThatMatchesNothing(t *testing.T) {
	s, _ := newPolicyDashboard(t, auth.RoleAdmin)
	s.opts.Manager = mountedManager(t)

	w := request(t, s, http.MethodPut, "/api/approval-policy", map[string]any{
		"rules": []map[string]any{
			{"id": "plugin-wide", "plugin": "echo", "max_risk": "high"},
			{"id": "never-rebooot", "action": "label.rebooot", "max_risk": ""},
			{"id": "other-host", "plugin": "cnmaestro", "max_risk": "low"},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; an unmatched rule is a warning, not a refusal (%s)",
			w.Code, w.Body)
	}
	got := decodePolicy(t, w.Body.Bytes())
	if len(got.Rules) != 3 {
		t.Fatalf("stored %d rules, want 3", len(got.Rules))
	}

	joined := strings.Join(got.Warnings, "\n")
	for _, want := range []string{"never-rebooot", "other-host"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings do not mention %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "plugin-wide") {
		t.Errorf("a rule that does match was warned about:\n%s", joined)
	}

	// And the warning is on the read too, not only on the write that caused it.
	reread := decodePolicy(t, request(t, s, http.MethodGet, "/api/approval-policy", nil).Body.Bytes())
	if len(reread.Warnings) != len(got.Warnings) {
		t.Errorf("GET reported %d warnings, PUT reported %d", len(reread.Warnings), len(got.Warnings))
	}
}

// A host with nothing mounted must not warn that every rule matches nothing.
func TestApprovalPolicy_DoesNotWarnWithNothingMounted(t *testing.T) {
	s, _ := newPolicyDashboard(t, auth.RoleAdmin)

	w := request(t, s, http.MethodPut, "/api/approval-policy", map[string]any{
		"rules": []map[string]any{{"id": "routine", "plugin": "cnmaestro", "max_risk": "low"}},
	})
	if got := decodePolicy(t, w.Body.Bytes()); len(got.Warnings) != 0 {
		t.Errorf("warned with no plugins mounted: %v", got.Warnings)
	}
}

// mountedManager builds a manager with one plugin registering one mutation, so
// the unmatched-rule check has something real to judge against.
func mountedManager(t *testing.T) *plugins.Manager {
	t.Helper()
	m := plugins.NewManager(slog.New(slog.NewTextHandler(io.Discard, nil)), "test",
		func(context.Context, string, auth.Capability) error { return nil },
		stubApprovals{}, nil, nil)
	if err := m.Register(context.Background(), labelPlugin{}, "echo", false); err != nil {
		t.Fatalf("register: %v", err)
	}
	return m
}

type labelPlugin struct{}

func (labelPlugin) Descriptor() plugins.Descriptor {
	return plugins.Descriptor{Name: "echo", Version: "1.0.0", Title: "Echo"}
}

func (labelPlugin) Register(_ context.Context, r *plugins.Registry) error {
	plugins.Mutation(r, plugins.MutationSpec{
		Action: "label.set", Title: "Set label", Description: "Sets a label.",
		Risk: operations.RiskLow, Reversible: true,
	}, labelMutation{})
	return nil
}

func (labelPlugin) Start(context.Context) error    { return nil }
func (labelPlugin) Shutdown(context.Context) error { return nil }
func (labelPlugin) Health(context.Context) plugins.Health {
	return plugins.Healthy()
}

type labelMutation struct{}

func (labelMutation) Plan(context.Context, struct{}) (plugins.Plan[struct{}], error) {
	return plugins.Plan[struct{}]{}, nil
}

func (labelMutation) Apply(context.Context, struct{}, plugins.Plan[struct{}]) (plugins.ApplyResult, error) {
	return plugins.ApplyResult{}, nil
}

func (labelMutation) Observe(context.Context, struct{}) (struct{}, error) {
	return struct{}{}, nil
}

// stubApprovals lets a plugin declaring a mutation mount. Nothing here is
// called: the test reads which actions are registered, it does not propose.
type stubApprovals struct{}

func (stubApprovals) Propose(context.Context, *auth.Principal, operations.ProposeRequest) (*operations.Operation, error) {
	return nil, errNotUsed
}

func (stubApprovals) Approve(context.Context, *auth.Principal, string, string) (*operations.Operation, error) {
	return nil, errNotUsed
}

func (stubApprovals) Reject(context.Context, *auth.Principal, string, string) (*operations.Operation, error) {
	return nil, errNotUsed
}

func (stubApprovals) Cancel(context.Context, *auth.Principal, string, string) (*operations.Operation, error) {
	return nil, errNotUsed
}

func (stubApprovals) Get(context.Context, *auth.Principal, string) (*operations.Operation, error) {
	return nil, errNotUsed
}

func (stubApprovals) ApproveInline(context.Context, *auth.Principal, string) (*operations.Operation, error) {
	return nil, errNotUsed
}

func (stubApprovals) AwaitOutcome(context.Context, string, time.Duration) (*operations.Operation, error) {
	return nil, errNotUsed
}

func (stubApprovals) List(context.Context, *auth.Principal, string, []operations.OperationState, int) ([]*operations.Operation, error) {
	return nil, errNotUsed
}

var errNotUsed = errors.New("not used in this test")
