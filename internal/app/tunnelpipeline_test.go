package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
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
	// unverified are workspaces OpenAI cannot verify belong to the
	// organisation: a create naming one is refused with the association code.
	unverified []string
	// posts counts every create asked for, made or refused.
	posts   int
	created []map[string]any
	deleted []string
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
			f.posts++
			named, _ := body["workspace_ids"].([]any)
			for _, ws := range named {
				if slices.Contains(f.unverified, ws.(string)) {
					w.Header().Set("x-request-id", "req_unverified")
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte(`{"error":{"code":"tunnel_principal_association_unverified","message":"We couldn't automatically verify the association between these workspaces and organizations."}}`))
					return
				}
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
	// The organisation on its own, then each workspace on its own, so a
	// refusal names what was refused. An organisation-only probe was all
	// this made once, and it passed on an account whose workspace OpenAI
	// refused at every Make.
	if len(cp.created) != 2 || len(cp.deleted) != 2 {
		t.Fatalf("made %d probes and deleted %d, want 2 of each", len(cp.created), len(cp.deleted))
	}
	if cp.created[0]["workspace_ids"] != nil {
		t.Errorf("the first probe should name no workspace: %v", cp.created[0])
	}
	if ws, _ := json.Marshal(cp.created[1]["workspace_ids"]); string(ws) != `["ws_own"]` {
		t.Errorf("the second probe listed in %s, want the account's workspace", ws)
	}
	if len(got.Pairings) != 2 || got.Pairings[1].Status != tunnel.PairingVerified {
		t.Errorf("pairings = %+v, want the organisation and ws_own verified", got.Pairings)
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
	if got.Reason != tunnel.ReasonWorkspaceRefused || !strings.Contains(got.Problem, "ws_own") {
		t.Errorf("reason = %q, upstream = %q; want the workspace refusal with OpenAI's words", got.Reason, got.Upstream)
	}
	if len(cp.created) != len(cp.deleted) {
		t.Errorf("every probe made should be deleted: made %d, deleted %d", len(cp.created), len(cp.deleted))
	}
}

// A workspace OpenAI cannot verify is named on its own, beside the ones it
// can: an account with one bad workspace of several was a refusal with
// nothing in it to act on. What was found is kept on the account.
func TestCheckChatGPTAccount_NamesTheWorkspaceOpenAICannotVerify(t *testing.T) {
	cp := &fakeControlPlane{unverified: []string{"ws_bad"}}
	a, acct := pipelineApp(t, cp)
	ctx := context.Background()
	both := []string{"ws_own", "ws_bad"}
	if _, err := a.chatgpt.Update(ctx, "user:test", acct.ID, tunnel.AccountUpdate{Workspaces: &both}); err != nil {
		t.Fatal(err)
	}

	got, err := a.CheckChatGPTAccount(ctx, acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CanMake || !strings.Contains(got.Problem, "ws_bad") || strings.Contains(got.Problem, "ws_own") {
		t.Fatalf("check = %+v, want ws_bad named and ws_own not", got)
	}
	kept := map[string]tunnel.PairingStatus{}
	for _, p := range a.ChatGPTPairings(ctx)[acct.ID] {
		kept[p.Workspace] = p.Status
	}
	if kept[""] != tunnel.PairingVerified || kept["ws_own"] != tunnel.PairingVerified || kept["ws_bad"] != tunnel.PairingUnverified {
		t.Errorf("kept = %v", kept)
	}
	if len(cp.created) != len(cp.deleted) {
		t.Errorf("every probe made should be deleted: made %d, deleted %d", len(cp.created), len(cp.deleted))
	}
}

// Once OpenAI has said it cannot verify a workspace, Make says so with what it
// said then instead of asking again -- only its Support can change the answer.
// A Check is how somebody says they have, and it clears the way.
func TestMakeTunnel_RemembersAWorkspaceOpenAICannotVerify(t *testing.T) {
	cp := &fakeControlPlane{unverified: []string{"ws_own"}}
	a, acct := pipelineApp(t, cp)
	ctx := context.Background()

	_, err := a.MakeTunnel(ctx, "user:test", MakeTunnelRequest{Account: acct.ID})
	if tunnel.Reason(err) != tunnel.ReasonWorkspaceRefused || !strings.Contains(err.Error(), "OpenAI Support") {
		t.Fatalf("first make: %v, want the unverified workspace named with what to do", err)
	}
	asked := cp.posts

	_, err = a.MakeTunnel(ctx, "user:test", MakeTunnelRequest{Account: acct.ID})
	if tunnel.Reason(err) != tunnel.ReasonWorkspaceRefused || !strings.Contains(tunnel.Upstream(err), "req_unverified") {
		t.Fatalf("second make: %v (upstream %q), want the kept refusal with OpenAI's request id", err, tunnel.Upstream(err))
	}
	if cp.posts != asked {
		t.Errorf("OpenAI was asked again: %d creates, want %d", cp.posts, asked)
	}

	// OpenAI's Support reviews it; a Check sees that and Make works again.
	cp.unverified = nil
	if got, _ := a.CheckChatGPTAccount(ctx, acct.ID); !got.CanMake {
		t.Fatalf("check after the review = %+v", got)
	}
	if _, err := a.MakeTunnel(ctx, "user:test", MakeTunnelRequest{Account: acct.ID}); err != nil {
		t.Fatalf("make after the review: %v", err)
	}
}

// A workspace OpenAI will not accept is refused when the account is saved,
// not discovered at the first Make -- and the account is left as it was.
func TestUpdateChatGPTAccount_RefusesAWorkspaceOpenAICannotVerify(t *testing.T) {
	cp := &fakeControlPlane{unverified: []string{"ws_bad"}}
	a, acct := pipelineApp(t, cp)
	ctx := context.Background()

	bad := []string{"ws_own", "ws_bad"}
	_, err := a.UpdateChatGPTAccount(ctx, "user:test", acct.ID, tunnel.AccountUpdate{Workspaces: &bad})
	if tunnel.Reason(err) != tunnel.ReasonWorkspaceRefused || !strings.Contains(err.Error(), "ws_bad") {
		t.Fatalf("err = %v, want the workspace refused by name", err)
	}
	if tunnel.Upstream(err) == "" {
		t.Error("what OpenAI said should travel with the refusal")
	}
	again, _, _ := a.chatgpt.Get(ctx, acct.ID)
	if strings.Join(again.Workspaces, ",") != "ws_own" {
		t.Errorf("workspaces = %v, want the account unchanged", again.Workspaces)
	}
	// Only the workspace being added was asked about, after the organisation.
	if cp.posts != 2 {
		t.Errorf("asked OpenAI %d times, want the organisation and the one new workspace", cp.posts)
	}
	if len(cp.created) != len(cp.deleted) {
		t.Errorf("every probe made should be deleted: made %d, deleted %d", len(cp.created), len(cp.deleted))
	}

	// An edit that does not touch the pair asks OpenAI nothing.
	cp.posts = 0
	rate := 5.0
	if _, err := a.UpdateChatGPTAccount(ctx, "user:test", acct.ID, tunnel.AccountUpdate{RatePerSec: &rate}); err != nil {
		t.Fatal(err)
	}
	if cp.posts != 0 {
		t.Errorf("an edit to the rate asked OpenAI %d times", cp.posts)
	}
}

// A workspace OpenAI accepts is saved, and the answer kept.
func TestUpdateChatGPTAccount_KeepsWhatOpenAISaidOfANewWorkspace(t *testing.T) {
	cp := &fakeControlPlane{}
	a, acct := pipelineApp(t, cp)
	ctx := context.Background()
	more := []string{"ws_own", "ws_new"}
	if _, err := a.UpdateChatGPTAccount(ctx, "user:test", acct.ID, tunnel.AccountUpdate{Workspaces: &more}); err != nil {
		t.Fatal(err)
	}
	var verified bool
	for _, p := range a.ChatGPTPairings(ctx)[acct.ID] {
		verified = verified || (p.Workspace == "ws_new" && p.Status == tunnel.PairingVerified)
	}
	if !verified {
		t.Errorf("pairings = %+v, want ws_new verified", a.ChatGPTPairings(ctx)[acct.ID])
	}
}

// A tunnel's workspaces are recorded when it is made, so the page can say when
// one is in none: it connects, and ChatGPT may never offer it.
func TestMakeTunnel_RecordsTheWorkspacesItWasMadeIn(t *testing.T) {
	cp := &fakeControlPlane{}
	a, acct := pipelineApp(t, cp)
	ctx := context.Background()
	made, err := a.MakeTunnel(ctx, "user:test", MakeTunnelRequest{Account: acct.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.tunnelWorkspaces(ctx)[made.TunnelID]; strings.Join(got, ",") != "ws_made,ws_own" {
		t.Errorf("recorded workspaces = %v", got)
	}
}
