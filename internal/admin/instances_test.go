package admin

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/auth"
)

type instanceCalls struct {
	removed      string
	acknowledged bool
	restored     string
}

func newInstanceDashboard(t *testing.T, role auth.Role, calls *instanceCalls) *Server {
	t.Helper()
	return NewServer(Options{
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Verifier: roleVerifier{role: role},
		Instances: func(context.Context) []PluginInstanceInfo {
			return []PluginInstanceInfo{{
				Name: "echo", Type: "echo", Runtime: "builtin",
				FromFile: true, Required: true, Enabled: false,
				Removed: true, RemovedBy: "user:alice", RemovedAt: time.UnixMilli(1_700_000_000_000),
				Declaration: &PluginDeclaration{
					Type: "echo", Enabled: true, Required: true,
					SettingsKeys: []string{"base_url"},
				},
			}}
		},
		StaleRemovals: func(context.Context) []StaleRemoval {
			return []StaleRemoval{{
				Name: "gone", DeclaredType: "cnmaestro",
				RemovedBy: "user:alice", RemovedAt: time.UnixMilli(1_700_000_000_000),
			}}
		},
		AddPlugin: func(context.Context, string, string, string) error { return nil },
		RemovePlugin: func(_ context.Context, _, name string, acknowledged bool) error {
			calls.removed, calls.acknowledged = name, acknowledged
			return nil
		},
		RestorePlugin: func(_ context.Context, _, name string) error {
			calls.restored = name
			return nil
		},
		SetPluginEnabled: func(context.Context, string, string, bool) error { return nil },
	})
}

// Restoring is the same kind of decision as removing -- it puts an integration
// back in front of an assistant -- so it is gated the same way.
func TestInstanceRoutes_CapabilityGating(t *testing.T) {
	tests := []struct {
		method, path string
		body         any
		adminOnly    bool
	}{
		{method: http.MethodGet, path: "/api/instances"},
		{
			method: http.MethodDelete, path: "/api/instances/echo",
			adminOnly: true,
		},
		{
			method: http.MethodPost, path: "/api/instances/echo/restore",
			adminOnly: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			var calls instanceCalls
			asUser := request(t, newInstanceDashboard(t, auth.RoleUser, &calls),
				tc.method, tc.path, tc.body)
			if tc.adminOnly && asUser.Code != http.StatusForbidden {
				t.Fatalf("as a user: %d, want 403", asUser.Code)
			}
			if !tc.adminOnly && asUser.Code != http.StatusOK {
				t.Fatalf("as a user: %d, want 200", asUser.Code)
			}
			asAdmin := request(t, newInstanceDashboard(t, auth.RoleAdmin, &calls),
				tc.method, tc.path, tc.body)
			if asAdmin.Code != http.StatusOK {
				t.Fatalf("as an admin: %d (%s)", asAdmin.Code, asAdmin.Body.String())
			}
		})
	}
}

// The acknowledgement is the operator saying they have seen that the file
// marks the plugin required. It has to reach the layer that enforces it.
func TestRemoveInstance_PassesTheAcknowledgement(t *testing.T) {
	var calls instanceCalls
	s := newInstanceDashboard(t, auth.RoleAdmin, &calls)

	if w := request(t, s, http.MethodDelete, "/api/instances/echo", nil); w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	if calls.removed != "echo" || calls.acknowledged {
		t.Fatalf("calls = %+v, want an unacknowledged removal", calls)
	}

	w := request(t, s, http.MethodDelete,
		"/api/instances/echo?acknowledge_required=true", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	if !calls.acknowledged {
		t.Fatal("the acknowledgement did not reach the remover")
	}
}

func TestRestoreInstance_Route(t *testing.T) {
	var calls instanceCalls
	s := newInstanceDashboard(t, auth.RoleAdmin, &calls)

	w := request(t, s, http.MethodPost, "/api/instances/echo/restore", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d (%s)", w.Code, w.Body.String())
	}
	if calls.restored != "echo" {
		t.Fatalf("restored = %q, want echo", calls.restored)
	}
}

// A removed plugin stays in the list, with what an operator needs to undo it
// and to find the entry in their own file. A removal with nothing left to
// remove is reported separately rather than being invisible.
func TestInstances_ReportsRemovalsAndTheFilesDeclaration(t *testing.T) {
	var calls instanceCalls
	w := request(t, newInstanceDashboard(t, auth.RoleAdmin, &calls),
		http.MethodGet, "/api/instances", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	var got struct {
		Instances     []PluginInstanceInfo `json:"instances"`
		StaleRemovals []StaleRemoval       `json:"stale_removals"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Instances) != 1 {
		t.Fatalf("got %d instances, want the removed one still listed", len(got.Instances))
	}
	inst := got.Instances[0]
	if !inst.Removed || inst.RemovedBy != "user:alice" || !inst.Required {
		t.Fatalf("instance = %+v, want the removal and the required flag", inst)
	}
	if inst.Declaration == nil || inst.Declaration.SettingsKeys[0] != "base_url" {
		t.Fatalf("declaration = %+v, want the file's entry, keys only", inst.Declaration)
	}
	if len(got.StaleRemovals) != 1 || got.StaleRemovals[0].Name != "gone" {
		t.Fatalf("stale removals = %+v, want the orphan reported", got.StaleRemovals)
	}
}
