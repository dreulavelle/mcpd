package sqlite

import (
	"context"
	"errors"
	"slices"
	"testing"

	"encoding/base64"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/tunnel"
)

// testCipher is a reversible transform, not encryption.
//
// The real one lives in internal/settings, which imports this package -- so a
// test here cannot reach it without a cycle. That is no loss: what these tests
// defend is that the store encrypts before writing and decrypts after reading,
// and a transform that visibly changes the bytes proves that as well as AES
// would. The cipher's own strength is tested where the cipher is.
type testCipher struct{}

func (testCipher) Encrypt(plaintext string) (string, error) {
	return base64.StdEncoding.EncodeToString([]byte(plaintext)), nil
}

func (testCipher) Decrypt(ciphertext string) (string, error) {
	out, err := base64.StdEncoding.DecodeString(ciphertext)
	return string(out), err
}

func newAccountStore(t *testing.T) *ChatGPTAccountStore {
	t.Helper()
	return NewChatGPTAccountStore(newTestDB(t), testCipher{}, nil)
}

func sampleAccount(name string) tunnel.Account {
	return tunnel.Account{
		Name:    name,
		APIKey:  "sk-runtime-" + name,
		RoleID:  auth.RoleOperator,
		Grants:  auth.Grants{{Plugin: auth.Wildcard, Level: auth.LevelWrite}},
		Enabled: true,
	}
}

// A credential that reached the database in the clear would be readable by
// anybody with the file, which is the whole reason the settings store
// encrypts the one this table replaced.
func TestAnAccountKeyIsEncryptedAtRest(t *testing.T) {
	ctx := context.Background()
	s := newAccountStore(t)

	created, err := s.Create(ctx, "user:test", sampleAccount("Work"))
	if err != nil {
		t.Fatal(err)
	}

	var stored string
	if err := s.db.Reader().QueryRowContext(ctx,
		`SELECT api_key FROM chatgpt_accounts WHERE id = ?`, created.ID).
		Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == "sk-runtime-Work" {
		t.Fatal("the API key is in the database in the clear")
	}

	// And it comes back readable, or the tunnels cannot authenticate.
	got, ok, err := s.Get(ctx, created.ID)
	if err != nil || !ok {
		t.Fatalf("Get = %v, %v", ok, err)
	}
	if got.APIKey != "sk-runtime-Work" {
		t.Fatalf("key round-tripped to %q", got.APIKey)
	}
}

// The dashboard never reads a key back, so an edit that changes only the rate
// limit arrives carrying no key at all. Reading that as an instruction to
// erase one would take every tunnel on the account offline.
func TestAnEditWithoutAKeyKeepsTheStoredOne(t *testing.T) {
	ctx := context.Background()
	s := newAccountStore(t)

	created, err := s.Create(ctx, "user:test", sampleAccount("Work"))
	if err != nil {
		t.Fatal(err)
	}

	rate := 5.0
	updated, err := s.Update(ctx, "user:test", created.ID, tunnel.AccountUpdate{RatePerSec: &rate})
	if err != nil {
		t.Fatal(err)
	}
	if updated.APIKey != "sk-runtime-Work" {
		t.Fatalf("key = %q after an edit that did not mention it", updated.APIKey)
	}
	if updated.RatePerSec != 5 {
		t.Fatalf("rate = %v, want the edited value", updated.RatePerSec)
	}
}

// Two accounts nobody can tell apart are two accounts nobody can assign a
// tunnel to with any confidence.
func TestAccountNamesAreUniqueIgnoringCase(t *testing.T) {
	ctx := context.Background()
	s := newAccountStore(t)

	if _, err := s.Create(ctx, "user:test", sampleAccount("Work")); err != nil {
		t.Fatal(err)
	}
	clash := sampleAccount("work")
	// A distinct principal, so it is the name being refused and not the
	// identity -- otherwise this test passes for the wrong reason.
	clash.Principal = "svc:chatgpt:other"
	if _, err := s.Create(ctx, "user:test", clash); !errors.Is(err, ErrAccountExists) {
		t.Fatalf("creating a same-name account = %v, want ErrAccountExists", err)
	}
}

// Two accounts sharing an identity would put two workspaces' calls under one
// name in the audit trail, which is most of what accounts exist to separate.
func TestAccountPrincipalsAreUnique(t *testing.T) {
	ctx := context.Background()
	s := newAccountStore(t)

	first := sampleAccount("Work")
	first.Principal = "svc:chatgpt:shared"
	if _, err := s.Create(ctx, "user:test", first); err != nil {
		t.Fatal(err)
	}
	second := sampleAccount("Home")
	second.Principal = "svc:chatgpt:shared"
	if _, err := s.Create(ctx, "user:test", second); !errors.Is(err, ErrAccountExists) {
		t.Fatalf("creating a same-principal account = %v, want ErrAccountExists", err)
	}
}

// An admin key with no organisation cannot list a single tunnel, because the
// API scopes every request to exactly one. Refused where it is typed rather
// than at the first request that comes back unexplained.
func TestAnAdminKeyNeedsAnOrganisation(t *testing.T) {
	ctx := context.Background()
	s := newAccountStore(t)

	acct := sampleAccount("Work")
	acct.AdminKey = "sk-admin"
	if _, err := s.Create(ctx, "user:test", acct); err == nil {
		t.Fatal("an admin key with no organization ID was accepted")
	}

	acct.OrgID = "org_123"
	if _, err := s.Create(ctx, "user:test", acct); err != nil {
		t.Fatalf("an admin key with its organization was refused: %v", err)
	}
}

// The name seeds the principal, which lands in the audit trail and in a plugin
// grant, so it is deliberately narrow about what it may hold.
func TestAccountValidationRefusesWhatItCannotName(t *testing.T) {
	ctx := context.Background()
	s := newAccountStore(t)

	for _, tc := range []struct {
		why  string
		edit func(*tunnel.Account)
	}{
		{"no name", func(a *tunnel.Account) { a.Name = "" }},
		{"no key", func(a *tunnel.Account) { a.APIKey = "" }},
		{"a name with punctuation", func(a *tunnel.Account) { a.Name = "work/prod" }},
		{"an unknown role", func(a *tunnel.Account) { a.RoleID = "role_superuser" }},
		{"a negative rate", func(a *tunnel.Account) { a.RatePerSec = -1 }},
	} {
		t.Run(tc.why, func(t *testing.T) {
			acct := sampleAccount("Work")
			tc.edit(&acct)
			if _, err := s.Create(ctx, "user:test", acct); err == nil {
				t.Fatalf("%s was accepted", tc.why)
			}
		})
	}
}

// An account is a privilege grant with a credential attached, so adding,
// editing and removing one belong in the hash-chained trail rather than
// nowhere.
func TestAccountChangesAreAudited(t *testing.T) {
	ctx := context.Background()
	s := newAccountStore(t)
	audit := NewAuditStore(s.db)

	created, err := s.Create(ctx, "user:alice", sampleAccount("Work"))
	if err != nil {
		t.Fatal(err)
	}
	enabled := false
	if _, err := s.Update(ctx, "user:alice", created.ID,
		tunnel.AccountUpdate{Enabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "user:alice", created.ID); err != nil {
		t.Fatal(err)
	}

	entries, err := audit.Recent(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"chatgpt.account.added":   false,
		"chatgpt.account.updated": false,
		"chatgpt.account.removed": false,
	}
	for _, e := range entries {
		if _, ok := want[e.Entry.Kind]; ok {
			want[e.Entry.Kind] = true
		}
	}
	for kind, seen := range want {
		if !seen {
			t.Errorf("%s was not recorded", kind)
		}
	}
}

// An edit that changes nothing is not an event. A trail that records somebody
// opening a form and closing it is one nobody reads carefully.
func TestAnEditThatChangesNothingIsNotRecorded(t *testing.T) {
	ctx := context.Background()
	s := newAccountStore(t)
	audit := NewAuditStore(s.db)

	created, err := s.Create(ctx, "user:test", sampleAccount("Work"))
	if err != nil {
		t.Fatal(err)
	}
	before, err := audit.Recent(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}

	same := created.Name
	if _, err := s.Update(ctx, "user:test", created.ID,
		tunnel.AccountUpdate{Name: &same}); err != nil {
		t.Fatal(err)
	}
	after, err := audit.Recent(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("a no-op edit wrote %d entries", len(after)-len(before))
	}
}

// Removing something that is not there is a refusal, not a silent success:
// a caller that deleted nothing should not be told it deleted something.
func TestRemovingAnUnknownAccountIsRefused(t *testing.T) {
	s := newAccountStore(t)
	if err := s.Delete(context.Background(), "user:test", "acct_missing"); !errors.Is(err, ErrNoSuchAccount) {
		t.Fatalf("Delete = %v, want ErrNoSuchAccount", err)
	}
}

// The workspaces are the account's own and survive a round trip through the
// store, normalised: trimmed, deduplicated, sorted, and never null.
func TestChatGPTAccount_WorkspacesRoundTrip(t *testing.T) {
	s := newAccountStore(t)
	ctx := context.Background()
	created, err := s.Create(ctx, "user:test", tunnel.Account{
		Name: "Work", APIKey: "sk-runtime", RoleID: auth.RoleOperator,
		Grants:  auth.Grants{{Plugin: auth.Wildcard, Level: auth.LevelWrite}},
		Enabled: true, Workspaces: []string{" ws_b", "ws_a", "ws_a", ""},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Get(ctx, created.ID)
	if err != nil || !ok {
		t.Fatalf("get: %v %v", ok, err)
	}
	if want := []string{"ws_a", "ws_b"}; !slices.Equal(got.Workspaces, want) {
		t.Fatalf("workspaces = %v, want %v", got.Workspaces, want)
	}
	none := []string{}
	if _, err := s.Update(ctx, "user:test", created.ID, tunnel.AccountUpdate{Workspaces: &none}); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Get(ctx, created.ID)
	if got.Workspaces == nil || len(got.Workspaces) != 0 {
		t.Fatalf("cleared workspaces = %#v, want an empty list", got.Workspaces)
	}
}
