package settings

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/storage/sqlite"
)

var clock = time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

func newTestStore(t *testing.T, withCipher bool) *Store {
	t.Helper()
	ctx := context.Background()

	db, err := sqlite.Open(ctx, sqlite.Options{
		Path:              filepath.Join(t.TempDir(), "settings.db"),
		RelaxedDurability: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := sqlite.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	var c *Cipher
	if withCipher {
		key, err := GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		if c, err = NewCipher(key); err != nil {
			t.Fatal(err)
		}
	}
	return NewStore(db, c, func() time.Time { return clock })
}

func TestStore_RoundTrip(t *testing.T) {
	s := newTestStore(t, true)
	ctx := context.Background()

	if err := s.SetJSON(ctx, "user:alice", KeyTunnelPrincipal, "svc:chatgpt"); err != nil {
		t.Fatal(err)
	}
	var got string
	found, err := s.GetJSON(ctx, KeyTunnelPrincipal, &got)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if got != "svc:chatgpt" {
		t.Fatalf("value = %q", got)
	}
}

// A secret must be unreadable in the database and readable through the store.
// That is the whole basis for letting an operator type a key into a form.
func TestStore_SecretsAreEncryptedAtRest(t *testing.T) {
	s := newTestStore(t, true)
	ctx := context.Background()
	const secret = "sk-a-real-looking-runtime-key"

	if err := s.SetSecret(ctx, "user:alice", KeyTunnelAPIKey, secret); err != nil {
		t.Fatal(err)
	}

	// Readable through the store.
	got, ok, err := s.Get(ctx, KeyTunnelAPIKey)
	if err != nil || !ok || got != secret {
		t.Fatalf("Get = (%q,%v,%v), want the plaintext back", got, ok, err)
	}

	// Not readable in the row.
	var stored string
	var encrypted int
	if err := s.db.Reader().QueryRow(
		`SELECT value, encrypted FROM settings WHERE key = ?`, KeyTunnelAPIKey).
		Scan(&stored, &encrypted); err != nil {
		t.Fatal(err)
	}
	if encrypted != 1 {
		t.Fatal("the row should be marked encrypted")
	}
	if strings.Contains(stored, secret) {
		t.Fatalf("the secret is stored in the clear: %q", stored)
	}
}

// Without a key, a secret must be refused rather than stored in the clear.
func TestStore_RefusesSecretsWithoutAKey(t *testing.T) {
	s := newTestStore(t, false)
	err := s.SetSecret(context.Background(), "user:alice", KeyTunnelAPIKey, "sk-x")
	if err == nil {
		t.Fatal("storing a secret without an encryption key must fail")
	}
	if !strings.Contains(err.Error(), "encryption key") {
		t.Fatalf("the error should explain why: %v", err)
	}
}

// History is what makes a configuration change auditable, but it must never
// become the plaintext copy the encryption exists to prevent.
func TestStore_HistoryNeverRecordsSecrets(t *testing.T) {
	s := newTestStore(t, true)
	ctx := context.Background()
	const secret = "sk-never-write-this-down"

	if err := s.SetSecret(ctx, "user:alice", KeyTunnelAPIKey, secret); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSecret(ctx, "user:alice", KeyTunnelAPIKey, secret+"-rotated"); err != nil {
		t.Fatal(err)
	}

	entries, err := s.History(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("history entries = %d, want 2", len(entries))
	}
	for _, e := range entries {
		if !e.Redacted {
			t.Error("a secret change must be marked redacted")
		}
		if strings.Contains(e.OldValue+e.NewValue, "sk-") {
			t.Fatalf("history recorded a secret: %+v", e)
		}
		if e.ChangedBy != "user:alice" {
			t.Errorf("changed_by = %q; configuration changes must be attributed", e.ChangedBy)
		}
	}
}

// An ordinary value should be fully recorded, because that is what makes the
// history useful.
func TestStore_HistoryRecordsOrdinaryValues(t *testing.T) {
	s := newTestStore(t, true)
	ctx := context.Background()

	_ = s.SetJSON(ctx, "user:alice", KeyApprovalProposalTTL, 30)
	_ = s.SetJSON(ctx, "user:bob", KeyApprovalProposalTTL, 60)

	entries, err := s.History(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	newest := entries[0]
	if newest.Redacted {
		t.Fatal("an ordinary value should not be redacted")
	}
	if newest.OldValue != "30" || newest.NewValue != "60" {
		t.Fatalf("history should show the change: %q -> %q", newest.OldValue, newest.NewValue)
	}
	if newest.ChangedBy != "user:bob" {
		t.Fatalf("changed_by = %q", newest.ChangedBy)
	}
}

// Applying half a configuration would leave the host in a state nobody asked
// for and nothing validated.
func TestStore_ApplyIsAtomic(t *testing.T) {
	s := newTestStore(t, false) // no cipher, so a secret in the batch fails
	ctx := context.Background()

	err := s.Apply(ctx, "user:alice", []Change{
		{Key: KeyTunnelPrincipal, Value: `"svc:chatgpt"`},
		{Key: KeyTunnelAPIKey, Value: "sk-x", Secret: true},
	})
	if err == nil {
		t.Fatal("the batch should have been refused")
	}
	if _, ok, _ := s.Get(ctx, KeyTunnelPrincipal); ok {
		t.Fatal("no part of a refused batch may be applied")
	}
}

func TestStore_Delete(t *testing.T) {
	s := newTestStore(t, true)
	ctx := context.Background()

	_ = s.SetJSON(ctx, "user:alice", KeyTunnelPrincipal, "svc:x")
	if err := s.Apply(ctx, "user:alice", []Change{{Key: KeyTunnelPrincipal, Delete: true}}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Get(ctx, KeyTunnelPrincipal); ok {
		t.Fatal("the setting should be gone")
	}
}

func TestStore_WatchersSeeChanges(t *testing.T) {
	s := newTestStore(t, true)
	ctx := context.Background()

	var seen []string
	s.Watch(func(changed []string) { seen = append(seen, changed...) })

	_ = s.SetJSON(ctx, "user:alice", KeyApprovalProposalTTL, 45)
	if len(seen) != 1 || seen[0] != KeyApprovalProposalTTL {
		t.Fatalf("watcher saw %v", seen)
	}
}

// --- crypto ---------------------------------------------------------------

func TestCipher_RoundTrip(t *testing.T) {
	key, _ := GenerateKey()
	c, err := NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}

	const plaintext = "sk-secret"
	sealed, err := c.Encrypt(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, plaintext) {
		t.Fatal("the ciphertext contains the plaintext")
	}
	got, err := c.Decrypt(sealed)
	if err != nil || got != plaintext {
		t.Fatalf("Decrypt = (%q,%v)", got, err)
	}
}

// A random nonce per value means the same secret encrypts differently each
// time, so an observer cannot tell that two settings hold the same key.
func TestCipher_NonceIsPerValue(t *testing.T) {
	key, _ := GenerateKey()
	c, _ := NewCipher(key)

	a, _ := c.Encrypt("same")
	b, _ := c.Encrypt("same")
	if a == b {
		t.Fatal("encrypting the same value twice produced identical ciphertext")
	}
}

// GCM makes a tampered value fail rather than decrypt to something else.
func TestCipher_DetectsTampering(t *testing.T) {
	key, _ := GenerateKey()
	c, _ := NewCipher(key)

	sealed, _ := c.Encrypt("sk-secret")
	// Flip a character in the middle of the ciphertext.
	mangled := []byte(sealed)
	mangled[len(mangled)/2] ^= 0x01
	if _, err := c.Decrypt(string(mangled)); err == nil {
		t.Fatal("a tampered value must not decrypt")
	}
}

// A wrong key must produce a message an operator can act on, not a corruption
// scare.
func TestCipher_WrongKeyExplainsItself(t *testing.T) {
	keyA, _ := GenerateKey()
	keyB, _ := GenerateKey()
	a, _ := NewCipher(keyA)
	b, _ := NewCipher(keyB)

	sealed, _ := a.Encrypt("sk-secret")
	_, err := b.Decrypt(sealed)
	if err == nil {
		t.Fatal("a different key must not decrypt")
	}
	if !strings.Contains(err.Error(), "encryption key has changed") {
		t.Fatalf("the error should point at the key: %v", err)
	}
}

func TestNewCipher_RejectsWeakKeys(t *testing.T) {
	for _, key := range []string{"", "   ", "too-short"} {
		if _, err := NewCipher(key); err == nil {
			t.Errorf("NewCipher(%q) should have failed", key)
		}
	}
}

// --- schema ---------------------------------------------------------------

func TestValidate(t *testing.T) {
	tests := []struct {
		key, value string
		valid      bool
	}{
		{KeyTunnelEnabled, "true", true},
		{KeyTunnelEnabled, "yes please", false},
		{KeyTunnelID, "tunnel_0123456789abcdef0123456789abcdef", true},
		{KeyTunnelID, "tunnel_short", false},
		{KeyTunnelID, "0123456789abcdef0123456789abcdef", false},
		{KeyTunnelRole, "operator", true},
		{KeyTunnelRole, "superuser", false},
		{KeyApprovalProposalTTL, "30", true},
		{KeyApprovalProposalTTL, "0", false},
		{KeyApprovalProposalTTL, "99999", false},
		{KeyApprovalProposalTTL, "thirty", false},
		{"not.a.setting", "x", false},
	}
	for _, tc := range tests {
		err := Validate(tc.key, tc.value)
		if tc.valid && err != nil {
			t.Errorf("Validate(%s, %q) should pass: %v", tc.key, tc.value, err)
		}
		if !tc.valid && err == nil {
			t.Errorf("Validate(%s, %q) should fail", tc.key, tc.value)
		}
	}
}

// Every field must be discoverable and every secret correctly flagged, since
// the flag is what decides whether a value is encrypted.
func TestSchema_IsCoherent(t *testing.T) {
	seen := map[string]bool{}
	for _, g := range Schema() {
		if g.Title == "" {
			t.Errorf("group %s has no title", g.Name)
		}
		for _, f := range g.Fields {
			if seen[f.Key] {
				t.Errorf("duplicate setting key %s", f.Key)
			}
			seen[f.Key] = true

			if f.Label == "" {
				t.Errorf("%s has no label", f.Key)
			}
			if f.Group != g.Name {
				t.Errorf("%s claims group %q but is in %q", f.Key, f.Group, g.Name)
			}
			if f.Kind == KindEnum && len(f.Options) == 0 {
				t.Errorf("%s is an enum with no options", f.Key)
			}
			if _, ok := FieldFor(f.Key); !ok {
				t.Errorf("%s is not discoverable through FieldFor", f.Key)
			}
		}
	}
	if !IsSecret(KeyTunnelAPIKey) {
		t.Error("the tunnel API key must be flagged secret, or it is stored in the clear")
	}
	if IsSecret(KeyTunnelID) {
		t.Error("the tunnel id is not a secret and hiding it helps nobody")
	}
}
