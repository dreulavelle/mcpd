package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/operations"
	"github.com/spoked/mcpd/internal/tunnel"
)

// Cipher is the encryption a stored credential goes through.
//
// Declared here as the two methods this store needs rather than taken as
// *settings.Cipher, so that storage does not depend on the settings package to
// hold a credential of its own. The implementation is the same one every other
// stored secret uses; there is deliberately no second cipher.
type Cipher interface {
	Encrypt(plaintext string) (string, error)
	Decrypt(ciphertext string) (string, error)
}

// ChatGPTAccountStore holds the ChatGPT accounts this host connects to.
//
// Credentials are encrypted at rest and decrypted on the way out, which is the
// same arrangement `settings` has always had for the single key this table
// replaces. Nothing here logs a key, and List returns them in the clear only
// because the tunnels are about to authenticate with them -- the dashboard is
// served a redacted view built above this layer.
type ChatGPTAccountStore struct {
	db     *DB
	cipher Cipher
	now    func() time.Time
}

// NewChatGPTAccountStore returns a store backed by db.
//
// A nil cipher leaves the store usable for everything but credentials, which
// is what a host with no encryption key has: it can be told an account exists
// and refuses to read or write its key rather than storing one in the clear.
func NewChatGPTAccountStore(db *DB, cipher Cipher, now func() time.Time) *ChatGPTAccountStore {
	if now == nil {
		now = time.Now
	}
	return &ChatGPTAccountStore{db: db, cipher: cipher, now: now}
}

// ErrNoSuchAccount reports an operation against an account that is not stored.
var ErrNoSuchAccount = errors.New("sqlite: no such ChatGPT account")

// ErrAccountExists reports a name or principal already taken.
var ErrAccountExists = errors.New(
	"sqlite: a ChatGPT account with that name already exists")

// ErrNoCipher reports that a credential cannot be handled without a key.
// ErrNoSuchRole reports a role id that names nothing.
var ErrNoSuchRole = errors.New("sqlite: no such role")

var ErrNoCipher = errors.New(
	"sqlite: no encryption key is configured, so a ChatGPT account's " +
		"credentials cannot be stored or read")

// Create stores a new account.
//
// The insert is guarded by the unique indexes rather than by a prior read, so
// two administrators racing the same name produce one account and one refusal.
func (s *ChatGPTAccountStore) Create(ctx context.Context, actor string, a tunnel.Account) (tunnel.Account, error) {
	a.Name = strings.TrimSpace(a.Name)
	if strings.TrimSpace(a.Principal) == "" {
		a.Principal = tunnel.PrincipalFor(a.Name)
	}
	if len(a.Grants) == 0 {
		a.Grants = auth.GrantsAt([]string{auth.Wildcard}, auth.LevelWrite)
	}
	a.Grants = a.Grants.Normalize()
	if a.RoleID == "" {
		a.RoleID = auth.RoleOperator
	}
	if err := a.Validate(); err != nil {
		return tunnel.Account{}, err
	}
	if s.cipher == nil {
		return tunnel.Account{}, ErrNoCipher
	}

	apiKey, err := s.cipher.Encrypt(a.APIKey)
	if err != nil {
		return tunnel.Account{}, fmt.Errorf("sqlite: encrypt account key: %w", err)
	}
	adminKey, err := s.encryptOptional(a.AdminKey)
	if err != nil {
		return tunnel.Account{}, err
	}
	now := s.now()
	a.ID = newAccountID()
	a.CreatedBy = actor
	a.CreatedAt = now
	a.UpdatedAt = now

	err = s.db.WriteTx(ctx, now.UnixMilli(), func(u *UnitOfWork) error {
		if err := roleExists(u, a.RoleID); err != nil {
			return err
		}
		_, err := u.exec(`
			INSERT INTO chatgpt_accounts
			  (id, name, api_key, admin_key, org_id, workspaces, principal, role_id, grants_json,
			   rate_per_sec, enabled, created_by, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			a.ID, a.Name, apiKey, adminKey, nullIfEmpty(a.OrgID), encodeWorkspaces(a.Workspaces), a.Principal,
			a.RoleID, auth.EncodeGrants(a.Grants), a.RatePerSec, boolToInt(a.Enabled),
			actor, now.UnixMilli(), now.UnixMilli())
		if err != nil {
			if isAccountConflict(err) {
				return ErrAccountExists
			}
			return fmt.Errorf("sqlite: create chatgpt account: %w", err)
		}
		return auditAccount(u, "chatgpt.account.added", actor, a, "add", map[string]any{
			"principal":    a.Principal,
			"role":         a.RoleID,
			"grants":       a.Grants,
			"rate_per_sec": a.RatePerSec,
			// Whether tunnels can be created from this account, which is the
			// difference between an account that manages its organisation and
			// one that only runs what it was pointed at.
			"has_admin_key": strings.TrimSpace(a.AdminKey) != "",
		})
	})
	if err != nil {
		return tunnel.Account{}, err
	}
	return a, nil
}

// Update edits an account in place, leaving unset fields alone.
func (s *ChatGPTAccountStore) Update(ctx context.Context, actor, id string, up tunnel.AccountUpdate) (tunnel.Account, error) {
	current, ok, err := s.Get(ctx, id)
	if err != nil {
		return tunnel.Account{}, err
	}
	if !ok {
		return tunnel.Account{}, ErrNoSuchAccount
	}

	next := current
	changed := map[string]any{}
	if up.Name != nil && *up.Name != current.Name {
		next.Name = strings.TrimSpace(*up.Name)
		changed["name"] = next.Name
	}
	if up.APIKey != nil {
		next.APIKey = *up.APIKey
		// The value is never recorded, only the fact that it moved -- which is
		// the part an operator reading the trail after a connector stopped
		// working actually needs.
		changed["api_key"] = "replaced"
	}
	if up.AdminKey != nil {
		next.AdminKey = *up.AdminKey
		changed["admin_key"] = map[bool]string{true: "cleared", false: "replaced"}[strings.TrimSpace(*up.AdminKey) == ""]
	}
	if up.OrgID != nil && *up.OrgID != current.OrgID {
		next.OrgID = strings.TrimSpace(*up.OrgID)
		changed["org_id"] = next.OrgID
	}
	if up.Workspaces != nil {
		next.Workspaces = tunnel.NormalizeWorkspaces(*up.Workspaces)
		if !slices.Equal(next.Workspaces, tunnel.NormalizeWorkspaces(current.Workspaces)) {
			changed["workspaces"] = next.Workspaces
		}
	}
	if up.RoleID != nil && *up.RoleID != current.RoleID {
		next.RoleID = *up.RoleID
		changed["role"] = next.RoleID
	}
	if up.Grants != nil {
		next.Grants = up.Grants.Normalize()
		if len(next.Grants) == 0 {
			next.Grants = auth.GrantsAt([]string{auth.Wildcard}, auth.LevelWrite)
		}
		if !next.Grants.Equal(current.Grants) {
			changed["grants"] = next.Grants
		}
	}
	if up.RatePerSec != nil && *up.RatePerSec != current.RatePerSec {
		next.RatePerSec = *up.RatePerSec
		changed["rate_per_sec"] = next.RatePerSec
	}
	if up.Enabled != nil && *up.Enabled != current.Enabled {
		next.Enabled = *up.Enabled
		changed["enabled"] = next.Enabled
	}

	if len(changed) == 0 {
		// Nothing moved. Recording it would put an entry in the trail for an
		// operator who opened a form and closed it.
		return current, nil
	}
	if err := next.Validate(); err != nil {
		return tunnel.Account{}, err
	}
	if s.cipher == nil {
		return tunnel.Account{}, ErrNoCipher
	}

	apiKey, err := s.cipher.Encrypt(next.APIKey)
	if err != nil {
		return tunnel.Account{}, fmt.Errorf("sqlite: encrypt account key: %w", err)
	}
	adminKey, err := s.encryptOptional(next.AdminKey)
	if err != nil {
		return tunnel.Account{}, err
	}
	now := s.now()
	next.UpdatedAt = now
	err = s.db.WriteTx(ctx, now.UnixMilli(), func(u *UnitOfWork) error {
		if _, moved := changed["role"]; moved {
			if err := roleExists(u, next.RoleID); err != nil {
				return err
			}
		}
		// Guarded on updated_at as well as id: two administrators editing one
		// account at once must not have the second silently overwrite a change
		// the first made and nobody saw.
		res, err := u.exec(`
			UPDATE chatgpt_accounts
			   SET name = ?, api_key = ?, admin_key = ?, org_id = ?, workspaces = ?, role_id = ?,
			       grants_json = ?, rate_per_sec = ?, enabled = ?, updated_at = ?
			 WHERE id = ? AND updated_at = ?`,
			next.Name, apiKey, adminKey, nullIfEmpty(next.OrgID), encodeWorkspaces(next.Workspaces), next.RoleID,
			auth.EncodeGrants(next.Grants), next.RatePerSec, boolToInt(next.Enabled),
			now.UnixMilli(), id, current.UpdatedAt.UnixMilli())
		if err != nil {
			if isAccountConflict(err) {
				return ErrAccountExists
			}
			return fmt.Errorf("sqlite: update chatgpt account: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			// Either it is gone, or somebody else wrote first. Both are a
			// refusal rather than a silent no-op.
			return ErrNoSuchAccount
		}
		return auditAccount(u, "chatgpt.account.updated", actor, next, "update", changed)
	})
	if err != nil {
		return tunnel.Account{}, err
	}
	return next, nil
}

// Delete forgets an account.
//
// Tunnels assigned to it are not touched here: an assignment is a setting, and
// clearing it is the caller's job so that the removal and the unassignment are
// one administrative act rather than a store reaching into another authority.
func (s *ChatGPTAccountStore) Delete(ctx context.Context, actor, id string) error {
	return s.db.WriteTx(ctx, s.now().UnixMilli(), func(u *UnitOfWork) error {
		var name, principal string
		err := u.queryRow(`SELECT name, principal FROM chatgpt_accounts WHERE id = ?`, id).
			Scan(&name, &principal)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNoSuchAccount
		}
		if err != nil {
			return fmt.Errorf("sqlite: read chatgpt account before removal: %w", err)
		}

		res, err := u.exec(`DELETE FROM chatgpt_accounts WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("sqlite: remove chatgpt account: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNoSuchAccount
		}
		return auditAccount(u, "chatgpt.account.removed", actor,
			tunnel.Account{ID: id, Name: name}, "remove",
			map[string]any{"principal": principal})
	})
}

// List returns every account, by name.
func (s *ChatGPTAccountStore) List(ctx context.Context) ([]tunnel.Account, error) {
	rows, err := s.db.Reader().QueryContext(ctx, `
		SELECT a.id, a.name, a.api_key, a.admin_key, a.org_id, a.workspaces, a.principal, a.role_id,
		       COALESCE(r.name, ''), COALESCE(r.permissions_json, '{}'), a.grants_json,
		       a.rate_per_sec, a.enabled, a.created_by, a.created_at, a.updated_at
		  FROM chatgpt_accounts a LEFT JOIN roles r ON r.id = a.role_id
		 ORDER BY lower(a.name)`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list chatgpt accounts: %w", err)
	}
	defer rows.Close()

	var out []tunnel.Account
	for rows.Next() {
		a, err := s.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Get returns one account.
func (s *ChatGPTAccountStore) Get(ctx context.Context, id string) (tunnel.Account, bool, error) {
	row := s.db.Reader().QueryRowContext(ctx, `
		SELECT a.id, a.name, a.api_key, a.admin_key, a.org_id, a.workspaces, a.principal, a.role_id,
		       COALESCE(r.name, ''), COALESCE(r.permissions_json, '{}'), a.grants_json,
		       a.rate_per_sec, a.enabled, a.created_by, a.created_at, a.updated_at
		  FROM chatgpt_accounts a LEFT JOIN roles r ON r.id = a.role_id
		 WHERE a.id = ?`, id)
	a, err := s.scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return tunnel.Account{}, false, nil
	}
	if err != nil {
		return tunnel.Account{}, false, err
	}
	return a, true, nil
}

// Count reports how many accounts are stored, which is what decides whether a
// deployment upgrading from the single-key arrangement still needs seeding.
func (s *ChatGPTAccountStore) Count(ctx context.Context) (int, error) {
	var n int
	err := s.db.Reader().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM chatgpt_accounts`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("sqlite: count chatgpt accounts: %w", err)
	}
	return n, nil
}

func (s *ChatGPTAccountStore) scan(sc scanner) (tunnel.Account, error) {
	var (
		a                  tunnel.Account
		apiKey             string
		adminKey, orgID    sql.NullString
		workspaces         string
		perms, grants      string
		enabled            int
		createdAt, updated int64
	)
	if err := sc.Scan(&a.ID, &a.Name, &apiKey, &adminKey, &orgID, &workspaces, &a.Principal,
		&a.RoleID, &a.RoleName, &perms, &grants, &a.RatePerSec, &enabled, &a.CreatedBy,
		&createdAt, &updated); err != nil {
		return tunnel.Account{}, err
	}
	a.Workspaces = decodeWorkspaces(workspaces)

	if s.cipher == nil {
		return tunnel.Account{}, ErrNoCipher
	}
	key, err := s.cipher.Decrypt(apiKey)
	if err != nil {
		return tunnel.Account{}, fmt.Errorf("sqlite: decrypt key for account %q: %w", a.Name, err)
	}
	a.APIKey = key
	if adminKey.Valid && adminKey.String != "" {
		admin, err := s.cipher.Decrypt(adminKey.String)
		if err != nil {
			return tunnel.Account{}, fmt.Errorf(
				"sqlite: decrypt admin key for account %q: %w", a.Name, err)
		}
		a.AdminKey = admin
	}
	a.OrgID = orgID.String
	if err := json.Unmarshal([]byte(perms), &a.Permissions); err != nil {
		return tunnel.Account{}, fmt.Errorf(
			"sqlite: decode permissions for account %q: %w", a.Name, err)
	}
	a.Grants = auth.DecodeGrants(grants)
	a.Enabled = enabled == 1
	a.CreatedAt = time.UnixMilli(createdAt)
	a.UpdatedAt = time.UnixMilli(updated)
	return a, nil
}

// roleExists refuses a role id that names nothing, inside the write. The
// roles package cannot be imported from here -- it is built on this one --
// so the one-line check is repeated rather than shared.
func roleExists(u *UnitOfWork, id string) error {
	var n int
	if err := u.queryRow(`SELECT COUNT(*) FROM roles WHERE id = ?`, id).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrNoSuchRole
	}
	return nil
}

func (s *ChatGPTAccountStore) encryptOptional(v string) (any, error) {
	if strings.TrimSpace(v) == "" {
		return nil, nil
	}
	out, err := s.cipher.Encrypt(v)
	if err != nil {
		return nil, fmt.Errorf("sqlite: encrypt account admin key: %w", err)
	}
	return out, nil
}

// auditAccount records an administrative act against an account.
//
// The audit trail rather than settings_history, for the same reason a remote
// MCP server's decisions go there: an account decides what a whole ChatGPT
// workspace may reach through this host, which is a privilege grant.
func auditAccount(u *UnitOfWork, kind, actor string, a tunnel.Account, action string, detail map[string]any) error {
	if detail == nil {
		detail = map[string]any{}
	}
	detail["account"] = a.Name
	detail["account_id"] = a.ID
	body, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("sqlite: encode audit detail for %s: %w", kind, err)
	}
	return u.appendAudit(operations.AuditEntry{
		EventID: newEventID(),
		Kind:    kind,
		Action:  action,
		Actor:   actor,
		Detail:  body,
	})
}

// nullIfEmpty keeps an absent optional column NULL rather than an empty
// string, so "not set" is one value in the database instead of two.
func nullIfEmpty(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}

// isAccountConflict reports a name or principal already taken.
//
// Both unique indexes are over an expression rather than a bare column --
// lower(name), and principal -- and modernc.org/sqlite reports an expression
// index by its own name rather than a column list, so the index names are what
// is matched. The package's isUniqueViolation takes the text to look for,
// which is why this is a wrapper rather than a second matcher.
func isAccountConflict(err error) bool {
	return isUniqueViolation(err, "ux_chatgpt_accounts_name") ||
		isUniqueViolation(err, "ux_chatgpt_accounts_principal") ||
		isUniqueViolation(err, "chatgpt_accounts.principal")
}

// encodeWorkspaces stores the list as JSON; never null, so a row written by
// this build and one migrated with the column's default read the same way.
func encodeWorkspaces(ws []string) string {
	b, err := json.Marshal(tunnel.NormalizeWorkspaces(ws))
	if err != nil {
		return "[]"
	}
	return string(b)
}

func decodeWorkspaces(raw string) []string {
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return []string{}
	}
	return tunnel.NormalizeWorkspaces(out)
}
