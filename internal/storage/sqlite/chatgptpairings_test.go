package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/tunnel"
)

func pairedAccount(t *testing.T, s *ChatGPTAccountStore) tunnel.Account {
	t.Helper()
	a := sampleAccount("Work")
	a.AdminKey, a.OrgID = "sk-admin-work", "org_1"
	a.Workspaces = []string{"ws_a", "ws_b"}
	created, err := s.Create(context.Background(), "user:test", a)
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func statuses(ps []tunnel.Pairing) map[string]tunnel.PairingStatus {
	out := map[string]tunnel.PairingStatus{}
	for _, p := range ps {
		out[p.Workspace] = p.Status
	}
	return out
}

// What OpenAI said about a pair is kept, and a second answer replaces the
// first rather than sitting beside it.
func TestPairings_TheLastAnswerIsKept(t *testing.T) {
	s := newAccountStore(t)
	ctx := context.Background()
	a := pairedAccount(t, s)
	at := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

	if err := s.RecordPairings(ctx, a.ID, "org_1", []tunnel.Pairing{
		{Workspace: "ws_a", Status: tunnel.PairingUnverified, Upstream: "req_1", CheckedAt: at},
		{Workspace: "ws_b", Status: tunnel.PairingVerified, CheckedAt: at},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPairings(ctx, a.ID, "org_1", []tunnel.Pairing{
		{Workspace: "ws_a", Status: tunnel.PairingVerified, CheckedAt: at.Add(time.Hour)},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Pairings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st := statuses(got[a.ID]); len(st) != 2 || st["ws_a"] != tunnel.PairingVerified || st["ws_b"] != tunnel.PairingVerified {
		t.Fatalf("pairings = %+v", got[a.ID])
	}
}

// An answer about another organisation is not an answer about this one: a
// check that lands after somebody changed the organisation is filed nowhere,
// and answers from before the change stop being read.
func TestPairings_AreAboutTheOrganisationTheAccountNames(t *testing.T) {
	s := newAccountStore(t)
	ctx := context.Background()
	a := pairedAccount(t, s)
	now := time.Now()

	if err := s.RecordPairings(ctx, a.ID, "org_other", []tunnel.Pairing{
		{Workspace: "ws_a", Status: tunnel.PairingVerified, CheckedAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Pairings(ctx); len(got[a.ID]) != 0 {
		t.Fatalf("an answer about another organisation was filed: %+v", got[a.ID])
	}

	if err := s.RecordPairings(ctx, a.ID, "org_1", []tunnel.Pairing{
		{Workspace: "ws_a", Status: tunnel.PairingVerified, CheckedAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	org := "org_2"
	if _, err := s.Update(ctx, "user:test", a.ID, tunnel.AccountUpdate{OrgID: &org}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Pairings(ctx); len(got[a.ID]) != 0 {
		t.Fatalf("an answer about the previous organisation is still read: %+v", got[a.ID])
	}
}

// A workspace taken off the account takes its answer with it, and removing
// the account removes them all.
func TestPairings_GoWithTheirWorkspaceAndAccount(t *testing.T) {
	s := newAccountStore(t)
	ctx := context.Background()
	a := pairedAccount(t, s)
	now := time.Now()
	if err := s.RecordPairings(ctx, a.ID, "org_1", []tunnel.Pairing{
		{Workspace: "ws_a", Status: tunnel.PairingUnverified, CheckedAt: now},
		{Workspace: "ws_b", Status: tunnel.PairingVerified, CheckedAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	only := []string{"ws_b"}
	if _, err := s.Update(ctx, "user:test", a.ID, tunnel.AccountUpdate{Workspaces: &only}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Pairings(ctx); len(got[a.ID]) != 1 || got[a.ID][0].Workspace != "ws_b" {
		t.Fatalf("pairings after removing ws_a = %+v", got[a.ID])
	}
	if err := s.Delete(ctx, "user:test", a.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.Reader().QueryRow(`SELECT COUNT(*) FROM chatgpt_pairings`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows left after removing the account: %d, %v", n, err)
	}
}
