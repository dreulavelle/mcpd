package app

import (
	"context"
	"strings"
	"testing"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/auth/users"
)

// TestAudit_RecordsTheStableIdentifierNotTheDisplayName is the reason a
// display name is safe to let people change.
//
// The trail names the account by its address. A record keyed on a value its
// own subject can edit would be a record of nothing: rename yourself twice and
// the history of what you did would follow you, or worse, land on somebody
// else. So the name is a rendering resolved when a page is drawn, and the row
// keeps the address it was written with.
func TestAudit_RecordsTheStableIdentifierNotTheDisplayName(t *testing.T) {
	rs := newRemote(t, map[string]string{"getWeather": "Reads the forecast."})
	a := newAppIn(t, t.TempDir())
	ctx := context.Background()

	alice, err := a.accounts.Create(ctx, users.CreateRequest{
		Email:       "alice@example.com",
		Password:    "a-sufficiently-long-passphrase",
		DisplayName: "Alice",
		Role:        auth.RoleAdmin,
		Plugins:     []string{auth.Wildcard},
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	// The actor is exactly what the dashboard passes: the principal's id.
	actor := alice.Principal("ses_1", alice.Plugins, nil).ID
	if actor != "user:alice@example.com" {
		t.Fatalf("principal id = %q, want it built from the address", actor)
	}
	mustImport(t, a, "weather", rs.document())
	// Re-import under the account, so the trail carries a row written by it.
	if err := a.ImportMCPServer(ctx, actor, "weather2", rs.document()); err != nil {
		t.Fatalf("import: %v", err)
	}

	rename := func(to string) {
		t.Helper()
		if _, err := a.accounts.Update(ctx, alice.ID, users.UpdateRequest{DisplayName: &to}); err != nil {
			t.Fatalf("rename to %q: %v", to, err)
		}
	}
	actorsFor := func(server string) []string {
		t.Helper()
		records, err := a.audit.Recent(ctx, 100)
		if err != nil {
			t.Fatalf("audit: %v", err)
		}
		var out []string
		for _, rec := range records {
			if rec.Entry.Plugin == server {
				out = append(out, rec.Entry.Actor)
			}
		}
		if len(out) == 0 {
			t.Fatalf("no audit record for %q", server)
		}
		return out
	}

	before := actorsFor("weather2")
	rename("Alice Anderson")
	rename("A. Anderson")

	after := actorsFor("weather2")
	if len(before) != len(after) {
		t.Fatalf("the trail changed length across a rename: %d then %d", len(before), len(after))
	}
	for i, got := range after {
		if got != actor {
			t.Errorf("record %d names %q, want the stable identifier %q", i, got, actor)
		}
		if strings.Contains(got, "Anderson") || got == "Alice" {
			t.Errorf("record %d carries a display name: %q", i, got)
		}
	}
	if before[0] != after[0] {
		t.Errorf("the recorded actor moved from %q to %q", before[0], after[0])
	}
}

// The account may rename itself, and the rename does not touch the address the
// trail is keyed on.
func TestDisplayName_RenamingDoesNotMoveTheIdentity(t *testing.T) {
	a := newAppIn(t, t.TempDir())
	ctx := context.Background()

	alice, err := a.accounts.Create(ctx, users.CreateRequest{
		Email: "alice@example.com", Password: "a-sufficiently-long-passphrase",
		Role: auth.RoleUser, Plugins: []string{auth.Wildcard},
	})
	if err != nil {
		t.Fatal(err)
	}
	name := "Alice"
	if _, err := a.accounts.Update(ctx, alice.ID, users.UpdateRequest{DisplayName: &name}); err != nil {
		t.Fatal(err)
	}
	after, err := a.accounts.ByID(ctx, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Email != "alice@example.com" {
		t.Errorf("email = %q, want it unchanged", after.Email)
	}
	if got := after.Principal("ses_1", after.Plugins, nil).ID; got != "user:alice@example.com" {
		t.Errorf("principal id = %q, want it unchanged by a rename", got)
	}
	if got := after.Principal("ses_1", after.Plugins, nil).DisplayName; got != "Alice" {
		t.Errorf("principal display name = %q, want the new name", got)
	}
}
