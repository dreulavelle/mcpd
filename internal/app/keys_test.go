package app

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/auth/apikeys"
	"github.com/spoked/mcpd/internal/auth/groups"
)

var toolsList = map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}

// A key created in the dashboard reaches the MCP listener like any other
// bearer credential, and it reaches exactly what it is granted -- an
// ungranted plugin is 404 rather than 403, so a scoped agent cannot discover
// which others are deployed.
func TestKey_AuthenticatesAndIsScopedLikeAnyOtherCredential(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	h := a.Handler()

	_, granted, err := a.keys.Create(ctx, "user:admin@example.com", apikeys.CreateRequest{
		Name: "granted", Role: auth.RoleUser, Plugins: []string{"echo"},
	})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	_, bare, err := a.keys.Create(ctx, "user:admin@example.com", apikeys.CreateRequest{
		Name: "bare", Role: auth.RoleUser,
	})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}

	if w := mcpRequest(t, h, "/mcp/echo", granted, toolsList); w.Code != http.StatusOK {
		t.Fatalf("granted key = %d, want 200\nbody: %s", w.Code, w.Body.String())
	}
	// Default none: a key with no grants and no groups reaches nothing, and
	// learns nothing about what is mounted.
	if w := mcpRequest(t, h, "/mcp/echo", bare, toolsList); w.Code != http.StatusNotFound {
		t.Errorf("key with no grants = %d, want 404", w.Code)
	}
}

// A group is how a key usually gets its reach, and joining one takes effect on
// the next request rather than the next restart.
func TestKey_AGroupWidensReachOnTheNextRequest(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	h := a.Handler()
	const admin = "user:admin@example.com"

	key, secret, err := a.keys.Create(ctx, admin, apikeys.CreateRequest{
		Name: "agent", Role: auth.RoleUser,
	})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	if w := mcpRequest(t, h, "/mcp/echo", secret, toolsList); w.Code != http.StatusNotFound {
		t.Fatalf("before the group = %d, want 404", w.Code)
	}

	g, err := a.groups.Create(ctx, admin, groups.CreateRequest{
		Name: "Echo", Plugins: []string{"echo"},
	})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := a.groups.AddMember(ctx, admin, g.ID, groups.Key(key.ID)); err != nil {
		t.Fatalf("add: %v", err)
	}

	if w := mcpRequest(t, h, "/mcp/echo", secret, toolsList); w.Code != http.StatusOK {
		t.Fatalf("after joining the group = %d, want 200\nbody: %s", w.Code, w.Body.String())
	}

	// And leaving takes it away again, on the next call.
	if err := a.groups.RemoveMember(ctx, admin, g.ID, groups.Key(key.ID)); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if w := mcpRequest(t, h, "/mcp/echo", secret, toolsList); w.Code != http.StatusNotFound {
		t.Errorf("after leaving the group = %d, want 404", w.Code)
	}
}

// Revocation and expiry are refused on the next request, with no restart, and
// both look identical to whoever presented the credential.
func TestKey_RevokedAndExpiredAreRefusedIdentically(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	h := a.Handler()
	const admin = "user:admin@example.com"

	revoked, revokedSecret, err := a.keys.Create(ctx, admin, apikeys.CreateRequest{
		Name: "revoked", Role: auth.RoleUser, Plugins: []string{"echo"},
	})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	if w := mcpRequest(t, h, "/mcp/echo", revokedSecret, toolsList); w.Code != http.StatusOK {
		t.Fatalf("before revocation = %d, want 200", w.Code)
	}
	if err := a.keys.Revoke(ctx, admin, revoked.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if w := mcpRequest(t, h, "/mcp/echo", revokedSecret, toolsList); w.Code != http.StatusUnauthorized {
		t.Errorf("after revocation = %d, want 401 on the very next request", w.Code)
	}

	// An expiry a second away, so the key is real when it is issued and dead
	// when it is presented. The alternative -- a fake clock -- would be
	// testing the store rather than the wiring, which apikeys already does.
	soon := time.Now().Add(50 * time.Millisecond)
	_, expiring, err := a.keys.Create(ctx, admin, apikeys.CreateRequest{
		Name: "expiring", Role: auth.RoleUser, Plugins: []string{"echo"},
		ExpiresAt: &soon,
	})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if w := mcpRequest(t, h, "/mcp/echo", expiring, toolsList); w.Code == http.StatusUnauthorized {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("an expired key was still accepted")
}

// The credentials the live deployment authenticates with keep working, and
// keep reaching exactly what they did. A file token has no row, so nothing on
// the Groups or Keys pages can widen or narrow it.
func TestStaticTokens_AreUnaffectedByGroupsAndKeys(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	h := a.Handler()
	const admin = "user:admin@example.com"

	if w := mcpRequest(t, h, "/mcp/echo", tokenScoped, toolsList); w.Code != http.StatusOK {
		t.Fatalf("scoped file token = %d, want 200", w.Code)
	}

	// A group granting everything, with the file token's own id in it as far
	// as anything could contrive.
	g, err := a.groups.Create(ctx, admin, groups.CreateRequest{
		Name: "Everything", Plugins: []string{auth.Wildcard},
	})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := a.groups.AddMember(ctx, admin, g.ID, groups.Key("scoped")); err == nil {
		t.Error("a file token was put in a group")
	}

	if w := mcpRequest(t, h, "/mcp/echo", tokenScoped, toolsList); w.Code != http.StatusOK {
		t.Errorf("scoped file token after groups existed = %d, want 200", w.Code)
	}
	if w := mcpRequest(t, h, "/mcp/proxmox", tokenScoped, toolsList); w.Code != http.StatusNotFound {
		t.Errorf("scoped file token reached %d for an ungranted plugin, want 404", w.Code)
	}
}
