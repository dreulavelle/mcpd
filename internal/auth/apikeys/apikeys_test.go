package apikeys

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/auth/groups"
	"github.com/spoked/mcpd/internal/storage/sqlite"
)

var testClock = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func newStore(t *testing.T) (*Store, *groups.Store, *sqlite.DB, func(time.Time)) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.Open(ctx, sqlite.Options{
		Path:              filepath.Join(t.TempDir(), "keys.db"),
		RelaxedDurability: true,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := sqlite.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	clock := testClock
	now := func() time.Time { return clock }
	gs := groups.NewStore(db, now)
	return NewStore(db, gs, now), gs, db, func(at time.Time) { clock = at }
}

const admin = "user:admin@example.com"

func mustCreate(t *testing.T, s *Store, req CreateRequest) (*Key, string) {
	t.Helper()
	k, secret, err := s.Create(context.Background(), admin, req)
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	return k, secret
}

func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// A key is a principal. It authenticates, it carries its own identity, and the
// identity is the key rather than a shared service name -- which is the reason
// the feature is worth having.
func TestVerify_AKeyIsAPrincipalThatNamesItself(t *testing.T) {
	s, _, _, _ := newStore(t)
	ctx := context.Background()
	key, secret := mustCreate(t, s, CreateRequest{
		Name: "ChatGPT connector", Role: auth.RoleUser, Plugins: []string{"echo"},
	})

	p, err := s.Verify(ctx, secret)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if p.ID != "key:"+key.ID {
		t.Errorf("principal id = %q, want %q", p.ID, "key:"+key.ID)
	}
	if p.TokenID != key.ID {
		t.Errorf("token id = %q, want the key's own identifier", p.TokenID)
	}
	if p.DisplayName != "ChatGPT connector" {
		t.Errorf("display name = %q", p.DisplayName)
	}
	if !p.Can(auth.CapRead) || p.Can(auth.CapAdmin) {
		t.Error("a key with the user role reads and does not administer")
	}
	if !p.CanAccessPlugin("echo") || p.CanAccessPlugin("netbox") {
		t.Errorf("reaches = %v; a key reaches exactly what it is granted", p.Plugins)
	}
}

// The grants on the principal are the union, resolved per request, so adding a
// key to a group takes effect on its next call.
func TestVerify_GrantsAreTheUnionAndFollowAGroup(t *testing.T) {
	s, gs, _, _ := newStore(t)
	ctx := context.Background()
	key, secret := mustCreate(t, s, CreateRequest{
		Name: "agent", Role: auth.RoleUser, Plugins: []string{"echo"},
	})

	p, err := s.Verify(ctx, secret)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !slices.Equal(p.Plugins, []string{"echo"}) {
		t.Fatalf("reaches = %v, want [echo]", p.Plugins)
	}

	g, err := gs.Create(ctx, admin, groups.CreateRequest{
		Name: "Field", Plugins: []string{"cnmaestro"},
	})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := gs.AddMember(ctx, admin, g.ID, groups.Key(key.ID)); err != nil {
		t.Fatalf("add: %v", err)
	}

	p, err = s.Verify(ctx, secret)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !slices.Equal(p.Plugins, []string{"cnmaestro", "echo"}) {
		t.Errorf("reaches = %v, want the union of its own grant and its group's", p.Plugins)
	}
}

// Default none. A key created with nothing and in no group reaches nothing.
func TestVerify_ANewKeyReachesNothing(t *testing.T) {
	s, _, _, _ := newStore(t)
	_, secret := mustCreate(t, s, CreateRequest{Name: "bare", Role: auth.RoleUser})
	p, err := s.Verify(context.Background(), secret)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(p.Plugins) != 0 {
		t.Errorf("reaches = %v; a new key reaches nothing", p.Plugins)
	}
	if p.CanAccessPlugin("echo") {
		t.Error("a key with no grants reached a plugin")
	}
}

// Revocation takes effect on the next request. Nothing about a key is cached
// between calls, so the row the next request reads is the revoked one.
func TestVerify_RevocationTakesEffectOnTheNextRequest(t *testing.T) {
	s, _, _, _ := newStore(t)
	ctx := context.Background()
	key, secret := mustCreate(t, s, CreateRequest{
		Name: "agent", Role: auth.RoleUser, Plugins: []string{"echo"},
	})
	if _, err := s.Verify(ctx, secret); err != nil {
		t.Fatalf("verify before revocation: %v", err)
	}

	if err := s.Revoke(ctx, admin, key.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := s.Verify(ctx, secret); !errors.Is(err, ErrRevoked) {
		t.Errorf("verify after revocation: %v, want ErrRevoked", err)
	}
	// Twice is one revocation, guarded in the statement rather than checked
	// before it.
	if err := s.Revoke(ctx, admin, key.ID); !errors.Is(err, ErrAlreadyRevoked) {
		t.Errorf("second revocation: %v, want ErrAlreadyRevoked", err)
	}
}

// Expiry and revocation are different facts and need different words. The
// distinction is for whoever is looking after the host; the caller is told
// only that the credential was not accepted, which the Verifier enforces.
func TestVerify_ExpiredAndRevokedAreDistinguishable(t *testing.T) {
	s, _, _, setClock := newStore(t)
	ctx := context.Background()

	expiry := testClock.Add(time.Hour)
	_, expiring := mustCreate(t, s, CreateRequest{
		Name: "expiring", Role: auth.RoleUser, ExpiresAt: &expiry,
	})
	revoked, revokedSecret := mustCreate(t, s, CreateRequest{
		Name: "revoked", Role: auth.RoleUser,
	})
	if err := s.Revoke(ctx, admin, revoked.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	setClock(testClock.Add(2 * time.Hour))
	if _, err := s.Verify(ctx, expiring); !errors.Is(err, ErrExpired) {
		t.Errorf("an expired key: %v, want ErrExpired", err)
	}
	if _, err := s.Verify(ctx, revokedSecret); !errors.Is(err, ErrRevoked) {
		t.Errorf("a revoked key: %v, want ErrRevoked", err)
	}

	// Through the Verifier every refusal is the same, because a caller that
	// can tell them apart can probe.
	v := NewVerifier(s, nil, quiet())
	for _, secret := range []string{expiring, revokedSecret, "mcpd_nonsense", "not-a-key"} {
		if _, err := v.Verify(ctx, secret, nil); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Errorf("verifier refusal for %q = %v, want ErrUnauthenticated", secret, err)
		}
	}
}

// The secret exists once. It is not on the key, not in a list, and not
// recoverable from the row -- what is stored is a digest.
func TestCreate_TheSecretIsNeverReadableAgain(t *testing.T) {
	s, _, db, _ := newStore(t)
	ctx := context.Background()
	key, secret := mustCreate(t, s, CreateRequest{Name: "agent", Role: auth.RoleUser})

	// Nothing the store hands back carries it.
	loaded, err := s.ByID(ctx, key.ID)
	if err != nil {
		t.Fatalf("by id: %v", err)
	}
	if encoded, err := json.Marshal(loaded); err != nil {
		t.Fatal(err)
	} else if bytes.Contains(encoded, []byte(secret)) {
		t.Error("a key serialised with its secret in it")
	}
	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if encoded, err := json.Marshal(list); err != nil {
		t.Fatal(err)
	} else if bytes.Contains(encoded, []byte(secret)) {
		t.Error("the key list carried a secret")
	}

	// And the row holds a digest rather than the credential.
	var stored string
	if err := db.Reader().QueryRowContext(ctx,
		`SELECT secret_hash FROM api_keys WHERE id = ?`, key.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == secret || strings.Contains(stored, secret) {
		t.Error("the secret is recoverable from the row")
	}
	if len(stored) != 64 {
		t.Errorf("secret_hash = %q; want a SHA-256 in hex", stored)
	}
}

// A last-used stamp is what makes a forgotten key findable. It is written at
// most once a minute, so it costs nothing on a busy endpoint.
func TestVerify_RecordsWhenAKeyWasLastUsed(t *testing.T) {
	s, _, _, setClock := newStore(t)
	ctx := context.Background()
	key, secret := mustCreate(t, s, CreateRequest{Name: "agent", Role: auth.RoleUser})

	if loaded, err := s.ByID(ctx, key.ID); err != nil {
		t.Fatal(err)
	} else if loaded.LastUsedAt != nil {
		t.Error("a key that has never been used carries a last-used stamp")
	}

	if _, err := s.Verify(ctx, secret); err != nil {
		t.Fatalf("verify: %v", err)
	}
	loaded, err := s.ByID(ctx, key.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.LastUsedAt == nil || !loaded.LastUsedAt.Equal(testClock) {
		t.Fatalf("last used = %v, want %v", loaded.LastUsedAt, testClock)
	}

	// A second call within the resolution does not write again.
	setClock(testClock.Add(time.Second))
	if _, err := s.Verify(ctx, secret); err != nil {
		t.Fatalf("verify: %v", err)
	}
	loaded, err = s.ByID(ctx, key.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.LastUsedAt.Equal(testClock) {
		t.Errorf("last used = %v; a second call within the minute rewrote it", loaded.LastUsedAt)
	}

	setClock(testClock.Add(2 * time.Minute))
	if _, err := s.Verify(ctx, secret); err != nil {
		t.Fatalf("verify: %v", err)
	}
	loaded, err = s.ByID(ctx, key.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.LastUsedAt.Equal(testClock) {
		t.Error("a call after the resolution did not move the stamp")
	}
}

// Static tokens are how the live instance and its connectors authenticate.
// They keep working, unchanged, and they reach exactly what they did -- the
// database is not consulted for one and no group can widen or narrow it.
func TestVerifier_StaticTokensStillAuthenticateAndReachTheSame(t *testing.T) {
	s, gs, _, _ := newStore(t)
	ctx := context.Background()

	const secret = "a-static-token-of-quite-sufficient-length"
	st, err := auth.NewStaticToken("chatgpt", secret, auth.Principal{
		ID:      "service:chatgpt",
		Role:    auth.RoleUser,
		Plugins: []string{"echo"},
	})
	if err != nil {
		t.Fatalf("static token: %v", err)
	}
	static, err := auth.NewStaticVerifier(st)
	if err != nil {
		t.Fatalf("static verifier: %v", err)
	}
	v := NewVerifier(s, static, quiet())

	before, err := v.Verify(ctx, secret, nil)
	if err != nil {
		t.Fatalf("static token refused: %v", err)
	}
	if before.ID != "service:chatgpt" || before.TokenID != "chatgpt" {
		t.Errorf("principal = %+v; a file token keeps its declared identity", before)
	}
	if !slices.Equal(before.Plugins, []string{"echo"}) {
		t.Errorf("reaches = %v, want [echo]", before.Plugins)
	}

	// A group that grants everything, with the token's own identifiers in it
	// as far as anything could contrive. A file token has no row, so nothing
	// here can touch it.
	g, err := gs.Create(ctx, admin, groups.CreateRequest{
		Name: "Everything", Plugins: []string{auth.Wildcard},
	})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := gs.AddMember(ctx, admin, g.ID, groups.Key("chatgpt")); !errors.Is(err, groups.ErrNoSuchMember) {
		t.Errorf("a file token was put in a group: %v", err)
	}

	after, err := v.Verify(ctx, secret, nil)
	if err != nil {
		t.Fatalf("static token refused after groups existed: %v", err)
	}
	if !after.Equal(*before) {
		t.Errorf("a static token's principal changed: %+v then %+v", before, after)
	}

	// And the scheme still says what it is, with the new mechanism named
	// beside the old one rather than replacing it.
	if v.Scheme() != "static+key" {
		t.Errorf("scheme = %q, want static+key", v.Scheme())
	}
}

// A key and a static token both work at once, and each keeps its own identity
// in the trail.
func TestVerifier_AKeyAndAStaticTokenCoexist(t *testing.T) {
	s, _, _, _ := newStore(t)
	ctx := context.Background()

	const fileSecret = "a-static-token-of-quite-sufficient-length"
	st, err := auth.NewStaticToken("chatgpt", fileSecret, auth.Principal{
		ID: "service:chatgpt", Role: auth.RoleUser, Plugins: []string{"echo"},
	})
	if err != nil {
		t.Fatal(err)
	}
	static, err := auth.NewStaticVerifier(st)
	if err != nil {
		t.Fatal(err)
	}
	v := NewVerifier(s, static, quiet())

	key, keySecret := mustCreate(t, s, CreateRequest{
		Name: "agent", Role: auth.RoleUser, Plugins: []string{"netbox"},
	})

	fromFile, err := v.Verify(ctx, fileSecret, nil)
	if err != nil {
		t.Fatalf("file token: %v", err)
	}
	fromDB, err := v.Verify(ctx, keySecret, nil)
	if err != nil {
		t.Fatalf("database key: %v", err)
	}
	if fromFile.TokenID == fromDB.TokenID {
		t.Error("two credentials share a token id; the trail cannot tell them apart")
	}
	if fromDB.TokenID != key.ID || !strings.HasPrefix(fromDB.TokenID, IDPrefix) {
		t.Errorf("key token id = %q, want a generated %s identifier", fromDB.TokenID, IDPrefix)
	}
}

// Creating, revoking and re-scoping are privilege changes, written into the
// hash-chained trail in the transaction that performed them, naming the
// administrator who acted -- and the chain still verifies.
func TestKeyOperationsAreAuditedAndTheChainVerifies(t *testing.T) {
	s, _, db, _ := newStore(t)
	ctx := context.Background()

	key, _ := mustCreate(t, s, CreateRequest{
		Name: "agent", Role: auth.RoleUser, Plugins: []string{"echo"},
	})
	if _, err := s.Update(ctx, admin, key.ID, UpdateRequest{
		Plugins: &[]string{"echo", "netbox"},
	}); err != nil {
		t.Fatalf("rescope: %v", err)
	}
	if err := s.Revoke(ctx, admin, key.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	audit := sqlite.NewAuditStore(db)
	records, err := audit.Recent(ctx, 50)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	want := map[string]bool{
		"apikey.created":  false,
		"apikey.rescoped": false,
		"apikey.revoked":  false,
	}
	for _, r := range records {
		if _, ok := want[r.Entry.Kind]; !ok {
			continue
		}
		want[r.Entry.Kind] = true
		if r.Entry.Actor != admin {
			t.Errorf("%s was recorded against %q, want %q", r.Entry.Kind, r.Entry.Actor, admin)
		}
		// The subject is the key's identifier, so "which key" is answerable
		// from the entry alone.
		if r.Entry.Plugin != key.ID {
			t.Errorf("%s names %q, want the key's identifier %q",
				r.Entry.Kind, r.Entry.Plugin, key.ID)
		}
		if bytes.Contains(r.Entry.Detail, []byte("secret")) {
			t.Errorf("%s detail mentions a secret: %s", r.Entry.Kind, r.Entry.Detail)
		}
	}
	for kind, seen := range want {
		if !seen {
			t.Errorf("%s is not in the trail", kind)
		}
	}
	if _, err := audit.VerifyChain(ctx); err != nil {
		t.Errorf("the audit chain no longer verifies: %v", err)
	}
}

// A re-scope says what the grant was as well as what it became. An entry
// naming only the new value leaves "what did this widen" unanswerable.
func TestUpdate_RecordsWhatTheGrantWas(t *testing.T) {
	s, _, db, _ := newStore(t)
	ctx := context.Background()
	key, _ := mustCreate(t, s, CreateRequest{
		Name: "agent", Role: auth.RoleUser, Plugins: []string{"echo"},
	})
	if _, err := s.Update(ctx, admin, key.ID, UpdateRequest{
		Plugins: &[]string{"netbox"},
	}); err != nil {
		t.Fatalf("rescope: %v", err)
	}
	records, err := sqlite.NewAuditStore(db).Recent(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range records {
		if r.Entry.Kind != "apikey.rescoped" {
			continue
		}
		detail := string(r.Entry.Detail)
		if !strings.Contains(detail, "plugins_before") || !strings.Contains(detail, "echo") {
			t.Errorf("detail = %s; it must carry the grant it replaced", detail)
		}
		return
	}
	t.Fatal("apikey.rescoped is not in the trail")
}

// A revoked key does not come back by being edited. Re-scoping one would
// record a grant nobody can use and suggest the key is returning.
func TestUpdate_RefusesARevokedKey(t *testing.T) {
	s, _, _, _ := newStore(t)
	ctx := context.Background()
	key, _ := mustCreate(t, s, CreateRequest{Name: "agent", Role: auth.RoleUser})
	if err := s.Revoke(ctx, admin, key.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := s.Update(ctx, admin, key.ID, UpdateRequest{
		Plugins: &[]string{"echo"},
	}); !errors.Is(err, ErrRevoked) {
		t.Errorf("editing a revoked key: %v, want ErrRevoked", err)
	}
}

func TestCreate_Refusals(t *testing.T) {
	s, _, _, _ := newStore(t)
	ctx := context.Background()
	past := testClock.Add(-time.Hour)

	for _, tc := range []struct {
		name string
		req  CreateRequest
	}{
		{"no name", CreateRequest{Role: auth.RoleUser}},
		{"unknown role", CreateRequest{Name: "k", Role: "superuser"}},
		{"expiry in the past", CreateRequest{Name: "k", Role: auth.RoleUser, ExpiresAt: &past}},
		{"newline in the name", CreateRequest{Name: "a\nb", Role: auth.RoleUser}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := s.Create(ctx, admin, tc.req); err == nil {
				t.Error("accepted")
			}
		})
	}
}

// A secret carries the prefix, so a leaked one is recognisable, and enough
// entropy that a digest lookup is the right comparison.
func TestGenerateSecret(t *testing.T) {
	first, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	second, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("two secrets came back the same")
	}
	if !strings.HasPrefix(first, SecretPrefix) {
		t.Errorf("secret = %q, want the %q prefix", first, SecretPrefix)
	}
	// 32 bytes, base64url without padding, is 43 characters.
	if got := len(first) - len(SecretPrefix); got != 43 {
		t.Errorf("secret body is %d characters, want 43", got)
	}
}

// detailOf returns the detail of the most recent entry of a kind, and fails
// if the chain no longer verifies. Recent is newest first, which is what lets
// a test make two changes and assert on the second.
func detailOf(t *testing.T, db *sqlite.DB, kind string) string {
	t.Helper()
	ctx := context.Background()
	store := sqlite.NewAuditStore(db)
	records, err := store.Recent(ctx, 100)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if _, err := store.VerifyChain(ctx); err != nil {
		t.Fatalf("the audit chain no longer verifies: %v", err)
	}
	for _, r := range records {
		if r.Entry.Kind == kind {
			return string(r.Entry.Detail)
		}
	}
	t.Fatalf("%s is not in the trail", kind)
	return ""
}

// Extending a key from next month to next year is a grant of a year's more
// reach. An entry naming only the new date says when it ends and not how much
// was added, which is the same failure an entry naming only a new plugin list
// would be.
func TestUpdate_RecordsThePriorExpiry(t *testing.T) {
	s, _, db, _ := newStore(t)
	ctx := context.Background()

	first := testClock.Add(24 * time.Hour)
	key, _ := mustCreate(t, s, CreateRequest{
		Name: "agent", Role: auth.RoleUser, ExpiresAt: &first,
	})

	extended := testClock.Add(365 * 24 * time.Hour)
	if _, err := s.Update(ctx, admin, key.ID, UpdateRequest{
		ExpiresAt: ptr(&extended),
	}); err != nil {
		t.Fatalf("rescope: %v", err)
	}

	detail := detailOf(t, db, "apikey.rescoped")
	if !strings.Contains(detail, "expires_at_before") {
		t.Fatalf("detail = %s; it must carry the expiry it replaced", detail)
	}
	if !strings.Contains(detail, first.UTC().Format(time.RFC3339)) {
		t.Errorf("detail = %s; it must name the previous date %s",
			detail, first.UTC().Format(time.RFC3339))
	}
	if !strings.Contains(detail, extended.UTC().Format(time.RFC3339)) {
		t.Errorf("detail = %s; it must name the new date", detail)
	}
}

// Setting an expiry on a key that had none, and clearing one, are both changes
// to how long a credential lives. Both have to say what they changed from --
// "no expiry" included, which reads as null rather than as an absent field.
func TestUpdate_RecordsAnExpiryArrivingAndLeaving(t *testing.T) {
	s, _, db, _ := newStore(t)
	ctx := context.Background()
	key, _ := mustCreate(t, s, CreateRequest{Name: "agent", Role: auth.RoleUser})

	at := testClock.Add(24 * time.Hour)
	if _, err := s.Update(ctx, admin, key.ID, UpdateRequest{ExpiresAt: ptr(&at)}); err != nil {
		t.Fatalf("set an expiry: %v", err)
	}
	detail := detailOf(t, db, "apikey.rescoped")
	if !strings.Contains(detail, `"expires_at_before":null`) {
		t.Errorf("detail = %s; a key that never expired must record that", detail)
	}

	var none *time.Time
	if _, err := s.Update(ctx, admin, key.ID, UpdateRequest{ExpiresAt: ptr(none)}); err != nil {
		t.Fatalf("clear the expiry: %v", err)
	}
	// The most recent entry now, which is the clearing one.
	detail = detailOf(t, db, "apikey.rescoped")
	if !strings.Contains(detail, at.UTC().Format(time.RFC3339)) {
		t.Errorf("detail = %s; clearing an expiry must name the one it removed", detail)
	}
	if !strings.Contains(detail, `"expires_at":null`) {
		t.Errorf("detail = %s; the key now never expires and the entry must say so", detail)
	}
}

// A key issued into a group is a membership like any other, and reads the same
// in the trail as one an administrator added on the Groups page.
func TestCreate_AuditsTheGroupsAKeyIsIssuedInto(t *testing.T) {
	s, gs, db, _ := newStore(t)
	ctx := context.Background()

	g, err := gs.Create(ctx, admin, groups.CreateRequest{
		Name: "Field", Plugins: []string{"cnmaestro"},
	})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	key, _ := mustCreate(t, s, CreateRequest{
		Name: "agent", Role: auth.RoleUser, Groups: []string{g.ID},
	})

	detail := detailOf(t, db, "group.member_added")
	if !strings.Contains(detail, key.ID) {
		t.Errorf("detail = %s; it must name the key (%s)", detail, key.ID)
	}
	if !strings.Contains(detail, `"kind":"key"`) {
		t.Errorf("detail = %s; it must say the member is a key", detail)
	}
}

func ptr[T any](v T) *T { return &v }
