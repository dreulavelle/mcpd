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
	// refuseWorkspace refuses only a create that names a workspace, which is
	// what OpenAI does when the key may make tunnels and the workspace is not
	// one it will list them in.
	refuseWorkspace bool
	// unverifiedPair refuses a create naming both an organisation and a
	// workspace, as OpenAI does when it cannot verify they belong together.
	unverifiedPair bool
	created        []map[string]any
	deleted        []string
}

func (f *fakeControlPlane) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tunnels":
			_ = json.NewEncoder(w).Encode(map[string]any{"tunnels": []map[string]any{
				{"id": "tunnel_0123456789abcdef0123456789abcdef", "name": "existing", "workspace_ids": []string{"ws_seen"}},
			}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tunnels":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if f.unverifiedPair && body["workspace_ids"] != nil && body["organization_ids"] != nil {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":{"code":"tunnel_principal_association_unverified","message":"We couldn't automatically verify the association between these workspaces and organizations."}}`))
				return
			}
			if f.refuseCreate || (f.refuseWorkspace && body["workspace_ids"] != nil) {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":{"message":"forbidden","code":"forbidden"}}`))
				return
			}
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

// A key refused with and without the workspace lacks the permission, and the
// error says which one rather than "not allowed".
func TestMakeTunnel_ExplainsARefusedCreate(t *testing.T) {
	cp := &fakeControlPlane{refuseCreate: true}
	a, acct := pipelineApp(t, cp)
	_, err := a.MakeTunnel(context.Background(), "user:test", MakeTunnelRequest{Account: acct.ID})
	if err == nil || tunnel.Reason(err) != tunnel.ReasonTunnelsManageRequired {
		t.Fatalf("err = %v, want the manage-required refusal", err)
	}
	if !strings.Contains(err.Error(), "api.organization.tunnel.write") {
		t.Errorf("the refusal should name the missing permission: %v", err)
	}
	if !strings.Contains(tunnel.Upstream(err), "forbidden") {
		t.Errorf("what OpenAI said should travel with the refusal: %q", tunnel.Upstream(err))
	}
	if got := a.assignedTunnels(context.Background()); len(got) != 0 {
		t.Errorf("a refused create must assign nothing: %+v", got)
	}
}

// The bug this exists for: an account's Check passed, and every Make was
// refused with "that key cannot manage tunnels". The key could; OpenAI was
// refusing the workspace saved on the account, which the Check never sent.
// The same key making the same tunnel without the workspace is what tells the
// two apart, and that probe is removed again.
func TestMakeTunnel_TellsARefusedWorkspaceFromARefusedKey(t *testing.T) {
	cp := &fakeControlPlane{refuseWorkspace: true}
	a, acct := pipelineApp(t, cp)
	_, err := a.MakeTunnel(context.Background(), "user:test", MakeTunnelRequest{Account: acct.ID})
	if tunnel.Reason(err) != tunnel.ReasonWorkspaceRefused {
		t.Fatalf("err = %v (reason %q), want the workspace refusal", err, tunnel.Reason(err))
	}
	if !strings.Contains(err.Error(), "ws_own") {
		t.Errorf("the refusal should name the workspace: %v", err)
	}
	if tunnel.Upstream(err) == "" {
		t.Error("what OpenAI said should travel with the refusal")
	}
	if len(cp.created) != 1 || len(cp.deleted) != 1 || cp.deleted[0] != "tunnel_abcdef0123456789abcdef0123456789" {
		t.Fatalf("the probe should be made once and deleted: made %d, deleted %v", len(cp.created), cp.deleted)
	}
	if got := a.assignedTunnels(context.Background()); len(got) != 0 {
		t.Errorf("a refused create must assign nothing, and the probe is not a tunnel: %+v", got)
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
	// Made exactly as Make would make it. An organisation-only probe tested a
	// request nobody sends, and passed on an account whose workspace OpenAI
	// refused.
	ws, _ := json.Marshal(cp.created[0]["workspace_ids"])
	if string(ws) != `["ws_own"]` {
		t.Errorf("probe listed in %s, want the account's own workspace", ws)
	}
}

// A Check on an account whose workspace OpenAI refuses says it cannot make
// tunnels, and why -- rather than passing and leaving Make to find out.
func TestCheckChatGPTAccount_FailsOnARefusedWorkspace(t *testing.T) {
	cp := &fakeControlPlane{refuseWorkspace: true}
	a, acct := pipelineApp(t, cp)
	got, err := a.CheckChatGPTAccount(context.Background(), acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.CanList || got.CanMake {
		t.Fatalf("check = %+v, want can list and cannot make", got)
	}
	if got.Reason != tunnel.ReasonWorkspaceRefused || got.Upstream == "" {
		t.Errorf("reason = %q, upstream = %q; want the workspace refusal with OpenAI's words", got.Reason, got.Upstream)
	}
	if len(cp.created) != len(cp.deleted) {
		t.Errorf("every probe made should be deleted: made %d, deleted %d", len(cp.created), len(cp.deleted))
	}
}

// OpenAI began refusing an organisation and a workspace named together when
// it could not verify the pairing, on an account whose earlier tunnels had
// been made with exactly that pair. The workspace alone has no pairing to
// verify, and it is the shape OpenAI's own documentation creates.
func TestMakeTunnel_MakesItInTheWorkspaceAloneWhenThePairIsUnverified(t *testing.T) {
	cp := &fakeControlPlane{unverifiedPair: true}
	a, acct := pipelineApp(t, cp)
	made, err := a.MakeTunnel(context.Background(), "user:test", MakeTunnelRequest{Account: acct.ID})
	if err != nil {
		t.Fatalf("MakeTunnel: %v", err)
	}
	if len(cp.created) != 1 {
		t.Fatalf("created %d tunnels, want 1", len(cp.created))
	}
	if cp.created[0]["organization_ids"] != nil {
		t.Errorf("the retry should name no organisation: %v", cp.created[0])
	}
	ws, _ := json.Marshal(cp.created[0]["workspace_ids"])
	if string(ws) != `["ws_own"]` {
		t.Errorf("listed in %s, want the account's own workspace", ws)
	}
	if at := a.assignedTunnels(context.Background()); len(at) != 1 || at[0].TunnelID != made.TunnelID {
		t.Errorf("assignment = %+v, want the tunnel made", at)
	}
}

// The Check goes through the same path, so it passes where Make will work.
func TestCheckChatGPTAccount_PassesWhereMakeWillWork(t *testing.T) {
	cp := &fakeControlPlane{unverifiedPair: true}
	a, acct := pipelineApp(t, cp)
	got, err := a.CheckChatGPTAccount(context.Background(), acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.CanMake {
		t.Fatalf("check = %+v, want can make", got)
	}
	if len(cp.created) != 1 || len(cp.deleted) != 1 {
		t.Errorf("the probe should be made once and deleted: made %d, deleted %d", len(cp.created), len(cp.deleted))
	}
}

// When the workspace alone is refused too, the refusal says what OpenAI said:
// the association needs its Support. Not the key's permissions.
func TestMakeTunnel_SendsAnUnverifiablePairToSupport(t *testing.T) {
	cp := &fakeControlPlane{unverifiedPair: true, refuseWorkspace: true}
	a, acct := pipelineApp(t, cp)
	_, err := a.MakeTunnel(context.Background(), "user:test", MakeTunnelRequest{Account: acct.ID})
	if tunnel.Reason(err) != tunnel.ReasonWorkspaceRefused {
		t.Fatalf("err = %v (reason %q), want the workspace refusal", err, tunnel.Reason(err))
	}
	if !strings.Contains(err.Error(), "verify") || !strings.Contains(tunnel.Upstream(err), "tunnel_principal_association_unverified") {
		t.Errorf("err = %v, upstream = %q; want the association named", err, tunnel.Upstream(err))
	}
	if len(cp.created) != 0 {
		t.Errorf("nothing should be left made: %d", len(cp.created))
	}
}
