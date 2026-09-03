package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// PluginRow is one row of a plugin's collection setting.
//
// Data holds the non-secret columns and Secrets the secret ones, decrypted;
// the two are kept apart so a caller building a redacted view for the
// dashboard cannot forget which is which.
type PluginRow struct {
	ID       string
	Instance string
	Field    string
	// Identity is the value of the collection's first column: what the row is
	// called, and what no two rows in one collection may share.
	Identity  string
	Position  int
	Data      map[string]any
	Secrets   map[string]string
	CreatedBy string
	CreatedAt time.Time
	UpdatedBy string
	UpdatedAt time.Time
}

// PluginRowStore holds the rows of every plugin collection.
type PluginRowStore struct {
	db     *DB
	cipher Cipher
	now    func() time.Time
}

// NewPluginRowStore returns a store backed by db.
//
// A nil cipher leaves the store usable for rows with no secret columns, and
// refuses to write or read a secret rather than storing one in the clear.
func NewPluginRowStore(db *DB, cipher Cipher, now func() time.Time) *PluginRowStore {
	if now == nil {
		now = time.Now
	}
	return &PluginRowStore{db: db, cipher: cipher, now: now}
}

// ErrNoSuchRow reports an operation against a row that is not stored.
var ErrNoSuchRow = errors.New("sqlite: no such row")

// ErrRowExists reports an identity already taken within the collection.
var ErrRowExists = errors.New("sqlite: a row with that name already exists")

// ErrRowNoCipher reports that a secret column cannot be handled without a key.
var ErrRowNoCipher = errors.New(
	"sqlite: no encryption key is configured, so a credential cannot be stored or read")

// List returns a collection's rows in display order, secrets decrypted.
func (s *PluginRowStore) List(ctx context.Context, instance, field string) ([]PluginRow, error) {
	rows, err := s.db.Reader().QueryContext(ctx, `
		SELECT id, instance, field, identity, position, data, secrets,
		       created_by, created_at, updated_by, updated_at
		  FROM plugin_rows
		 WHERE instance = ? AND field = ?
		 ORDER BY position, created_at`, instance, field)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list plugin rows: %w", err)
	}
	defer rows.Close()

	var out []PluginRow
	for rows.Next() {
		r, err := s.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Get returns one row by id.
func (s *PluginRowStore) Get(ctx context.Context, id string) (PluginRow, bool, error) {
	rows, err := s.db.Reader().QueryContext(ctx, `
		SELECT id, instance, field, identity, position, data, secrets,
		       created_by, created_at, updated_by, updated_at
		  FROM plugin_rows WHERE id = ?`, id)
	if err != nil {
		return PluginRow{}, false, fmt.Errorf("sqlite: read plugin row: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return PluginRow{}, false, rows.Err()
	}
	r, err := s.scan(rows)
	return r, err == nil, err
}

func (s *PluginRowStore) scan(row scanner) (PluginRow, error) {
	var r PluginRow
	var data, secrets string
	var createdAt, updatedAt int64
	if err := row.Scan(&r.ID, &r.Instance, &r.Field, &r.Identity, &r.Position, &data, &secrets,
		&r.CreatedBy, &createdAt, &r.UpdatedBy, &updatedAt); err != nil {
		return PluginRow{}, fmt.Errorf("sqlite: scan plugin row: %w", err)
	}
	r.CreatedAt = time.UnixMilli(createdAt).UTC()
	r.UpdatedAt = time.UnixMilli(updatedAt).UTC()
	if err := json.Unmarshal([]byte(data), &r.Data); err != nil {
		return PluginRow{}, fmt.Errorf("sqlite: plugin row %s holds unreadable data: %w", r.ID, err)
	}
	if r.Data == nil {
		r.Data = map[string]any{}
	}
	r.Secrets = map[string]string{}
	if secrets != "" {
		if s.cipher == nil {
			return PluginRow{}, ErrRowNoCipher
		}
		plain, err := s.cipher.Decrypt(secrets)
		if err != nil {
			return PluginRow{}, fmt.Errorf("sqlite: plugin row %s: decrypt: %w", r.ID, err)
		}
		if err := json.Unmarshal([]byte(plain), &r.Secrets); err != nil {
			return PluginRow{}, fmt.Errorf("sqlite: plugin row %s holds unreadable secrets: %w", r.ID, err)
		}
	}
	return r, nil
}

// Create stores a new row at the end of the collection.
//
// The insert is guarded by the unique index rather than by a prior read, so
// two administrators racing the same name produce one row and one refusal.
func (s *PluginRowStore) Create(ctx context.Context, actor, instance, field, identity string,
	data map[string]any, secrets map[string]string) (PluginRow, error) {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return PluginRow{}, fmt.Errorf("sqlite: a row needs a name")
	}
	encodedData, encodedSecrets, err := s.encode(data, secrets)
	if err != nil {
		return PluginRow{}, err
	}
	now := s.now()
	r := PluginRow{
		ID: newRowID(), Instance: instance, Field: field, Identity: identity,
		Data: data, Secrets: secrets,
		CreatedBy: actor, CreatedAt: now, UpdatedBy: actor, UpdatedAt: now,
	}
	if r.Data == nil {
		r.Data = map[string]any{}
	}
	if r.Secrets == nil {
		r.Secrets = map[string]string{}
	}
	err = s.db.WriteTx(ctx, now.UnixMilli(), func(u *UnitOfWork) error {
		if err := u.QueryRow(`SELECT COALESCE(MAX(position), 0) + 1 FROM plugin_rows WHERE instance = ? AND field = ?`,
			instance, field).Scan(&r.Position); err != nil {
			return fmt.Errorf("sqlite: next row position: %w", err)
		}
		_, err := u.exec(`
			INSERT INTO plugin_rows
			  (id, instance, field, identity, position, data, secrets,
			   created_by, created_at, updated_by, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			r.ID, instance, field, identity, r.Position, encodedData, encodedSecrets,
			actor, now.UnixMilli(), actor, now.UnixMilli())
		if err != nil {
			if isUniqueViolation(err, "ux_plugin_rows_identity") || isUniqueViolation(err, "plugin_rows.identity") {
				return ErrRowExists
			}
			return fmt.Errorf("sqlite: create plugin row: %w", err)
		}
		return nil
	})
	if err != nil {
		return PluginRow{}, err
	}
	return r, nil
}

// Update replaces a row's non-secret columns and merges its secrets.
//
// Data replaces the row's data whole, because a form submits every non-secret
// column. Secrets are merged: setSecrets are written, clearSecrets are removed,
// and anything else is kept -- which is what lets one credential be replaced
// without the others being retyped.
func (s *PluginRowStore) Update(ctx context.Context, actor, id, identity string,
	data map[string]any, setSecrets map[string]string, clearSecrets []string) (PluginRow, error) {
	current, ok, err := s.Get(ctx, id)
	if err != nil {
		return PluginRow{}, err
	}
	if !ok {
		return PluginRow{}, ErrNoSuchRow
	}
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return PluginRow{}, fmt.Errorf("sqlite: a row needs a name")
	}

	next := current
	next.Identity = identity
	next.Data = data
	if next.Data == nil {
		next.Data = map[string]any{}
	}
	next.Secrets = make(map[string]string, len(current.Secrets)+len(setSecrets))
	for k, v := range current.Secrets {
		next.Secrets[k] = v
	}
	for _, k := range clearSecrets {
		delete(next.Secrets, k)
	}
	for k, v := range setSecrets {
		next.Secrets[k] = v
	}

	encodedData, encodedSecrets, err := s.encode(next.Data, next.Secrets)
	if err != nil {
		return PluginRow{}, err
	}
	now := s.now()
	next.UpdatedBy, next.UpdatedAt = actor, now
	err = s.db.WriteTx(ctx, now.UnixMilli(), func(u *UnitOfWork) error {
		// Guarded on updated_at as well as id: two administrators editing one
		// row at once must not have the second silently overwrite a change the
		// first made and nobody saw.
		res, err := u.exec(`
			UPDATE plugin_rows
			   SET identity = ?, data = ?, secrets = ?, updated_by = ?, updated_at = ?
			 WHERE id = ? AND updated_at = ?`,
			identity, encodedData, encodedSecrets, actor, now.UnixMilli(),
			id, current.UpdatedAt.UnixMilli())
		if err != nil {
			if isUniqueViolation(err, "ux_plugin_rows_identity") || isUniqueViolation(err, "plugin_rows.identity") {
				return ErrRowExists
			}
			return fmt.Errorf("sqlite: update plugin row: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNoSuchRow
		}
		return nil
	})
	if err != nil {
		return PluginRow{}, err
	}
	return next, nil
}

// Delete forgets one row.
func (s *PluginRowStore) Delete(ctx context.Context, id string) error {
	now := s.now()
	return s.db.WriteTx(ctx, now.UnixMilli(), func(u *UnitOfWork) error {
		res, err := u.exec(`DELETE FROM plugin_rows WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("sqlite: delete plugin row: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNoSuchRow
		}
		return nil
	})
}

// DeleteAll forgets every row an instance holds, for when the instance itself
// is removed.
func (s *PluginRowStore) DeleteAll(ctx context.Context, instance string) error {
	now := s.now()
	return s.db.WriteTx(ctx, now.UnixMilli(), func(u *UnitOfWork) error {
		if _, err := u.exec(`DELETE FROM plugin_rows WHERE instance = ?`, instance); err != nil {
			return fmt.Errorf("sqlite: delete plugin rows: %w", err)
		}
		return nil
	})
}

// encode prepares the two JSON columns, encrypting the secrets.
func (s *PluginRowStore) encode(data map[string]any, secrets map[string]string) (string, string, error) {
	if data == nil {
		data = map[string]any{}
	}
	encodedData, err := json.Marshal(data)
	if err != nil {
		return "", "", fmt.Errorf("sqlite: encode plugin row: %w", err)
	}
	if len(secrets) == 0 {
		return string(encodedData), "", nil
	}
	if s.cipher == nil {
		return "", "", ErrRowNoCipher
	}
	plain, err := json.Marshal(secrets)
	if err != nil {
		return "", "", fmt.Errorf("sqlite: encode plugin row secrets: %w", err)
	}
	sealed, err := s.cipher.Encrypt(string(plain))
	if err != nil {
		return "", "", fmt.Errorf("sqlite: encrypt plugin row secrets: %w", err)
	}
	return string(encodedData), sealed, nil
}
