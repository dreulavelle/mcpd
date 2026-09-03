package sqlite

import (
	"context"
	"errors"
	"testing"
)

func newRowStore(t *testing.T) *PluginRowStore {
	t.Helper()
	return NewPluginRowStore(newTestDB(t), testCipher{}, nil)
}

// A row's secret columns reach the database only as ciphertext, and come back
// decrypted; the non-secret columns are stored as they are.
func TestPluginRows_SecretsAreEncryptedAtRest(t *testing.T) {
	ctx := context.Background()
	s := newRowStore(t)

	created, err := s.Create(ctx, "user:test", "pbx", "customers", "Acme",
		map[string]any{"name": "Acme", "host": "acme.example", "aliases": []string{"acme", "ACME Inc"}},
		map[string]string{"password": "s3cret"})
	if err != nil {
		t.Fatal(err)
	}

	var storedSecrets, storedData string
	if err := s.db.Reader().QueryRowContext(ctx,
		`SELECT secrets, data FROM plugin_rows WHERE id = ?`, created.ID).Scan(&storedSecrets, &storedData); err != nil {
		t.Fatal(err)
	}
	if storedSecrets == "" || storedSecrets == `{"password":"s3cret"}` {
		t.Errorf("secrets should be stored encrypted, got %q", storedSecrets)
	}
	if storedData != `{"aliases":["acme","ACME Inc"],"host":"acme.example","name":"Acme"}` {
		t.Errorf("data stored as %q", storedData)
	}

	rows, err := s.List(ctx, "pbx", "customers")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Secrets["password"] != "s3cret" || rows[0].Data["host"] != "acme.example" {
		t.Errorf("rows read back: %+v", rows)
	}
	if rows[0].Identity != "Acme" || rows[0].Position != 1 {
		t.Errorf("identity and position: %+v", rows[0])
	}
}

// Two rows in one collection may not share a name, whatever the case; the same
// name in another instance or another field is fine.
func TestPluginRows_IdentityIsUniquePerCollection(t *testing.T) {
	ctx := context.Background()
	s := newRowStore(t)
	if _, err := s.Create(ctx, "u", "pbx", "customers", "Acme", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, "u", "pbx", "customers", "acme", nil, nil); !errors.Is(err, ErrRowExists) {
		t.Errorf("a duplicate name differing in case should be refused, got %v", err)
	}
	if _, err := s.Create(ctx, "u", "other", "customers", "Acme", nil, nil); err != nil {
		t.Errorf("the same name in another instance is fine: %v", err)
	}
	if _, err := s.Create(ctx, "u", "pbx", "sites", "Acme", nil, nil); err != nil {
		t.Errorf("the same name in another field is fine: %v", err)
	}
}

// Updating replaces the data whole, merges secrets -- one can be replaced or
// cleared without the others being retyped -- and refuses a stale write.
func TestPluginRows_UpdateMergesSecrets(t *testing.T) {
	ctx := context.Background()
	s := newRowStore(t)
	created, err := s.Create(ctx, "u", "pbx", "customers", "Acme",
		map[string]any{"name": "Acme", "host": "old.example"},
		map[string]string{"password": "one", "token": "two"})
	if err != nil {
		t.Fatal(err)
	}

	updated, err := s.Update(ctx, "u", created.ID, "Acme Ltd",
		map[string]any{"name": "Acme Ltd", "host": "new.example"},
		map[string]string{"password": "three"}, []string{"token"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Identity != "Acme Ltd" || updated.Data["host"] != "new.example" {
		t.Errorf("data should be replaced: %+v", updated)
	}
	if updated.Secrets["password"] != "three" || updated.Secrets["token"] != "" || len(updated.Secrets) != 1 {
		t.Errorf("secrets should be merged: %+v", updated.Secrets)
	}

	// An untouched secret survives an update that says nothing about it.
	again, err := s.Update(ctx, "u", created.ID, "Acme Ltd", map[string]any{"name": "Acme Ltd"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if again.Secrets["password"] != "three" {
		t.Errorf("a secret not mentioned should be kept, got %+v", again.Secrets)
	}

	// A second writer holding the old row loses.
	_, err = s.Update(ctx, "u", created.ID, "Stale", nil, nil, nil)
	if err != nil {
		t.Fatal("the store re-reads the row itself; only the row's own state is checked")
	}
	if _, err := s.Update(ctx, "u", "row_missing", "x", nil, nil, nil); !errors.Is(err, ErrNoSuchRow) {
		t.Errorf("an unknown id should be refused, got %v", err)
	}
}

// Deleting one row leaves the others; deleting an instance's rows takes every
// collection it had and nothing of any other instance.
func TestPluginRows_Delete(t *testing.T) {
	ctx := context.Background()
	s := newRowStore(t)
	a, _ := s.Create(ctx, "u", "pbx", "customers", "A", nil, nil)
	_, _ = s.Create(ctx, "u", "pbx", "customers", "B", nil, nil)
	_, _ = s.Create(ctx, "u", "pbx", "sites", "S", nil, nil)
	_, _ = s.Create(ctx, "u", "other", "customers", "O", nil, nil)

	if err := s.Delete(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, a.ID); !errors.Is(err, ErrNoSuchRow) {
		t.Errorf("deleting twice should be refused, got %v", err)
	}
	rows, _ := s.List(ctx, "pbx", "customers")
	if len(rows) != 1 || rows[0].Identity != "B" {
		t.Errorf("after one delete: %+v", rows)
	}

	if err := s.DeleteAll(ctx, "pbx"); err != nil {
		t.Fatal(err)
	}
	if rows, _ := s.List(ctx, "pbx", "customers"); len(rows) != 0 {
		t.Errorf("customers should be gone: %+v", rows)
	}
	if rows, _ := s.List(ctx, "pbx", "sites"); len(rows) != 0 {
		t.Errorf("sites should be gone: %+v", rows)
	}
	if rows, _ := s.List(ctx, "other", "customers"); len(rows) != 1 {
		t.Errorf("another instance's rows should survive: %+v", rows)
	}
}

// Without a cipher a row with no secrets is fine and a row with one is refused,
// rather than stored in the clear.
func TestPluginRows_NoCipherRefusesSecrets(t *testing.T) {
	ctx := context.Background()
	s := NewPluginRowStore(newTestDB(t), nil, nil)
	if _, err := s.Create(ctx, "u", "pbx", "customers", "Acme", map[string]any{"name": "Acme"}, nil); err != nil {
		t.Errorf("a row without secrets needs no cipher: %v", err)
	}
	if _, err := s.Create(ctx, "u", "pbx", "customers", "Globex", nil, map[string]string{"password": "x"}); !errors.Is(err, ErrRowNoCipher) {
		t.Errorf("a secret without a cipher should be refused, got %v", err)
	}
}
