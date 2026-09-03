// Package groups holds the one place a subject's effective access is
// computed.
//
// A group is a name, a role and a set of grants. Accounts and API keys belong
// to groups, and a subject's effective access is the union of its own role
// and grants with those of every group it belongs to -- computed by Resolve,
// which is the only function in the process that computes it. That is the
// same arrangement Principal.Can has for a single permission: one function
// every decision goes through, so a rule applied there is applied everywhere.
//
// Grants add up and nothing subtracts. Adding a subject to a group gives it
// what the group has; taking it out takes that away and leaves everything
// else. There used to be a ceiling a group could impose on its members, and
// a rule that a subject's own grant beat its groups'. Both went in the same
// change, because each was a second answer to a question the other already
// answered, and "why can this person approve" had to be read twice to be
// answered once.
package groups

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/auth/roles"
	"github.com/spoked/mcpd/internal/storage/sqlite"
)

// Errors returned by this package.
var (
	// ErrNotFound reports an unknown group.
	ErrNotFound = errors.New("groups: no such group")
	// ErrLastAdmin reports a change that would leave nobody able to manage
	// access to this host: taking away the role of a group through which the
	// only administrator holds it, or removing that administrator from it.
	// There is no dashboard path back from it, since undoing the change
	// needs the permission it just took away.
	ErrLastAdmin = errors.New("groups: this would leave nobody able to manage access")
	// ErrDuplicateName reports a name another group already uses.
	ErrDuplicateName = errors.New("groups: a group with that name already exists")
	// ErrNoSuchMember reports a membership that is not there.
	ErrNoSuchMember = errors.New("groups: that account or key is not in this group")
	// ErrNoSuchRole reports a role id that names nothing.
	ErrNoSuchRole = errors.New("groups: no such role")
)

// MaxNameRunes bounds a group name. The schema enforces the same bound in
// the same units.
const MaxNameRunes = auth.MaxLabelRunes

// Kind says what a membership is for.
//
// A closed set rather than an inference from the identifier's prefix. The
// prefixes happen to be disjoint today, and a rule that depends on that is a
// rule that breaks the first time an identifier scheme changes.
type Kind string

const (
	// KindUser is an account.
	KindUser Kind = "user"
	// KindKey is an API key.
	KindKey Kind = "key"
)

// Valid reports whether k is a recognised kind.
func (k Kind) Valid() bool { return k == KindUser || k == KindKey }

// Subject is whatever access is resolved for.
type Subject struct {
	Kind Kind
	ID   string
}

// User names an account as a subject.
func User(id string) Subject { return Subject{Kind: KindUser, ID: id} }

// Key names an API key as a subject.
func Key(id string) Subject { return Subject{Kind: KindKey, ID: id} }

// Group is a named role and set of grants, handed to every member.
type Group struct {
	ID          string
	Name        string
	Description string
	// RoleID is the role every member holds through this group, or empty
	// for a group that only hands out reach. RoleName is for rendering.
	RoleID   string
	RoleName string
	// Grants is the reach every member holds through this group. Empty
	// grants nothing.
	Grants    auth.Grants
	CreatedBy string
	CreatedAt time.Time
	UpdatedAt time.Time
	// Members counts the accounts and keys in the group. Populated by List
	// and ByID, because "delete this group" is a question about who is in it.
	Members int
}

// Member is one subject's membership, with enough to render a row.
type Member struct {
	Kind Kind
	ID   string
	// Label is the address of an account or the name of a key. For reading
	// only; ID is what every guard and every grant is keyed on.
	Label   string
	AddedBy string
	AddedAt time.Time
}

// Store persists groups and their membership.
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

// --- effective access ------------------------------------------------------

// Resolved is everything a subject holds, once its groups have been read.
type Resolved struct {
	// RoleID and RoleName are the subject's own role, for rendering. The
	// permissions below already include it.
	RoleID   string
	RoleName string
	// Permissions is the union of the subject's role and every group's.
	Permissions auth.Permissions
	// Grants is the union of the subject's grants and every group's, at the
	// highest level named for each plugin.
	Grants auth.Grants
}

// Resolve returns everything a subject may do and reach.
//
// This is the one place the union is computed, and it is one statement so
// that there is nothing to keep in step. The subject's own row and the row of
// every group it belongs to arrive together, each joined to its role; the
// permissions merge and the grants union.
//
// Everything about "default none" falls out of it rather than being asserted.
// A subject with no row and no membership produces no rows and holds nothing;
// a group with no role and no grants contributes nothing; and a membership
// removed between two requests stops contributing on the second, because this
// runs per request rather than being frozen when a session or a key was
// issued. A role that no longer exists reads as no permissions -- failing
// closed -- rather than as an error, because a subject can be looked at
// even when it has been left holding nothing.
func (s *Store) Resolve(ctx context.Context, subject Subject) (Resolved, error) {
	out := Resolved{Permissions: auth.Permissions{}, Grants: auth.Grants{}}
	if !subject.Kind.Valid() || strings.TrimSpace(subject.ID) == "" {
		return out, nil
	}
	rows, err := s.db.Reader().QueryContext(ctx, resolveQuery, string(subject.Kind), subject.ID)
	if err != nil {
		return out, fmt.Errorf("groups: resolve access for %s: %w", subject.ID, err)
	}
	defer rows.Close()

	var grants []auth.Grants
	for rows.Next() {
		var isGroup int
		var roleID string
		var roleName, perms sql.NullString
		var encodedGrants string
		if err := rows.Scan(&isGroup, &roleID, &roleName, &perms, &encodedGrants); err != nil {
			return out, err
		}
		if isGroup == 0 {
			out.RoleID = roleID
			out.RoleName = roleName.String
		}
		if perms.Valid {
			var ps auth.Permissions
			if err := ps.UnmarshalJSON([]byte(perms.String)); err != nil {
				// One unreadable role must not hand the subject somebody
				// else's rights, and it must not silently widen this one
				// either. Refusing is the safe direction: the caller turns
				// it into a 500 and nothing is granted.
				return out, fmt.Errorf("groups: decode permissions of role %s: %w", roleID, err)
			}
			out.Permissions = out.Permissions.Merge(ps)
		}
		grants = append(grants, auth.DecodeGrants(encodedGrants))
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	out.Grants = auth.UnionGrants(grants...)
	return out, nil
}

// Effective returns every grant a subject holds, for the places that only
// ask about reach.
func (s *Store) Effective(ctx context.Context, subject Subject) (auth.Grants, error) {
	r, err := s.Resolve(ctx, subject)
	if err != nil {
		return nil, err
	}
	return r.Grants, nil
}

// resolveQuery is deliberately one statement covering both subject kinds and
// both sources of access. Two statements would be two places for the rule to
// live even inside one function.
const resolveQuery = `
	SELECT 0 AS is_group, s.role_id, r.name, r.permissions_json, s.grants_json
	  FROM (SELECT role_id, grants_json FROM users    WHERE ?1 = 'user' AND id = ?2
	        UNION ALL
	        SELECT role_id, grants_json FROM api_keys WHERE ?1 = 'key'  AND id = ?2) s
	  LEFT JOIN roles r ON r.id = s.role_id
	UNION ALL
	SELECT 1 AS is_group, g.role_id, r.name, r.permissions_json, g.grants_json
	  FROM groups g
	  JOIN group_members m ON m.group_id = g.id
	  LEFT JOIN roles r ON r.id = g.role_id
	 WHERE (?1 = 'user' AND m.user_id = ?2)
	    OR (?1 = 'key'  AND m.key_id  = ?2)`

// --- groups ----------------------------------------------------------------

// CreateRequest describes a new group.
type CreateRequest struct {
	Name        string
	Description string
	// RoleID may be empty: a group that hands out reach and no role.
	RoleID string
	// Grants may be empty, and empty is the default. A new group grants
	// nothing until somebody says what it is for.
	Grants auth.Grants
}

// Create makes a group.
//
// Audited, because a group is a grant waiting to be handed out and the set of
// them is what an operator reads to answer "who can reach what".
func (s *Store) Create(ctx context.Context, actor string, req CreateRequest) (*Group, error) {
	name, err := ValidateName(req.Name)
	if err != nil {
		return nil, err
	}
	description, err := validateDescription(req.Description)
	if err != nil {
		return nil, err
	}
	if err := req.Grants.Validate(); err != nil {
		return nil, err
	}
	grants := req.Grants.Normalize()
	roleID := strings.TrimSpace(req.RoleID)
	id, err := newID()
	if err != nil {
		return nil, err
	}

	now := s.now().UnixMilli()
	err = s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		if roleID != "" {
			if ok, err := roles.Exists(tx, roleID); err != nil {
				return err
			} else if !ok {
				return ErrNoSuchRole
			}
		}
		// The condition is in the statement rather than a read before it: two
		// administrators adding the same name at once would each read a table
		// without the other's row and both proceed.
		affected, err := tx.ExecAffected(`
			INSERT INTO groups (id, name, description, role_id, grants_json,
			                    created_by, created_at, updated_at)
			SELECT ?,?,?,?,?,?,?,?
			 WHERE NOT EXISTS (SELECT 1 FROM groups WHERE lower(name) = lower(?))`,
			id, name, description, roleID, auth.EncodeGrants(grants), actor, now, now, name)
		if err != nil {
			return err
		}
		if affected == 0 {
			return ErrDuplicateName
		}
		return tx.AppendAudit(sqlite.AdminAct{
			Kind:    "group.created",
			Actor:   actor,
			Subject: name,
			Action:  "create",
			Detail:  map[string]any{"group": id, "role": roleID, "grants": grants},
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

const groupColumns = `g.id, g.name, g.description, g.role_id, COALESCE(r.name, ''),
	g.grants_json, g.created_by, g.created_at, g.updated_at,
	(SELECT COUNT(*) FROM group_members m WHERE m.group_id = g.id)`

const groupFrom = ` FROM groups g LEFT JOIN roles r ON r.id = g.role_id`

// List returns every group, ordered by name.
func (s *Store) List(ctx context.Context) ([]*Group, error) {
	rows, err := s.db.Reader().QueryContext(ctx,
		`SELECT `+groupColumns+groupFrom+` ORDER BY lower(g.name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []*Group{}
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ByID loads one group.
func (s *Store) ByID(ctx context.Context, id string) (*Group, error) {
	return scanGroup(s.db.Reader().QueryRowContext(ctx,
		`SELECT `+groupColumns+groupFrom+` WHERE g.id = ?`, id))
}

// ByName loads one group by name, case-insensitively.
//
// For the registration default, which names a group an operator typed rather
// than an identifier they would have had to copy. A name that matches nothing
// is ErrNotFound and grants nothing, which is the safe direction for a setting
// left pointing at a group somebody deleted.
func (s *Store) ByName(ctx context.Context, name string) (*Group, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return nil, ErrNotFound
	}
	return scanGroup(s.db.Reader().QueryRowContext(ctx,
		`SELECT `+groupColumns+groupFrom+` WHERE lower(g.name) = lower(?)`, trimmed))
}

// UpdateRequest describes an edit. Nil fields are left alone.
type UpdateRequest struct {
	Name        *string
	Description *string
	// RoleID sets the group's role; a pointer to "" removes it.
	RoleID *string
	Grants *auth.Grants
}

// Update edits a group.
//
// Changing a group's role or grants changes what every one of its members
// holds, so it is a privilege change and is audited as one, in the same
// transaction, naming the administrator who made it and both the old and the
// new value. An entry carrying only the new one would leave "what did this
// widen" unanswerable. The last-administrator guard runs after the write and
// inside it, for the case where the only person managing access held it
// through this group's role.
func (s *Store) Update(ctx context.Context, actor, id string, req UpdateRequest) (*Group, error) {
	// An edit that changes nothing writes nothing, and that includes the
	// updated_at stamp: a group whose modification time moved without anything
	// about it changing is a row that lies about when it was last decided on.
	if req.Name == nil && req.Description == nil && req.RoleID == nil && req.Grants == nil {
		return s.ByID(ctx, id)
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
	var grants auth.Grants
	if req.Grants != nil {
		if err := req.Grants.Validate(); err != nil {
			return nil, err
		}
		grants = req.Grants.Normalize()
	}

	now := s.now().UnixMilli()
	err = s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		var wasName, wasRole, wasGrants string
		if err := tx.QueryRow(
			`SELECT name, role_id, grants_json FROM groups WHERE id = ?`, id).
			Scan(&wasName, &wasRole, &wasGrants); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		adminsBefore, err := roles.CountAdministrators(tx)
		if err != nil {
			return err
		}

		sets := []string{"updated_at = ?"}
		args := []any{now}
		guard := ""
		var guardArgs []any
		detail := map[string]any{"group": id}

		if req.Name != nil {
			sets = append(sets, "name = ?")
			args = append(args, name)
			guard = ` AND NOT EXISTS (
				SELECT 1 FROM groups other
				 WHERE other.id <> ? AND lower(other.name) = lower(?)
			)`
			guardArgs = append(guardArgs, id, name)
			detail["renamed_from"] = wasName
		}
		if req.Description != nil {
			sets = append(sets, "description = ?")
			args = append(args, description)
		}
		if req.RoleID != nil {
			roleID := strings.TrimSpace(*req.RoleID)
			if roleID != "" {
				if ok, err := roles.Exists(tx, roleID); err != nil {
					return err
				} else if !ok {
					return ErrNoSuchRole
				}
			}
			sets = append(sets, "role_id = ?")
			args = append(args, roleID)
			detail["role"] = roleID
			detail["role_before"] = wasRole
		}
		if req.Grants != nil {
			sets = append(sets, "grants_json = ?")
			args = append(args, auth.EncodeGrants(grants))
			detail["grants"] = grants
			detail["grants_before"] = auth.DecodeGrants(wasGrants)
		}
		args = append(args, id)
		args = append(args, guardArgs...)

		affected, err := tx.ExecAffected(
			`UPDATE groups SET `+strings.Join(sets, ", ")+` WHERE id = ?`+guard, args...)
		if err != nil {
			return err
		}
		if affected == 0 {
			// The row was read at the top of this transaction, so the only
			// condition that can have failed is the name guard.
			return ErrDuplicateName
		}
		// Checked after the write and inside the transaction, so the question
		// is asked of the state this change would leave rather than of a
		// guess about it, and a refusal rolls the write back.
		if err := roles.GuardAdminRemains(tx, adminsBefore); err != nil {
			return ErrLastAdmin
		}
		return tx.AppendAudit(sqlite.AdminAct{
			Kind:    "group.updated",
			Actor:   actor,
			Subject: cmpName(name, wasName),
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

// Delete removes a group.
//
// The rule, and it is the one worth stating: deleting a group takes its role
// and grants away from everyone in it and gives nobody anything. Memberships
// go with it, members keep their own role and grants and every other group
// they are in, and nothing is stranded -- except the one case the guard
// covers, where the only person managing access held it through this group.
//
// It is allowed rather than refused while members remain, because narrowing
// is the safe direction and a group that cannot be deleted until it is
// emptied is a group an operator empties in a hurry with no record of what it
// held. The entry names how many members lost the grant and what it was.
func (s *Store) Delete(ctx context.Context, actor, id string) error {
	now := s.now().UnixMilli()
	return s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		var name, roleID, grants string
		if err := tx.QueryRow(`SELECT name, role_id, grants_json FROM groups WHERE id = ?`, id).
			Scan(&name, &roleID, &grants); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		var members int
		if err := tx.QueryRow(
			`SELECT COUNT(*) FROM group_members WHERE group_id = ?`, id).
			Scan(&members); err != nil {
			return err
		}
		adminsBefore, err := roles.CountAdministrators(tx)
		if err != nil {
			return err
		}
		// Memberships cascade on the foreign key. Saying so here keeps the
		// behaviour true of a database restored without foreign keys on,
		// which is the same reason users.Delete spells out its cascades.
		if err := tx.Exec(`DELETE FROM group_members WHERE group_id = ?`, id); err != nil {
			return err
		}
		affected, err := tx.ExecAffected(`DELETE FROM groups WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if affected == 0 {
			return ErrNotFound
		}
		if err := roles.GuardAdminRemains(tx, adminsBefore); err != nil {
			return ErrLastAdmin
		}
		return tx.AppendAudit(sqlite.AdminAct{
			Kind:    "group.deleted",
			Actor:   actor,
			Subject: name,
			Action:  "delete",
			Detail: map[string]any{
				"group":   id,
				"role":    roleID,
				"grants":  auth.DecodeGrants(grants),
				"members": members,
			},
		})
	})
}

// --- membership ------------------------------------------------------------

// Members lists a group's accounts and keys.
func (s *Store) Members(ctx context.Context, groupID string) ([]Member, error) {
	rows, err := s.db.Reader().QueryContext(ctx, `
		SELECT CASE WHEN m.user_id IS NULL THEN 'key' ELSE 'user' END,
		       COALESCE(m.user_id, m.key_id),
		       COALESCE(u.email, k.name, ''),
		       m.added_by, m.added_at
		  FROM group_members m
		  LEFT JOIN users    u ON u.id = m.user_id
		  LEFT JOIN api_keys k ON k.id = m.key_id
		 WHERE m.group_id = ?
		 ORDER BY 1, 3`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Member{}
	for rows.Next() {
		var m Member
		var kind string
		var added int64
		if err := rows.Scan(&kind, &m.ID, &m.Label, &m.AddedBy, &added); err != nil {
			return nil, err
		}
		m.Kind = Kind(kind)
		m.AddedAt = time.UnixMilli(added).UTC()
		out = append(out, m)
	}
	return out, rows.Err()
}

// Of lists the groups a subject belongs to, for display beside the subject.
func (s *Store) Of(ctx context.Context, subject Subject) ([]*Group, error) {
	if !subject.Kind.Valid() {
		return []*Group{}, nil
	}
	rows, err := s.db.Reader().QueryContext(ctx, `
		SELECT `+groupColumns+groupFrom+`
		  JOIN group_members m ON m.group_id = g.id
		 WHERE (?1 = 'user' AND m.user_id = ?2)
		    OR (?1 = 'key'  AND m.key_id  = ?2)
		 ORDER BY lower(g.name)`, string(subject.Kind), subject.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []*Group{}
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// AddMember puts an account or a key into a group.
//
// A privilege grant: it is the moment a subject gains whatever the group
// holds, so it is audited in the same transaction, naming the administrator
// who decided. Adding somebody who is already a member writes nothing and is
// not an error -- a trail that records non-events is one nobody reads
// carefully -- while a group or a subject that does not exist is refused.
func (s *Store) AddMember(ctx context.Context, actor, groupID string, subject Subject) error {
	if !subject.Kind.Valid() || strings.TrimSpace(subject.ID) == "" {
		return ErrNoSuchMember
	}
	now := s.now().UnixMilli()
	return s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		return AddMemberAudited(tx, actor, groupID, subject, now)
	})
}

// AddMemberAudited inserts a membership and records it, inside a transaction
// somebody else owns.
//
// Every membership goes through here, whichever act produced it: an
// administrator adding somebody to a group, an account created straight into
// one, a key issued into one, an approval assigning one, or a registration
// joining the default. That is the point of the function existing. A trail is
// only useful for "how did this person come to reach that plugin" if it holds
// every membership, and a second path that inserted one without recording it
// would leave exactly the gap the record exists to close -- reach changed, and
// nothing says how.
func AddMemberAudited(tx *sqlite.UnitOfWork, actor, groupID string, subject Subject, now int64) error {
	name, err := groupName(tx, groupID)
	if err != nil {
		return err
	}
	added, err := AddMemberTx(tx, actor, groupID, subject, now)
	if err != nil {
		return err
	}
	// Already a member: nothing changed, so nothing is recorded. A trail that
	// carries non-events is one nobody reads carefully.
	if !added {
		return nil
	}
	return tx.AppendAudit(sqlite.AdminAct{
		Kind:    "group.member_added",
		Actor:   actor,
		Subject: name,
		Action:  "add_member",
		Detail: map[string]any{
			"group": groupID,
			"kind":  string(subject.Kind),
			"id":    subject.ID,
		},
	})
}

// AddMemberTx inserts one membership inside a transaction somebody else owns.
//
// Exported so that a registration can join its default group in the same
// transaction that creates the account, rather than in a second write that
// could land after somebody read the account and concluded it reached nothing.
// It reports whether a row was written, so the caller can decide what to
// record.
//
// The subject's existence is a condition of the insert rather than a read
// before it: an account deleted between the read and the write would otherwise
// leave a membership naming nobody.
func AddMemberTx(tx *sqlite.UnitOfWork, actor, groupID string, subject Subject, now int64) (bool, error) {
	var column, table string
	switch subject.Kind {
	case KindUser:
		column, table = "user_id", "users"
	case KindKey:
		column, table = "key_id", "api_keys"
	default:
		return false, ErrNoSuchMember
	}
	// The group's existence is a condition too, so a group deleted between a
	// page load and this write is a refusal with a sentence rather than a
	// foreign-key violation nobody can read.
	other := "key_id"
	if subject.Kind == KindKey {
		other = "user_id"
	}
	affected, err := tx.ExecAffected(`
		INSERT INTO group_members (group_id, `+column+`, `+other+`, added_by, added_at)
		SELECT ?,?,NULL,?,?
		 WHERE EXISTS (SELECT 1 FROM groups WHERE id = ?)
		   AND EXISTS (SELECT 1 FROM `+table+` WHERE id = ?)
		   AND NOT EXISTS (
		       SELECT 1 FROM group_members
		        WHERE group_id = ? AND `+column+` = ?)`,
		groupID, subject.ID, actor, now, groupID, subject.ID, groupID, subject.ID)
	if err != nil {
		return false, err
	}
	if affected > 0 {
		return true, nil
	}
	// Nothing was written, and three conditions could account for it. Which
	// one is answered by reading afterwards rather than by the write, because
	// the three need different words.
	var already int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM group_members WHERE group_id = ? AND `+column+` = ?`,
		groupID, subject.ID).Scan(&already); err != nil {
		return false, err
	}
	if already > 0 {
		return false, nil
	}
	if _, err := groupName(tx, groupID); err != nil {
		return false, err
	}
	return false, ErrNoSuchMember
}

// RemoveMemberAudited deletes a membership and records it, inside a
// transaction somebody else owns. The counterpart of AddMemberAudited, for
// the same reason: a key whose edit form sets its groups removes some in the
// same write that adds others, and the trail has to hold both. Removing a
// membership that is not there writes nothing and is not an error.
func RemoveMemberAudited(tx *sqlite.UnitOfWork, actor, groupID string, subject Subject) error {
	if !subject.Kind.Valid() {
		return ErrNoSuchMember
	}
	column := "user_id"
	if subject.Kind == KindKey {
		column = "key_id"
	}
	name, err := groupName(tx, groupID)
	if err != nil {
		return err
	}
	affected, err := tx.ExecAffected(
		`DELETE FROM group_members WHERE group_id = ? AND `+column+` = ?`,
		groupID, subject.ID)
	if err != nil {
		return err
	}
	if affected == 0 {
		return nil
	}
	return tx.AppendAudit(sqlite.AdminAct{
		Kind:    "group.member_removed",
		Actor:   actor,
		Subject: name,
		Action:  "remove_member",
		Detail: map[string]any{
			"group": groupID,
			"kind":  string(subject.Kind),
			"id":    subject.ID,
		},
	})
}

// RemoveMember takes an account or a key out of a group.
//
// It takes effect on the subject's next request, because access is resolved
// per request rather than frozen when a session or a key was issued. Refused
// where the person being removed is the only one managing access and holds
// that through this group.
func (s *Store) RemoveMember(ctx context.Context, actor, groupID string, subject Subject) error {
	if !subject.Kind.Valid() {
		return ErrNoSuchMember
	}
	column := "user_id"
	if subject.Kind == KindKey {
		column = "key_id"
	}
	now := s.now().UnixMilli()
	return s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		name, err := groupName(tx, groupID)
		if err != nil {
			return err
		}
		adminsBefore, err := roles.CountAdministrators(tx)
		if err != nil {
			return err
		}
		affected, err := tx.ExecAffected(
			`DELETE FROM group_members WHERE group_id = ? AND `+column+` = ?`,
			groupID, subject.ID)
		if err != nil {
			return err
		}
		if affected == 0 {
			return ErrNoSuchMember
		}
		if err := roles.GuardAdminRemains(tx, adminsBefore); err != nil {
			return ErrLastAdmin
		}
		return tx.AppendAudit(sqlite.AdminAct{
			Kind:    "group.member_removed",
			Actor:   actor,
			Subject: name,
			Action:  "remove_member",
			Detail: map[string]any{
				"group": groupID,
				"kind":  string(subject.Kind),
				"id":    subject.ID,
			},
		})
	})
}

// --- validation ------------------------------------------------------------

// ValidateName checks and normalises a group name.
func ValidateName(raw string) (string, error) {
	return auth.ValidateLabel("groups", "group name", raw)
}

// ValidateLabel is kept for the packages that validated their names through
// this one before the rule moved to auth.
func ValidateLabel(pkg, noun, raw string) (string, error) {
	return auth.ValidateLabel(pkg, noun, raw)
}

// maxDescriptionRunes bounds the line under a group's name.
const maxDescriptionRunes = 200

func validateDescription(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	return auth.ValidateText("groups", "description", raw, maxDescriptionRunes)
}

// --- plumbing --------------------------------------------------------------

type rowScanner interface{ Scan(...any) error }

func scanGroup(row rowScanner) (*Group, error) {
	var (
		g        Group
		grants   string
		created  int64
		updated  int64
		members  int
		describe string
	)
	err := row.Scan(&g.ID, &g.Name, &describe, &g.RoleID, &g.RoleName, &grants,
		&g.CreatedBy, &created, &updated, &members)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	g.Description = describe
	g.Grants = auth.DecodeGrants(grants)
	g.CreatedAt = time.UnixMilli(created).UTC()
	g.UpdatedAt = time.UnixMilli(updated).UTC()
	g.Members = members
	return &g, nil
}

func groupName(tx *sqlite.UnitOfWork, id string) (string, error) {
	var name string
	if err := tx.QueryRow(`SELECT name FROM groups WHERE id = ?`, id).Scan(&name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	return name, nil
}

func cmpName(next, was string) string {
	if next != "" {
		return next
	}
	return was
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// newID returns a random identifier for a group. Random rather than derived
// from the name, so that renaming a group does not change the thing every
// membership row and every audit entry points at.
func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("groups: system entropy unavailable: %w", err)
	}
	return "grp_" + hex.EncodeToString(b), nil
}
