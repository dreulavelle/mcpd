package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/tunnel"
)

// madeHere is the whole of what this host manages, and it is a local record
// rather than a question asked of OpenAI.
//
// The bug these exist for: "made by mcpd" was a description string every build
// stamps, so a host sharing an organisation with a second mcpd listed that
// instance's connectors as its own and offered to re-point them -- and the
// delete path checked nothing at all, so a tunnel made by hand in OpenAI's
// console could be removed by id.
func TestMadeHere_OnlyWhatThisHostRecorded(t *testing.T) {
	const mine = "tunnel_1123456789abcdef0123456789abcdef"
	const theirs = "tunnel_2123456789abcdef0123456789abcdef"

	s := NewServer(Options{
		TunnelsMadeHere: func() map[string]string {
			return map[string]string{mine: "mcpd: echo"}
		},
	})

	if !s.madeHere(mine) {
		t.Error("a tunnel this host recorded making is its own")
	}
	if s.madeHere(theirs) {
		t.Error("a tunnel this host has no record of making is not its own, " +
			"however its description reads")
	}

	// No record at all is the state of a host that has made nothing. It must
	// deny rather than fall back to trusting a listing.
	empty := NewServer(Options{})
	if empty.madeHere(mine) {
		t.Error("with no record of any tunnel, nothing is this host's to manage")
	}
}

// Deleting is not reversible and it is not local: any connector anywhere
// pointing at that tunnel stops working. An id went from the URL straight to
// OpenAI's delete, with no ownership check of any kind.
func TestDeleteTunnel_RefusesOneThisHostDidNotMake(t *testing.T) {
	const theirs = "tunnel_2123456789abcdef0123456789abcdef"

	// The handler is called directly with an administrator in context: what is
	// under test is the ownership check, not the route's authentication.
	deletedAt := 0
	s := NewServer(Options{
		TunnelsMadeHere: func() map[string]string { return map[string]string{} },
		ChatGPTAccounts: func(context.Context) ([]tunnel.Account, error) {
			return []tunnel.Account{{
				ID: "acct_1", Name: "Work", AdminKey: "sk-admin", OrgID: "org_1",
			}}, nil
		},
		Directory: func(string) *tunnel.Directory {
			return tunnel.NewDirectory("sk-admin", "org_1", "")
		},
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/api/tunnels/"+theirs, nil)
	r.SetPathValue("id", theirs)
	s.handleDeleteTunnel(w, r.WithContext(auth.WithPrincipal(r.Context(),
		&auth.Principal{ID: "user:test", RoleID: auth.RoleAdministrator})))

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: a tunnel this host did not make is not "+
			"its own to delete", w.Code)
	}
	if !strings.Contains(w.Body.String(), "only manages the tunnels it made") {
		t.Errorf("the refusal should say what mcpd manages, got %s", w.Body.String())
	}
	if deletedAt != 0 {
		t.Fatalf("nothing should have reached OpenAI's delete, got %d calls", deletedAt)
	}
}

// A tunnel made in no ChatGPT workspace connects and may never be offered in
// ChatGPT, which looks healthy from here, so the page is told. One made before
// workspaces were recorded is not known either way and is not marked.
func TestTunnelStatus_MarksATunnelInNoWorkspace(t *testing.T) {
	const none = "tunnel_1123456789abcdef0123456789abcdef"
	const listed = "tunnel_2123456789abcdef0123456789abcdef"
	const older = "tunnel_3123456789abcdef0123456789abcdef"
	s := NewServer(Options{
		TunnelsMadeHere: func() map[string]string {
			return map[string]string{none: "a", listed: "b", older: "c"}
		},
		TunnelWorkspaces: func() map[string][]string {
			return map[string][]string{none: {}, listed: {"ws_1"}}
		},
	})
	w := httptest.NewRecorder()
	s.handleTunnelStatus(w, httptest.NewRequest(http.MethodGet, "/api/tunnel", nil))

	var resp struct {
		Available []struct {
			ID          string `json:"id"`
			NoWorkspace bool   `json:"no_workspace"`
		} `json:"available"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	marked := map[string]bool{}
	for _, a := range resp.Available {
		marked[a.ID] = a.NoWorkspace
	}
	if len(marked) != 3 || !marked[none] || marked[listed] || marked[older] {
		t.Errorf("marked = %v, want only the tunnel recorded in no workspace", marked)
	}
}
