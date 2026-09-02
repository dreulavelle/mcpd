package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/settings"
	"github.com/spoked/mcpd/internal/tunnel"
)

// A stand-in for OpenAI's tunnel management API: lists one tunnel in a
// workspace, makes tunnels, deletes them, and can be told to refuse.
type fakeControlPlane struct {
	refuseCreate bool
	created      []map[string]any
	deleted      []string
}

func (f *fakeControlPlane) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tunnels":
			_ = json.NewEncoder(w).Encode(map[string]any{"tunnels": []map[string]any{
				{"id": "tunnel_0123456789abcdef0123456789abcdef", "name": "existing", "workspace_ids": []string{"ws_seen"}},
			}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tunnels":
			if f.refuseCreate {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":{"message":"forbidden"}}`))
				return
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.created = append(f.created, body)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "tunnel_abcdef0123456789abcdef0123456789", "name": body["name"],
				"workspace_ids": body["workspace_ids"],
			})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/tunnels/"):
			f.deleted = append(f.deleted, strings.TrimPrefix(r.URL.Path, "/v1/tunnels/"))
			_ = json.NewEncoder(w).Encode(map[string]any{"id": strings.TrimPrefix(r.URL.Path, "/v1/tunnels/")})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func pipelineApp(t *testing.T, cp *fakeControlPlane) (*App, tunnel.Account) {
	t.Helper()
	a := newSettingsApp(t)
	ctx := context.Background()
	srv := httptest.NewServer(cp.handler())
	t.Cleanup(srv.Close)
	if err := a.settings.Apply(ctx, "user:test", []settings.Change{
		{Key: settings.KeyTunnelControlPlane, Value: `"` + srv.URL + `"`},
	}); err != nil {
		t.Fatal(err)
	}
	acct, err := a.chatgpt.Create(ctx, "user:test", tunnel.Account{
		Name: "Work", APIKey: "sk-runtime", AdminKey: "sk-admin-test", OrgID: "org_test",
		Role: auth.RoleUser, Plugins: []string{auth.Wildcard}, Enabled: true,
		Workspaces: []string{"ws_own"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return a, acct
}

// One call does the whole job: the tunnel is made in every workspace the
// account knows -- its own and the ones its tunnels report -- pointed at
// the system, and the subsystem switched on. Nobody supplies a workspace.
func TestMakeTunnel_IsTheWholePipeline(t *testing.T) {
	cp := &fakeControlPlane{}
	a, acct := pipelineApp(t, cp)
	ctx := context.Background()

	made, err := a.MakeTunnel(ctx, "user:test", MakeTunnelRequest{Plugin: "", Account: acct.ID})
	if err != nil {
		t.Fatalf("MakeTunnel: %v", err)
	}
	if len(cp.created) != 1 {
		t.Fatalf("created %d tunnels, want 1", len(cp.created))
	}
	ws, _ := json.Marshal(cp.created[0]["workspace_ids"])
	if string(ws) != `["ws_own","ws_seen"]` {
		t.Fatalf("listed in %s, want the account's own workspace and the one its tunnels use", ws)
	}
	if cp.created[0]["name"] != "mcpd" {
		t.Errorf("name = %v, want mcpd for everything", cp.created[0]["name"])
	}

	// Assigned to everything, under the account, and switched on.
	at := a.assignedTunnels(ctx)
	if len(at) != 1 || at[0].TunnelID != made.TunnelID || at[0].Plugin != settings.TunnelEverything || at[0].Account != acct.ID {
		t.Fatalf("assignment = %+v", at)
	}
	if !a.settings.FieldBool(ctx, settings.KeyTunnelEnabled) {
		t.Error("tunnels were not switched on")
	}
	// And the account learned the workspace its tunnels use.
	again, _, _ := a.chatgpt.Get(ctx, acct.ID)
	if strings.Join(again.Workspaces, ",") != "ws_own,ws_seen" {
		t.Errorf("account workspaces = %v, want the learned one added", again.Workspaces)
	}
}

// A 403 on create with a key that has just listed means the write scope is
// missing, and the error says so rather than "not allowed".
func TestMakeTunnel_ExplainsARefusedCreate(t *testing.T) {
	cp := &fakeControlPlane{refuseCreate: true}
	a, acct := pipelineApp(t, cp)
	_, err := a.MakeTunnel(context.Background(), "user:test", MakeTunnelRequest{Account: acct.ID})
	if err == nil || tunnel.Reason(err) != tunnel.ReasonTunnelsManageRequired {
		t.Fatalf("err = %v, want the manage-required refusal", err)
	}
	if !strings.Contains(err.Error(), "write scope") {
		t.Errorf("the refusal should name the missing scope: %v", err)
	}
	if got := a.assignedTunnels(context.Background()); len(got) != 0 {
		t.Errorf("a refused create must assign nothing: %+v", got)
	}
}

// Checking an account proves both halves by doing them, and leaves nothing
// behind: the probe tunnel is deleted in the same call.
func TestCheckChatGPTAccount_ProvesListAndMake(t *testing.T) {
	cp := &fakeControlPlane{}
	a, acct := pipelineApp(t, cp)
	got, err := a.CheckChatGPTAccount(context.Background(), acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.CanList || !got.CanMake || got.Tunnels != 1 {
		t.Fatalf("check = %+v", got)
	}
	if len(cp.created) != 1 || len(cp.deleted) != 1 {
		t.Fatalf("the probe should be made once and deleted once: made %d, deleted %d", len(cp.created), len(cp.deleted))
	}
	if cp.created[0]["workspace_ids"] != nil {
		t.Error("the probe must be organisation-only, so it appears nowhere")
	}
}
