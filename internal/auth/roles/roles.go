// Package roles persists the named permission sets a subject can hold.
//
// Three roles are built in and belong to the binary: their permissions are
// what auth.BuiltinRoles says, re-applied at every startup, and the store
// refuses to edit or delete them. Everything else is an operator's, composed
// from the same vocabulary, and can be changed or removed like any other
// record -- removed only when nothing holds it, because a subject pointing
// at a role that no longer exists would hold nothing and nobody would have
// decided that.
//
// This package also owns the one query that answers "does anybody still
// administer this host". It is here rather than beside the users because the
// answer depends on roles -- a person administers when their own role or one
// of their groups' roles holds access at write -- and three copies of that
// rule in three packages is how the last-administrator guard used to drift.
package roles

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/storage/sqlite"
)

// Errors returned by this package.
var (
	// ErrNotFound reports an unknown role.
	ErrNotFound = errors.New("roles: no such role")
	// ErrBuiltin reports an attempt to edit or delete a role the binary
	// defines. Their meaning has to be the same on every host.
	ErrBuiltin = errors.New("roles: a built-in role cannot be changed")
	// ErrAssigned reports deleting a role something still holds.
	ErrAssigned = errors.New("roles: that role is still assigned")
	// ErrDuplicateName reports a name another role already uses.
	ErrDuplicateName = errors.New("roles: a role with that name already exists")
	// ErrLastAdmin reports a change that would leave nobody able to manage
	// access to this host. There is no dashboard path back from it, since
	// undoing the change needs the permission it just took away.
	ErrLastAdmin = errors.New("roles: this would leave nobody able to manage access")
)

// Store persists roles.
type Store struct {
	db  *sqlite.DB
	now func() time.Time
}

// NewStore returns a store backed by db.
func NewStore(db *sqlite.DB, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, now: now}
}

// EnsureBuiltins writes the built-in roles as the binary defines them.
//
// Run at startup, before anything resolves a permission. An upsert rather
// than an insert-if-missing, so that a permission area added in a later
// version reaches every administrator without a migration and without
// anybody editing a role -- the same reason Grafana re-attaches new
// permissions to its basic roles. Names and descriptions are re-applied too;
// the built-ins are not the operator's to rename.
func (s *Store) EnsureBuiltins(ctx context.Context) error {
	now := s.now().UnixMilli()
	return s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		for _, r := range auth.BuiltinRoles() {
			encoded, err := json.Marshal(r.Permissions)
			if err != nil {
				return err
			}
			if err := tx.Exec(`
				INSERT INTO roles (id, name, description, builtin, permissions_json,
				                   created_by, created_at, updated_at)
				VALUES (?,?,?,1,?,'system',?,?)
				ON CONFLICT(id) DO UPDATE SET
				    name = excluded.name,
				    description = excluded.description,
				    builtin = 1,
				    permissions_json = excluded.permissions_json,
				    updated_at = CASE WHEN roles.permissions_json = excluded.permissions_json
				                      THEN roles.updated_at ELSE excluded.updated_at END`,
				r.ID, r.Name, r.Description, string(encoded), now, now); err != nil {
				return fmt.Errorf("roles: ensure %s: %w", r.ID, err)
			}
		}
		return nil
	})
}

// roleColumns is every column a role renders with, plus how many subjects
// hold it. Revoked keys are not counted: a role only a dead key holds is one
// nothing is using.
const roleColumns = `r.id, r.name, r.description, r.builtin, r.permissions_json,
	r.created_by, r.created_at, r.updated_at,
	(SELECT COUNT(*) FROM users u WHERE u.role_id = r.id)
	+ (SELECT COUNT(*) FROM api_keys k WHERE k.role_id = r.id AND k.revoked_at IS NULL)
	+ (SELECT COUNT(*) FROM chatgpt_accounts a WHERE a.role_id = r.id)
	+ (SELECT COUNT(*) FROM groups g WHERE g.role_id = r.id)`

// List returns every role: the built-ins first in their own order, then
// custom roles by name.
func (s *Store) List(ctx context.Context) ([]*auth.Role, error) {
	rows, err := s.db.Reader().QueryContext(ctx, `
		SELECT `+roleColumns+` FROM roles r
		 ORDER BY r.builtin DESC,
		          CASE r.id WHEN ? THEN 0 WHEN ? THEN 1 WHEN ? THEN 2 ELSE 3 END,
		          lower(r.name)`,
		auth.RoleReader, auth.RoleOperator, auth.RoleAdministrator)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*auth.Role{}
	for rows.Next() {
		r, err := scanRole(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ByID loads one role.
func (s *Store) ByID(ctx context.Context, id string) (*auth.Role, error) {
	return scanRole(s.db.Reader().QueryRowContext(ctx,
		`SELECT `+roleColumns+` FROM roles r WHERE r.id = ?`, id))
}

// ByName loads one role by name, case-insensitively. For a configuration
// file, which names a role the way a person says it rather than by an
// identifier they would have had to copy.
func (s *Store) ByName(ctx context.Context, name string) (*auth.Role, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return nil, ErrNotFound
	}
	return scanRole(s.db.Reader().QueryRowContext(ctx,
		`SELECT `+roleColumns+` FROM roles r WHERE lower(r.name) = lower(?)`, trimmed))
}

// Exists reports whether a role id names a row, inside a transaction
// somebody else owns. For the stores that assign roles, so that an
// assignment to a role that does not exist is refused where it is made.
func Exists(tx *sqlite.UnitOfWork, id string) (bool, error) {
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM roles WHERE id = ?`, id).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// CreateRequest describes a new role.
type CreateRequest struct {
	Name        string
	Description string
	Permissions auth.Permissions
}

// Create makes a custom role.
//
// Audited, because a role is a set of rights waiting to be handed out and
// the set of them is what an operator reads to answer "what can this person
// do".
func (s *Store) Create(ctx context.Context, actor string, req CreateRequest) (*auth.Role, error) {
	name, err := ValidateName(req.Name)
	if err != nil {
		return nil, err
	}
	description, err := validateDescription(req.Description)
	if err != nil {
		return nil, err
	}
	if err := req.Permissions.Validate(); err != nil {
		return nil, err
	}
	perms := req.Permissions.Normalize()
	encoded, err := json.Marshal(perms)
	if err != nil {
		return nil, err
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}

	now := s.now().UnixMilli()
	err = s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		// The condition is in the statement rather than a read before it:
		// two administrators adding the same name at once would each read a
		// table without the other's row and both proceed.
		affected, err := tx.ExecAffected(`
			INSERT INTO roles (id, name, description, builtin, permissions_json,
			                   created_by, created_at, updated_at)
			SELECT ?,?,?,0,?,?,?,?
			 WHERE NOT EXISTS (SELECT 1 FROM roles WHERE lower(name) = lower(?))`,
			id, name, description, string(encoded), actor, now, now, name)
		if err != nil {
			return err
		}
		if affected == 0 {
			return ErrDuplicateName
		}
		return tx.AppendAudit(sqlite.AdminAct{
			Kind:    "role.created",
			Actor:   actor,
			Subject: name,
			Action:  "create",
			Detail:  map[string]any{"role": id, "permissions": perms},
		})
	})
	if isUniqueViolation(err) {
		return nil, ErrDuplicateName
	}
	if err != nil {
		return nil, err
	}
	return s.ByID(ctx, id)
}

// UpdateRequest describes an edit. Nil fields are left alone.
type UpdateRequest struct {
	Name        *string
	Description *string
	Permissions *auth.Permissions
}

// Update edits a custom role.
//
// Changing a role's permissions changes what everything holding it may do,
// so it is a privilege change and is audited as one, in the same
// transaction, with the permissions before as well as after. The
// last-administrator guard runs after the write and inside it: if this edit
// takes access away from the last person managing it, the write rolls back.
func (s *Store) Update(ctx context.Context, actor, id string, req UpdateRequest) (*auth.Role, error) {
	if req.Name == nil && req.Description == nil && req.Permissions == nil {
		return s.ByID(ctx, id)
	}
	if auth.IsBuiltinRole(id) {
		return nil, ErrBuiltin
	}
	var name, description string
	var err error
	if req.Name != nil {
		if name, err = ValidateName(*req.Name); err != nil {
			return nil, err
		}
	}
	if req.Description != nil {
		if description, err = validateDescription(*req.Description); err != nil {
			return nil, err
		}
	}
	var perms auth.Permissions
	if req.Permissions != nil {
		if err := req.Permissions.Validate(); err != nil {
			return nil, err
		}
		perms = req.Permissions.Normalize()
	}

	now := s.now().UnixMilli()
	err = s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		var wasName, wasPerms string
		var builtin int
		if err := tx.QueryRow(
			`SELECT name, permissions_json, builtin FROM roles WHERE id = ?`, id).
			Scan(&wasName, &wasPerms, &builtin); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if builtin == 1 {
			return ErrBuiltin
		}
		adminsBefore, err := CountAdministrators(tx)
		if err != nil {
			return err
		}

		sets := []string{"updated_at = ?"}
		args := []any{now}
		guard := ""
		var guardArgs []any
		detail := map[string]any{"role": id}
		if req.Name != nil {
			sets = append(sets, "name = ?")
			args = append(args, name)
			guard = ` AND NOT EXISTS (
				SELECT 1 FROM roles other WHERE other.id <> ? AND lower(other.name) = lower(?))`
			guardArgs = append(guardArgs, id, name)
			detail["renamed_from"] = wasName
		}
		if req.Description != nil {
			sets = append(sets, "description = ?")
			args = append(args, description)
		}
		if req.Permissions != nil {
			encoded, err := json.Marshal(perms)
			if err != nil {
				return err
			}
			sets = append(sets, "permissions_json = ?")
			args = append(args, string(encoded))
			detail["permissions"] = perms
			detail["permissions_before"] = json.RawMessage(wasPerms)
		}
		args = append(args, id)
		args = append(args, guardArgs...)
		affected, err := tx.ExecAffected(
			`UPDATE roles SET `+strings.Join(sets, ", ")+` WHERE id = ? AND builtin = 0`+guard, args...)
		if err != nil {
			return err
		}
		if affected == 0 {
			return ErrDuplicateName
		}
		if err := GuardAdminRemains(tx, adminsBefore); err != nil {
			return err
		}
		subject := wasName
		if name != "" {
			subject = name
		}
		return tx.AppendAudit(sqlite.AdminAct{
			Kind:    "role.updated",
			Actor:   actor,
			Subject: subject,
			Action:  "update",
			Detail:  detail,
		})
	})
	if isUniqueViolation(err) {
		return nil, ErrDuplicateName
	}
	if err != nil {
		return nil, err
	}
	return s.ByID(ctx, id)
}

// Delete removes a custom role nothing holds.
//
// Refused while assigned, unlike a group, because the two failures differ.
// Deleting a group takes reach away, which is the safe direction. Deleting a
// role something holds leaves that thing pointing at nothing -- it would
// hold no permission at all, and nobody decided that it should.
func (s *Store) Delete(ctx context.Context, actor, id string) error {
	if auth.IsBuiltinRole(id) {
		return ErrBuiltin
	}
	now := s.now().UnixMilli()
	return s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		var name string
		var builtin int
		if err := tx.QueryRow(`SELECT name, builtin FROM roles WHERE id = ?`, id).
			Scan(&name, &builtin); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if builtin == 1 {
			return ErrBuiltin
		}
		// The condition is the statement's: a key issued into this role
		// between the count and the delete would otherwise be left holding
		// nothing.
		affected, err := tx.ExecAffected(`
			DELETE FROM roles WHERE id = ? AND builtin = 0
			   AND NOT EXISTS (SELECT 1 FROM users WHERE role_id = ?)
			   AND NOT EXISTS (SELECT 1 FROM api_keys WHERE role_id = ? AND revoked_at IS NULL)
			   AND NOT EXISTS (SELECT 1 FROM chatgpt_accounts WHERE role_id = ?)
			   AND NOT EXISTS (SELECT 1 FROM groups WHERE role_id = ?)`,
			id, id, id, id, id)
		if err != nil {
			return err
		}
		if affected == 0 {
			return ErrAssigned
		}
		return tx.AppendAudit(sqlite.AdminAct{
			Kind:    "role.deleted",
			Actor:   actor,
			Subject: name,
			Action:  "delete",
			Detail:  map[string]any{"role": id},
		})
	})
}

// --- the last administrator ------------------------------------------------

// CountAdministrators counts the enabled, active accounts that can manage
// access to this host: those whose own role, or one of whose groups' roles,
// holds access at write.
//
// The one place the rule is written in SQL, used by every guard that has to
// ask it inside a transaction. The rule behind every exclusion is the same:
// a subject who cannot sign in cannot put things right, so it is not what
// stands between this host and nobody being able to administer it. Keys are
// excluded for that reason, and a pending account holds nothing whatever its
// row says.
//
// An unclaimed invitation is excluded for the same reason, and it is the one
// that is easy to miss because the row looks like an ordinary administrator:
// it has the role, it is enabled, and it is active. What it has not got is a
// credential -- the password is the sentinel and no identity has been linked
// -- so nobody has ever signed in to it and nobody may be able to. Counting it
// let the last person who actually holds the role demote or delete themselves,
// with the guard reporting an administrator who is an address somebody typed
// on the Users page and an invitation that may already have lapsed.
func CountAdministrators(tx *sqlite.UnitOfWork) (int, error) {
	var n int
	err := tx.QueryRow(`
		SELECT COUNT(*) FROM users u
		 WHERE u.disabled = 0 AND u.status <> 'pending'
		   AND u.invite_provider = ''
		   AND (
		     EXISTS (SELECT 1 FROM roles r
		              WHERE r.id = u.role_id
		                AND json_extract(r.permissions_json, '$.access') = 'write')
		     OR EXISTS (SELECT 1 FROM group_members m
		                  JOIN groups g ON g.id = m.group_id
		                  JOIN roles r ON r.id = g.role_id
		                 WHERE m.user_id = u.id
		                   AND json_extract(r.permissions_json, '$.access') = 'write')
		   )`).Scan(&n)
	return n, err
}

// GuardAdminRemains refuses the enclosing transaction if somebody could
// manage access before the write and, as things now stand in it, nobody can.
// Run after the write, so that the state judged is the one the change would
// leave, and a refusal rolls it back.
//
// The comparison rather than a bare "at least one" is what lets a host that
// has no administrator yet -- a fresh database, a test fixture -- change
// roles freely: nothing was taken away, because nobody had it.
func GuardAdminRemains(tx *sqlite.UnitOfWork, before int) error {
	if before == 0 {
		return nil
	}
	after, err := CountAdministrators(tx)
	if err != nil {
		return err
	}
	if after == 0 {
		return ErrLastAdmin
	}
	return nil
}

// --- validation and plumbing -----------------------------------------------

// ValidateName checks and normalises a role name.
func ValidateName(raw string) (string, error) {
	return auth.ValidateLabel("role name", raw)
}

const maxDescriptionRunes = 200

func validateDescription(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	return auth.ValidateText("description", raw, maxDescriptionRunes)
}

type rowScanner interface{ Scan(...any) error }

func scanRole(row rowScanner) (*auth.Role, error) {
	var (
		r        auth.Role
		builtin  int
		perms    string
		created  int64
		updated  int64
		assigned int
	)
	err := row.Scan(&r.ID, &r.Name, &r.Description, &builtin, &perms,
		&r.CreatedBy, &created, &updated, &assigned)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r.Builtin = builtin == 1
	if err := json.Unmarshal([]byte(perms), &r.Permissions); err != nil {
		// A row this build cannot read holds nothing. Failing closed: the
		// alternative is guessing what an unreadable set of rights meant.
		r.Permissions = auth.Permissions{}
	}
	r.CreatedAt = time.UnixMilli(created).UTC()
	r.UpdatedAt = time.UnixMilli(updated).UTC()
	r.Assigned = assigned
	return &r, nil
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// newID returns a random identifier. Random rather than derived from the
// name, so that renaming a role does not change the thing every subject
// row and every audit entry points at. Hex only, so it can never collide
// with a built-in id.
func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("roles: system entropy unavailable: %w", err)
	}
	return "role_" + hex.EncodeToString(b), nil
}
