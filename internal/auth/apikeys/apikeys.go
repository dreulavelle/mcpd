// Package apikeys holds bearer credentials this host issued itself.
//
// A key is not a second authorization model. It carries a principal identity,
// a role and a set of plugin grants, which is exactly what a static token in
// `auth.static_tokens` has always carried -- so a key is that declaration
// moved into the database, and what moving it buys is revocation, expiry, a
// last-used timestamp, and grants that follow a group rather than a file.
//
// Because a key has an identity of its own, the audit trail names *which key*
// acted rather than a shared service identity. That is the reason the feature
// is worth building: with a standing rule able to authorise a write unasked,
// "which agent did this" has to be answerable.
//
// The secret is shown once, at creation, and stored as a digest. There is no
// endpoint that reads one back, and no error body carries one.
package apikeys

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/auth/groups"
	"github.com/spoked/mcpd/internal/storage/sqlite"
)

// Errors returned by this package.
//
// The three refusals are separate values on purpose, and the separation is
// operator-facing rather than caller-facing. "This key was revoked", "this key
// expired" and "no such key" need different words in a log and different words
// on the Keys page -- an operator chasing a connector that stopped working
// needs to know which -- while the caller is told only that its credential was
// not accepted. Verifier is what enforces that: it logs the reason and returns
// auth.ErrUnauthenticated, so nothing about the distinction reaches a probing
// caller.
var (
	// ErrNotFound reports an unknown key.
	ErrNotFound = errors.New("apikeys: no such key")
	// ErrRevoked reports a key an administrator withdrew.
	ErrRevoked = errors.New("apikeys: that key was revoked")
	// ErrExpired reports a key past its expiry.
	ErrExpired = errors.New("apikeys: that key has expired")
	// ErrAlreadyRevoked reports revoking a key that is already revoked.
	ErrAlreadyRevoked = errors.New("apikeys: that key is already revoked")
)

// SecretPrefix marks a credential this host issued.
//
// Worth the five characters: it makes a leaked string recognisable to a secret
// scanner and to whoever finds it in a log, and it lets verification skip the
// database for a credential that cannot be one of these.
const SecretPrefix = "mcpd_"

// IDPrefix begins every key identifier.
//
// Key identifiers are generated rather than chosen, which is what keeps them
// from colliding with a static token's id. Config validation refuses a file
// token whose id begins with this, so the two namespaces cannot meet from
// either direction and an audit entry naming a credential names exactly one.
const IDPrefix = "key_"

// MaxNameRunes bounds a key's name. The schema enforces the same bound.
const MaxNameRunes = 64

// Status is what a key is, right now.
type Status string

const (
	// StatusActive is a key that would be accepted.
	StatusActive Status = "active"
	// StatusExpired is a key past its expiry.
	StatusExpired Status = "expired"
	// StatusRevoked is a key an administrator withdrew.
	StatusRevoked Status = "revoked"
)

// Key is one credential. The secret is not a field: it exists once, in the
// reply to the request that created it.
type Key struct {
	ID          string
	Name        string
	Role        auth.Role
	Plugins     []string
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ExpiresAt   *time.Time
	LastUsedAt  *time.Time
	RevokedAt   *time.Time
	RevokedBy   string
	groupsCache []*groups.Group
}

// Status reports whether the key would be accepted at t.
//
// Revoked outranks expired: a key an administrator withdrew is withdrawn
// whatever its expiry says, and reporting it as merely expired would suggest
// reissuing it is a matter of moving a date.
func (k *Key) Status(t time.Time) Status {
	switch {
	case k.RevokedAt != nil:
		return StatusRevoked
	case k.ExpiresAt != nil && !t.Before(*k.ExpiresAt):
		return StatusExpired
	}
	return StatusActive
}

// Groups lists the groups this key belongs to, when they were loaded.
func (k *Key) Groups() []*groups.Group { return k.groupsCache }

// Store persists keys.
type Store struct {
	db     *sqlite.DB
	groups *groups.Store
	now    func() time.Time
}

// NewStore returns a store backed by db.
func NewStore(db *sqlite.DB, gs *groups.Store, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, groups: gs, now: now}
}

// --- creating --------------------------------------------------------------

// CreateRequest describes a new key.
type CreateRequest struct {
	Name string
	Role auth.Role
	// Plugins are the key's own grants. Empty is the default and reaches
	// nothing; groups are how a key usually gets its reach.
	Plugins []string
	// Groups the key joins, in the same transaction.
	Groups []string
	// ExpiresAt is optional. Nil never expires, which is honest for a
	// long-lived connector: inventing a date would mean an integration that
	// stops working on a day nobody chose.
	ExpiresAt *time.Time
}

// Create issues a key and returns its secret, once.
//
// The secret is returned here and nowhere else. It is not stored, not logged,
// and no endpoint reads it back -- what is stored is its SHA-256, which is
// what verification compares against.
//
// Creating a key is a privilege grant, so the audit entry is written in the
// same transaction as the row, naming the administrator who issued it, what it
// may reach, and what it may do. An entry committed separately from the grant
// it records is a trail that can disagree with the database about who may act.
func (s *Store) Create(ctx context.Context, actor string, req CreateRequest) (*Key, string, error) {
	name, err := ValidateName(req.Name)
	if err != nil {
		return nil, "", err
	}
	if !req.Role.Valid() {
		return nil, "", fmt.Errorf("apikeys: role %q is not one of %q or %q",
			req.Role, auth.RoleUser, auth.RoleAdmin)
	}
	plugins := groups.NormalizeGrants(req.Plugins)
	encoded, err := json.Marshal(plugins)
	if err != nil {
		return nil, "", fmt.Errorf("apikeys: encode plugin grants: %w", err)
	}
	id, err := newID()
	if err != nil {
		return nil, "", err
	}
	secret, err := GenerateSecret()
	if err != nil {
		return nil, "", err
	}

	now := s.now()
	var expires *int64
	if req.ExpiresAt != nil {
		if !req.ExpiresAt.After(now) {
			return nil, "", fmt.Errorf("apikeys: an expiry in the past would issue a key that is already dead")
		}
		ms := req.ExpiresAt.UnixMilli()
		expires = &ms
	}

	stamp := now.UnixMilli()
	err = s.db.WriteTx(ctx, stamp, func(tx *sqlite.UnitOfWork) error {
		if err := tx.Exec(`
			INSERT INTO api_keys (id, name, secret_hash, role, plugins_json,
			                      created_by, created_at, updated_at, expires_at)
			VALUES (?,?,?,?,?,?,?,?,?)`,
			id, name, digest(secret), string(req.Role), string(encoded),
			actor, stamp, stamp, expires); err != nil {
			return err
		}
		joined := []string{}
		for _, groupID := range req.Groups {
			if err := groups.AddMemberAudited(tx, actor, groupID,
				groups.Key(id), stamp); err != nil {
				return err
			}
			joined = append(joined, groupID)
		}
		return tx.AppendAudit(sqlite.AdminAct{
			Kind:    "apikey.created",
			Actor:   actor,
			Subject: id,
			Action:  "create",
			Detail: map[string]any{
				"name":       name,
				"role":       string(req.Role),
				"plugins":    plugins,
				"groups":     joined,
				"expires_at": expiryDetail(req.ExpiresAt),
			},
		})
	})
	if err != nil {
		return nil, "", err
	}

	key := &Key{
		ID: id, Name: name, Role: req.Role, Plugins: plugins,
		CreatedBy: actor,
		CreatedAt: time.UnixMilli(stamp).UTC(),
		UpdatedAt: time.UnixMilli(stamp).UTC(),
		ExpiresAt: req.ExpiresAt,
	}
	return key, secret, nil
}

// --- reading ---------------------------------------------------------------

const keyColumns = `id, name, role, plugins_json, created_by, created_at,
	updated_at, expires_at, last_used_at, revoked_at, revoked_by`

// List returns every key, newest first. The secret digest is deliberately not
// among the columns: nothing above this layer has a use for it, and every
// serialisation is a chance to leak one.
func (s *Store) List(ctx context.Context) ([]*Key, error) {
	rows, err := s.db.Reader().QueryContext(ctx,
		`SELECT `+keyColumns+` FROM api_keys ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []*Key{}
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, k := range out {
		if k.groupsCache, err = s.groups.Of(ctx, groups.Key(k.ID)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ByID loads one key.
func (s *Store) ByID(ctx context.Context, id string) (*Key, error) {
	k, err := scanKey(s.db.Reader().QueryRowContext(ctx,
		`SELECT `+keyColumns+` FROM api_keys WHERE id = ?`, id))
	if err != nil {
		return nil, err
	}
	if k.groupsCache, err = s.groups.Of(ctx, groups.Key(k.ID)); err != nil {
		return nil, err
	}
	return k, nil
}

// --- editing ---------------------------------------------------------------

// UpdateRequest describes a re-scope. Nil fields are left alone.
type UpdateRequest struct {
	Name    *string
	Role    *auth.Role
	Plugins *[]string
	// ExpiresAt sets or clears the expiry. The double pointer distinguishes
	// "leave it alone" from "clear it", which one pointer cannot.
	ExpiresAt **time.Time
}

// Update re-scopes a key.
//
// A privilege change, audited as one, in the same transaction, carrying what
// the grant was as well as what it became -- an entry naming only the new
// value leaves "what did this widen" unanswerable.
//
// It takes effect on the key's next request. Nothing about a key is cached
// between requests: the row is read and the grants are resolved every time,
// which is the same property Can() already has for a pending account.
func (s *Store) Update(ctx context.Context, actor, id string, req UpdateRequest) (*Key, error) {
	var name string
	var err error
	if req.Name != nil {
		if name, err = ValidateName(*req.Name); err != nil {
			return nil, err
		}
	}
	if req.Role != nil && !req.Role.Valid() {
		return nil, fmt.Errorf("apikeys: role %q is not one of %q or %q",
			*req.Role, auth.RoleUser, auth.RoleAdmin)
	}
	var plugins []string
	if req.Plugins != nil {
		plugins = groups.NormalizeGrants(*req.Plugins)
	}

	now := s.now()
	stamp := now.UnixMilli()
	err = s.db.WriteTx(ctx, stamp, func(tx *sqlite.UnitOfWork) error {
		var wasName, wasRole, wasPlugins string
		var wasExpiry, revoked sql.NullInt64
		if err := tx.QueryRow(
			`SELECT name, role, plugins_json, expires_at, revoked_at
			   FROM api_keys WHERE id = ?`, id).
			Scan(&wasName, &wasRole, &wasPlugins, &wasExpiry, &revoked); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if revoked.Valid {
			// Re-scoping a revoked key would record a grant nobody can use and
			// would suggest the key is coming back. Issue a new one instead.
			return ErrRevoked
		}

		sets := []string{"updated_at = ?"}
		args := []any{stamp}
		detail := map[string]any{}
		if req.Name != nil {
			sets = append(sets, "name = ?")
			args = append(args, name)
			detail["name"] = name
			detail["name_before"] = wasName
		}
		if req.Role != nil {
			sets = append(sets, "role = ?")
			args = append(args, string(*req.Role))
			detail["role"] = string(*req.Role)
			detail["role_before"] = wasRole
		}
		if req.Plugins != nil {
			encoded, err := json.Marshal(plugins)
			if err != nil {
				return fmt.Errorf("apikeys: encode plugin grants: %w", err)
			}
			sets = append(sets, "plugins_json = ?")
			args = append(args, string(encoded))
			detail["plugins"] = plugins
			detail["plugins_before"] = decodeGrants(wasPlugins)
		}
		if req.ExpiresAt != nil {
			var ms *int64
			if *req.ExpiresAt != nil {
				if !(*req.ExpiresAt).After(now) {
					return fmt.Errorf("apikeys: an expiry in the past would kill the key it is set on")
				}
				v := (*req.ExpiresAt).UnixMilli()
				ms = &v
			}
			sets = append(sets, "expires_at = ?")
			args = append(args, ms)
			detail["expires_at"] = expiryDetail(*req.ExpiresAt)
			// The rule this feature holds every privilege change to: an entry
			// with only the new value leaves "what did this widen"
			// unanswerable. Extending a key from next month to next year is a
			// grant of a year's more reach, and the trail has to say how much
			// was added rather than only when it now ends.
			detail["expires_at_before"] = expiryDetail(optionalTime(wasExpiry))
		}
		if len(detail) == 0 {
			return nil
		}
		args = append(args, id)

		// Guarded on the key still being live: a revocation landing between
		// the read above and this write must win, rather than being undone by
		// an edit that started first.
		affected, err := tx.ExecAffected(
			`UPDATE api_keys SET `+strings.Join(sets, ", ")+
				` WHERE id = ? AND revoked_at IS NULL`, args...)
		if err != nil {
			return err
		}
		if affected == 0 {
			return ErrRevoked
		}
		return tx.AppendAudit(sqlite.AdminAct{
			Kind:    "apikey.rescoped",
			Actor:   actor,
			Subject: id,
			Action:  "rescope",
			Detail:  detail,
		})
	})
	if err != nil {
		return nil, err
	}
	return s.ByID(ctx, id)
}

// Revoke withdraws a key.
//
// The row stays. A deleted key would leave every audit entry naming an
// identifier that resolves to nothing, and "which agent did this" is the
// question the whole feature exists to answer.
//
// Guarded on the key being live, so two administrators revoking the same key
// produce one revocation and one refusal rather than two entries claiming
// something that happened once.
func (s *Store) Revoke(ctx context.Context, actor, id string) error {
	stamp := s.now().UnixMilli()
	return s.db.WriteTx(ctx, stamp, func(tx *sqlite.UnitOfWork) error {
		var name string
		if err := tx.QueryRow(`SELECT name FROM api_keys WHERE id = ?`, id).Scan(&name); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		affected, err := tx.ExecAffected(`
			UPDATE api_keys SET revoked_at = ?, revoked_by = ?, updated_at = ?
			 WHERE id = ? AND revoked_at IS NULL`, stamp, actor, stamp, id)
		if err != nil {
			return err
		}
		if affected == 0 {
			return ErrAlreadyRevoked
		}
		return tx.AppendAudit(sqlite.AdminAct{
			Kind:    "apikey.revoked",
			Actor:   actor,
			Subject: id,
			Action:  "revoke",
			Detail:  map[string]any{"name": name},
		})
	})
}

// --- verifying -------------------------------------------------------------

// lastUsedResolution is how stale a last-used stamp may get.
//
// A write on every request would put SQLite's single writer on the hot path of
// every tool call to answer a question nobody asks to the second. A minute is
// enough to find a forgotten key and cheap enough to be free.
const lastUsedResolution = time.Minute

// Verify turns a presented secret into a principal.
//
// The grants on the returned principal are the union groups.Effective
// computes: the key's own, plus every group it belongs to. It is resolved here
// rather than stored on the row, which is what makes adding a key to a group
// take effect on the next request.
//
// Returns a typed refusal so that a caller can log which of the three it was.
// Nothing above this returns it to whoever presented the credential; see
// Verifier.
func (s *Store) Verify(ctx context.Context, secret string) (*auth.Principal, error) {
	if !strings.HasPrefix(secret, SecretPrefix) {
		return nil, ErrNotFound
	}
	k, err := scanKey(s.db.Reader().QueryRowContext(ctx,
		`SELECT `+keyColumns+` FROM api_keys WHERE secret_hash = ?`, digest(secret)))
	if err != nil {
		return nil, err
	}

	now := s.now()
	switch k.Status(now) {
	case StatusRevoked:
		return nil, ErrRevoked
	case StatusExpired:
		return nil, ErrExpired
	}

	granted, err := s.groups.Effective(ctx, groups.Key(k.ID))
	if err != nil {
		return nil, err
	}
	s.touch(ctx, k, now)

	return &auth.Principal{
		// The identifier, not the name. A name is a rendering and its owner
		// can change it; the trail has to name the credential that acted.
		ID:          "key:" + k.ID,
		DisplayName: k.Name,
		Role:        k.Role,
		Plugins:     granted,
		TokenID:     k.ID,
	}, nil
}

// touch records that a key was used, at most once per lastUsedResolution.
//
// Best effort: a lost timestamp is not worth refusing a request that
// authenticated. The condition is in the WHERE clause so two concurrent
// requests write once between them rather than racing.
func (s *Store) touch(ctx context.Context, k *Key, now time.Time) {
	if k.LastUsedAt != nil && now.Sub(*k.LastUsedAt) < lastUsedResolution {
		return
	}
	stamp := now.UnixMilli()
	threshold := now.Add(-lastUsedResolution).UnixMilli()
	_ = s.db.WriteTx(ctx, stamp, func(tx *sqlite.UnitOfWork) error {
		return tx.Exec(`
			UPDATE api_keys SET last_used_at = ?
			 WHERE id = ? AND (last_used_at IS NULL OR last_used_at <= ?)`,
			stamp, k.ID, threshold)
	})
}

// Verifier accepts a database key, falling back to whatever authenticated
// before it existed.
//
// Static tokens are tried first, and that order is the whole of the
// compatibility promise: a file token is matched in memory, reaches no table,
// and is answered exactly as it was before this package existed. Only a
// credential no static token matches costs a query.
type Verifier struct {
	store *Store
	next  auth.TokenVerifier
	log   *slog.Logger
}

// NewVerifier wraps next with database-issued keys.
func NewVerifier(store *Store, next auth.TokenVerifier, log *slog.Logger) *Verifier {
	if log == nil {
		log = slog.Default()
	}
	return &Verifier{store: store, next: next, log: log}
}

// Scheme implements auth.TokenVerifier.
func (v *Verifier) Scheme() string {
	if v.next == nil {
		return "key"
	}
	return v.next.Scheme() + "+key"
}

// Verify implements auth.TokenVerifier.
//
// Every failure is auth.ErrUnauthenticated, whatever it actually was.
// Distinguishing "revoked" from "expired" from "unknown" in the reply would
// hand a caller an oracle it can probe; the distinction belongs in this
// host's log and on the Keys page, and that is where it goes.
func (v *Verifier) Verify(ctx context.Context, token string, r *http.Request) (*auth.Principal, error) {
	if v.next != nil {
		if p, err := v.next.Verify(ctx, token, r); err == nil {
			return p, nil
		}
	}
	if v.store == nil {
		return nil, auth.ErrUnauthenticated
	}
	p, err := v.store.Verify(ctx, token)
	switch {
	case errors.Is(err, ErrRevoked), errors.Is(err, ErrExpired):
		v.log.Warn("an API key was refused",
			"reason", err.Error(), "token_fingerprint", auth.Fingerprint(token))
		return nil, auth.ErrUnauthenticated
	case err != nil:
		return nil, auth.ErrUnauthenticated
	}
	return p, nil
}

// --- validation and plumbing -----------------------------------------------

// ValidateName checks and normalises a key's name.
//
// The same rules a display name and a group name are held to: it is rendered
// in a list, it appears beside an audit entry, and a control or
// invisible-formatting character in it makes it read as something it is not.
func ValidateName(raw string) (string, error) {
	return groups.ValidateLabel("apikeys", "key name", raw)
}

// GenerateSecret returns a credential with 32 bytes of entropy behind the
// prefix, encoded URL-safely so it survives headers, environment variables and
// shell quoting.
func GenerateSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("apikeys: system entropy unavailable: %w", err)
	}
	return SecretPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// digest derives the stored form of a secret.
//
// SHA-256 without a salt, which is correct here and would be wrong for a
// password: the value carries 256 bits from a CSPRNG, so there is no
// dictionary to precompute and no work factor worth paying, and salting would
// only prevent the lookup by digest that verification depends on. The session
// store hashes its tokens the same way for the same reason.
func digest(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("apikeys: system entropy unavailable: %w", err)
	}
	return IDPrefix + hex.EncodeToString(b), nil
}

type rowScanner interface{ Scan(...any) error }

func scanKey(row rowScanner) (*Key, error) {
	var (
		k         Key
		role      string
		plugins   string
		created   int64
		updated   int64
		expires   sql.NullInt64
		lastUsed  sql.NullInt64
		revoked   sql.NullInt64
		revokedBy sql.NullString
	)
	err := row.Scan(&k.ID, &k.Name, &role, &plugins, &k.CreatedBy,
		&created, &updated, &expires, &lastUsed, &revoked, &revokedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	k.Role = auth.Role(role)
	k.Plugins = decodeGrants(plugins)
	k.CreatedAt = time.UnixMilli(created).UTC()
	k.UpdatedAt = time.UnixMilli(updated).UTC()
	k.ExpiresAt = optionalTime(expires)
	k.LastUsedAt = optionalTime(lastUsed)
	k.RevokedAt = optionalTime(revoked)
	k.RevokedBy = revokedBy.String
	return &k, nil
}

func optionalTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.UnixMilli(v.Int64).UTC()
	return &t
}

// decodeGrants reads a stored grant list. A value this build cannot parse
// reads as no grants, which is the safe direction.
func decodeGrants(encoded string) []string {
	var list []string
	if err := json.Unmarshal([]byte(encoded), &list); err != nil {
		return []string{}
	}
	if list == nil {
		return []string{}
	}
	return list
}

func expiryDetail(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}
