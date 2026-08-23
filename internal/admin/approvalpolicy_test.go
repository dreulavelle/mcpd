package admin

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/operations"
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
