package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spoked/mcpd/internal/storage/sqlite"
)

// Store persists runtime configuration.
type Store struct {
	db     *sqlite.DB
	cipher *Cipher
	now    func() time.Time

	// mu guards the cache. Settings are read on nearly every request that
	// touches policy, and written rarely, so they are cached in memory and
	// invalidated on write.
	mu     sync.RWMutex
	cache  map[string]string
	loaded bool

	// watchers are notified after a successful write, so components that hold
	// derived state can rebuild it.
	watchMu  sync.Mutex
	watchers []func(changed []string)
}

// NewStore returns a settings store. cipher may be nil, in which case writing
// a secret fails rather than storing it in the clear.
func NewStore(db *sqlite.DB, cipher *Cipher, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, cipher: cipher, now: now, cache: make(map[string]string)}
}

// HasCipher reports whether secrets can be stored.
func (s *Store) HasCipher() bool { return s.cipher != nil }

// Watch registers a callback invoked after settings change.
//
// It runs synchronously inside the write path, so a watcher must be quick and
// must not write settings itself.
func (s *Store) Watch(fn func(changed []string)) {
	s.watchMu.Lock()
	s.watchers = append(s.watchers, fn)
	s.watchMu.Unlock()
}

// load fills the cache from the database.
func (s *Store) load(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded {
		return nil
	}

	rows, err := s.db.Reader().QueryContext(ctx, `SELECT key, value, encrypted FROM settings`)
	if err != nil {
		return fmt.Errorf("settings: read: %w", err)
	}
	defer rows.Close()

	cache := make(map[string]string)
	var undecryptable []string

	for rows.Next() {
		var key, value string
		var encrypted int
		if err := rows.Scan(&key, &value, &encrypted); err != nil {
			return err
		}
		if encrypted == 1 {
			plain, decErr := s.cipher.Decrypt(value)
			if decErr != nil {
				// One unreadable secret must not make every other setting
				// unavailable: the host should still start, report which
				// values need re-entering, and let an operator fix them.
				undecryptable = append(undecryptable, key)
				continue
			}
			value = plain
		}
		cache[key] = value
	}
	if err := rows.Err(); err != nil {
		return err
	}

	s.cache = cache
	s.loaded = true

	if len(undecryptable) > 0 {
		sort.Strings(undecryptable)
		return fmt.Errorf("%w (affected: %v)", ErrDecrypt, undecryptable)
	}
	return nil
}

// Get returns a raw stored value.
func (s *Store) Get(ctx context.Context, key string) (string, bool, error) {
	if err := s.load(ctx); err != nil && !errors.Is(err, ErrDecrypt) {
		return "", false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.cache[key]
	return v, ok, nil
}

// WithPrefix returns every stored key beginning with prefix.
//
// It exists for the settings whose names are not known in advance -- a plugin
// instance is recorded by its own name, so there is no schema entry to look
// up. A copy rather than the cache itself, since the caller holds no lock.
func (s *Store) WithPrefix(ctx context.Context, prefix string) map[string]string {
	if err := s.load(ctx); err != nil && !errors.Is(err, ErrDecrypt) {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := map[string]string{}
	for k, v := range s.cache {
		if strings.HasPrefix(k, prefix) {
			out[k] = v
		}
	}
	return out
}

// GetJSON decodes a stored value into out.
func (s *Store) GetJSON(ctx context.Context, key string, out any) (bool, error) {
	raw, ok, err := s.Get(ctx, key)
	if err != nil || !ok {
		return false, err
	}
	if err := json.Unmarshal([]byte(raw), out); err != nil {
		return false, fmt.Errorf("settings: %s holds unreadable JSON: %w", key, err)
	}
	return true, nil
}

// Change is one setting update.
type Change struct {
	Key   string
	Value string
	// Secret marks a value to encrypt at rest. It is a property of the write
	// rather than of the key, so a caller cannot accidentally store a
	// credential in the clear by using the wrong helper.
	Secret bool
	// Delete removes the key instead of setting it.
	Delete bool
}

// Apply writes a batch of changes atomically.
//
// All or nothing matters here: applying half a tunnel configuration would
// leave the host in a state no operator asked for and none of the validation
// checked.
func (s *Store) Apply(ctx context.Context, actor string, changes []Change) error {
	if len(changes) == 0 {
		return nil
	}
	if err := s.load(ctx); err != nil && !errors.Is(err, ErrDecrypt) {
		return err
	}

	for _, c := range changes {
		if c.Secret && !c.Delete && s.cipher == nil {
			return fmt.Errorf(
				"settings: %s is a secret and no encryption key is configured; "+
					"set one so it is not stored in the clear", c.Key)
		}
	}

	now := s.now()
	applied := make(map[string]string, len(changes))
	removed := make([]string, 0, len(changes))
	keys := make([]string, 0, len(changes))

	err := s.db.WriteTx(ctx, now.UnixMilli(), func(tx *sqlite.UnitOfWork) error {
		for _, c := range changes {
			old, oldSecret, err := readCurrent(tx, c.Key)
			if err != nil {
				return err
			}

			if c.Delete {
				if err := tx.Exec(`DELETE FROM settings WHERE key = ?`, c.Key); err != nil {
					return err
				}
				removed = append(removed, c.Key)
			} else {
				stored := c.Value
				encrypted := 0
				if c.Secret {
					sealed, err := s.cipher.Encrypt(c.Value)
					if err != nil {
						return err
					}
					stored, encrypted = sealed, 1
				}
				if err := tx.Exec(`
					INSERT INTO settings (key, value, encrypted, updated_at, updated_by)
					VALUES (?,?,?,?,?)
					ON CONFLICT (key) DO UPDATE SET
						value = excluded.value,
						encrypted = excluded.encrypted,
						updated_at = excluded.updated_at,
						updated_by = excluded.updated_by`,
					c.Key, stored, encrypted, now.UnixMilli(), actor); err != nil {
					return err
				}
				applied[c.Key] = c.Value
			}

			// History never records a secret, in either column. Doing so would
			// make this table the plaintext copy the encryption exists to
			// prevent.
			redacted := c.Secret || oldSecret
			var oldRecorded, newRecorded any
			if !redacted {
				oldRecorded = nullIfEmpty(old)
				if !c.Delete {
					newRecorded = c.Value
				}
			}
			if err := tx.Exec(`
				INSERT INTO settings_history
					(key, old_value, new_value, redacted, changed_at, changed_by)
				VALUES (?,?,?,?,?,?)`,
				c.Key, oldRecorded, newRecorded, boolInt(redacted),
				now.UnixMilli(), actor); err != nil {
				return err
			}
			keys = append(keys, c.Key)
		}
		return nil
	})
	if err != nil {
		return err
	}

	s.mu.Lock()
	for k, v := range applied {
		s.cache[k] = v
	}
	for _, k := range removed {
		delete(s.cache, k)
	}
	s.mu.Unlock()

	s.notify(keys)
	return nil
}

// SetJSON encodes and stores a value.
func (s *Store) SetJSON(ctx context.Context, actor, key string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("settings: encode %s: %w", key, err)
	}
	return s.Apply(ctx, actor, []Change{{Key: key, Value: string(encoded)}})
}

// SetSecret stores an encrypted value.
func (s *Store) SetSecret(ctx context.Context, actor, key, value string) error {
	return s.Apply(ctx, actor, []Change{{Key: key, Value: value, Secret: true}})
}

// HistoryEntry is one recorded change.
type HistoryEntry struct {
	Seq       int64     `json:"seq"`
	Key       string    `json:"key"`
	OldValue  string    `json:"old_value,omitempty"`
	NewValue  string    `json:"new_value,omitempty"`
	Redacted  bool      `json:"redacted"`
	ChangedAt time.Time `json:"changed_at"`
	ChangedBy string    `json:"changed_by"`
}

// History returns recent configuration changes.
func (s *Store) History(ctx context.Context, limit int) ([]HistoryEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Reader().QueryContext(ctx, `
		SELECT seq, key, COALESCE(old_value,''), COALESCE(new_value,''),
		       redacted, changed_at, changed_by
		FROM settings_history ORDER BY seq DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("settings: read history: %w", err)
	}
	defer rows.Close()

	var out []HistoryEntry
	for rows.Next() {
		var e HistoryEntry
		var redacted int
		var at int64
		if err := rows.Scan(&e.Seq, &e.Key, &e.OldValue, &e.NewValue,
			&redacted, &at, &e.ChangedBy); err != nil {
			return nil, err
		}
		e.Redacted = redacted == 1
		e.ChangedAt = time.UnixMilli(at).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) notify(changed []string) {
	s.watchMu.Lock()
	watchers := make([]func([]string), len(s.watchers))
	copy(watchers, s.watchers)
	s.watchMu.Unlock()

	for _, fn := range watchers {
		fn(changed)
	}
}

func readCurrent(tx *sqlite.UnitOfWork, key string) (value string, secret bool, err error) {
	var encrypted int
	err = tx.QueryRow(`SELECT value, encrypted FROM settings WHERE key = ?`, key).
		Scan(&value, &encrypted)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, encrypted == 1, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
