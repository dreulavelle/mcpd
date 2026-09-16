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
			// A workspace beyond the ones asked for, because OpenAI may list a
			// tunnel in more than were requested -- and those are the account's
			// own by definition: it just made a tunnel in them.
			ws, _ := body["workspace_ids"].([]any)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "tunnel_abcdef0123456789abcdef0123456789", "name": body["name"],
				"workspace_ids": append(append([]any{}, ws...), "ws_made"),
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
		RoleID: auth.RoleOperator,
		Grants: auth.Grants{{Plugin: auth.Wildcard, Level: auth.LevelWrite}}, Enabled: true,
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
	// The account's own saved workspaces, and only those. This used to union
	// in the workspaces of every tunnel in the organisation, which meant a
	// workspace id was learned from -- and stored against this host's account
	// from -- connectors other people and other mcpd instances had made.
	ws, _ := json.Marshal(cp.created[0]["workspace_ids"])
	if string(ws) != `["ws_own"]` {
		t.Fatalf("listed in %s, want only the account's own saved workspace", ws)
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
	// And the account learned the workspaces the new tunnel came back listed
	// in -- from the create's own response, never from a listing of tunnels
	// this host did not make.
	again, _, _ := a.chatgpt.Get(ctx, acct.ID)
	if strings.Join(again.Workspaces, ",") != "ws_made,ws_own" {
		t.Errorf("account workspaces = %v, want the created tunnel's added", again.Workspaces)
	}
}

// The record of what this host made is what the page, the assign endpoint and
// the delete endpoint all read. A create writes it; nothing else does.
func TestMakeTunnel_RecordsThatThisHostMadeIt(t *testing.T) {
	cp := &fakeControlPlane{}
	a, acct := pipelineApp(t, cp)
	ctx := context.Background()

	made, err := a.MakeTunnel(ctx, "user:test", MakeTunnelRequest{Plugin: "", Account: acct.ID})
	if err != nil {
		t.Fatalf("MakeTunnel: %v", err)
	}

	mine := a.tunnelsMadeHere(ctx)
	if _, ok := mine[made.TunnelID]; !ok {
		t.Fatalf("made %s and did not record it as this host's: %v", made.TunnelID, mine)
	}
	if mine[made.TunnelID] != "mcpd" {
		t.Errorf("recorded name = %q, want the name it was made with", mine[made.TunnelID])
	}

	// The organisation's other tunnels -- the fake control plane lists one
	// nobody here made -- are not this host's, whatever their description
	// says. Every mcpd build stamps the same one.
	if _, ok := mine["tunnel_0123456789abcdef0123456789abcdef"]; ok {
		t.Error("a tunnel this host did not make must not be recorded as its own")
	}
	if len(mine) != 1 {
		t.Errorf("manages %d tunnels, want only the one it made: %v", len(mine), mine)
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
	if !got.CanList || !got.CanMake {
		t.Fatalf("check = %+v", got)
	}
	if len(cp.created) != 1 || len(cp.deleted) != 1 {
		t.Fatalf("the probe should be made once and deleted once: made %d, deleted %d", len(cp.created), len(cp.deleted))
	}
	if cp.created[0]["workspace_ids"] != nil {
		t.Error("the probe must be organisation-only, so it appears nowhere")
	}
}
