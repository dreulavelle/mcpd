// Package groups holds the one thing that decides what a caller may reach.
//
// A group is a name and a set of plugins. Accounts and API keys belong to
// groups, and a subject's effective grants are its own grants unioned with
// every group it belongs to -- computed by Effective, which is the only place
// in the process that computes it. That is the same arrangement Principal.Can
// has for capabilities: one function every decision goes through, so a rule
// applied there is applied everywhere.
//
// Groups deliberately carry no capabilities. Roles decide what a caller may
// *do* and groups decide what it may *reach*, and keeping the two axes apart
// is what makes either explainable -- "why can this person approve" is
// answered by their role and nothing else. A second bundle-of-rights mechanism
// beside a two-role map would be a cost with no question behind it.
package groups

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/storage/sqlite"
)

// Errors returned by this package.
var (
	// ErrNotFound reports an unknown group.
	ErrNotFound = errors.New("groups: no such group")
	// ErrDuplicateName reports a name another group already uses.
	ErrDuplicateName = errors.New("groups: a group with that name already exists")
	// ErrNoSuchMember reports a membership that is not there.
	ErrNoSuchMember = errors.New("groups: that account or key is not in this group")
)

// MaxNameRunes bounds a group name, in runes rather than bytes so that a name
// in a script whose characters cost three bytes each is not a third as long as
// one in ASCII. The schema enforces the same bound in the same units.
const MaxNameRunes = 64

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

// Subject is whatever grants are resolved for.
type Subject struct {
	Kind Kind
	ID   string
}

// User names an account as a subject.
func User(id string) Subject { return Subject{Kind: KindUser, ID: id} }

// Key names an API key as a subject.
func Key(id string) Subject { return Subject{Kind: KindKey, ID: id} }

// Group is a named set of plugin grants.
type Group struct {
	ID          string
	Name        string
	Description string
	// Plugins is the grant. Empty grants nothing; the single element
	// auth.Wildcard grants every plugin.
	Plugins   []string
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

// --- effective grants ------------------------------------------------------

// Effective returns every plugin a subject may reach.
//
// This is the one place the union is computed, and it is one statement so that
// there is nothing to keep in step. A subject's own grants and the grants of
// every group it belongs to arrive as rows; Union folds them.
//
// Everything about "default none" falls out of it rather than being asserted.
// A subject with no row and no membership produces no rows and reaches
// nothing; a group whose grant is empty contributes nothing; and a membership
// removed between two requests stops contributing on the second, because this
// runs per request rather than being frozen when a session or a key was
// issued.
func (s *Store) Effective(ctx context.Context, subject Subject) ([]string, error) {
	if !subject.Kind.Valid() || strings.TrimSpace(subject.ID) == "" {
		return []string{}, nil
	}
	rows, err := s.db.Reader().QueryContext(ctx, effectiveQuery,
		string(subject.Kind), subject.ID)
	if err != nil {
		return nil, fmt.Errorf("groups: resolve grants for %s: %w", subject.ID, err)
	}
	defer rows.Close()

	var lists [][]string
	for rows.Next() {
		var encoded string
		if err := rows.Scan(&encoded); err != nil {
			return nil, err
		}
		var list []string
		if err := json.Unmarshal([]byte(encoded), &list); err != nil {
			// One malformed row must not hand the subject somebody else's
			// reach, and it must not silently widen this one either. Refusing
			// is the safe direction: the caller turns it into a 500 and
			// nothing is granted.
			return nil, fmt.Errorf("groups: decode a grant for %s: %w", subject.ID, err)
		}
		lists = append(lists, list)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return Union(lists...), nil
}

// effectiveQuery is deliberately one statement covering both subject kinds and
// both sources of a grant. Two statements would be two places for the rule to
// live even inside one function.
const effectiveQuery = `
	SELECT plugins_json FROM users
	 WHERE ?1 = 'user' AND id = ?2
	UNION ALL
	SELECT plugins_json FROM api_keys
	 WHERE ?1 = 'key' AND id = ?2
	UNION ALL
	SELECT g.plugins_json
	  FROM groups g
	  JOIN group_members m ON m.group_id = g.id
	 WHERE (?1 = 'user' AND m.user_id = ?2)
	    OR (?1 = 'key'  AND m.key_id  = ?2)`

// Union folds grant lists into the set a subject actually reaches.
//
// The wildcard absorbs: a subject in a group granting everything reaches
// everything, and listing the named plugins beside it would be the same set
// rendered as though it were smaller. The result is sorted so that two equal
// unions compare equal -- Principal.Equal already sorts before comparing, and
// a stable order keeps a tunnel from restarting because a group was read in a
// different order.
func Union(lists ...[]string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, list := range lists {
		for _, name := range list {
			name = strings.TrimSpace(name)
			if name == auth.Wildcard {
				return []string{auth.Wildcard}
			}
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// --- groups ----------------------------------------------------------------

// CreateRequest describes a new group.
type CreateRequest struct {
	Name        string
	Description string
	// Plugins may be empty, and empty is the default. A new group grants
	// nothing until somebody says what it is for.
	Plugins []string
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
	plugins := NormalizeGrants(req.Plugins)
	encoded, err := json.Marshal(plugins)
	if err != nil {
		return nil, fmt.Errorf("groups: encode plugin grants: %w", err)
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}

	now := s.now().UnixMilli()
	err = s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		// The condition is in the statement rather than a read before it: two
		// administrators adding the same name at once would each read a table
		// without the other's row and both proceed.
		affected, err := tx.ExecAffected(`
			INSERT INTO groups (id, name, description, plugins_json,
			                    created_by, created_at, updated_at)
			SELECT ?,?,?,?,?,?,?
			 WHERE NOT EXISTS (SELECT 1 FROM groups WHERE lower(name) = lower(?))`,
			id, name, description, string(encoded), actor, now, now, name)
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
			Detail:  map[string]any{"group": id, "plugins": plugins},
		})
	})
	if isUniqueViolation(err) {
		return nil, ErrDuplicateName
	}
	if err != nil {
		return nil, err
	}
	return &Group{
		ID: id, Name: name, Description: description, Plugins: plugins,
		CreatedBy: actor,
		CreatedAt: time.UnixMilli(now).UTC(),
		UpdatedAt: time.UnixMilli(now).UTC(),
	}, nil
}

const groupColumns = `g.id, g.name, g.description, g.plugins_json,
	g.created_by, g.created_at, g.updated_at,
	(SELECT COUNT(*) FROM group_members m WHERE m.group_id = g.id)`

// List returns every group, ordered by name.
func (s *Store) List(ctx context.Context) ([]*Group, error) {
	rows, err := s.db.Reader().QueryContext(ctx,
		`SELECT `+groupColumns+` FROM groups g ORDER BY lower(g.name)`)
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
		`SELECT `+groupColumns+` FROM groups g WHERE g.id = ?`, id))
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
		`SELECT `+groupColumns+` FROM groups g WHERE lower(g.name) = lower(?)`, trimmed))
}

// UpdateRequest describes an edit. Nil fields are left alone.
type UpdateRequest struct {
	Name        *string
	Description *string
	Plugins     *[]string
}

// Update edits a group.
//
// Re-scoping a group changes what every one of its members can reach, so it is
// a privilege change and is audited as one, in the same transaction, naming
// the administrator who made it and both the old and the new grant. An entry
// carrying only the new one would leave "what did this widen" unanswerable.
func (s *Store) Update(ctx context.Context, actor, id string, req UpdateRequest) (*Group, error) {
	var name, description string
	var plugins []string
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
	if req.Plugins != nil {
		plugins = NormalizeGrants(*req.Plugins)
	}

	now := s.now().UnixMilli()
	err = s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		var wasName, wasPlugins string
		if err := tx.QueryRow(
			`SELECT name, plugins_json FROM groups WHERE id = ?`, id).
			Scan(&wasName, &wasPlugins); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
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
		if req.Plugins != nil {
			encoded, err := json.Marshal(plugins)
			if err != nil {
				return fmt.Errorf("groups: encode plugin grants: %w", err)
			}
			sets = append(sets, "plugins_json = ?")
			args = append(args, string(encoded))
			detail["plugins"] = plugins
			detail["plugins_before"] = decodeGrants(wasPlugins)
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
		if req.Plugins == nil && req.Name == nil && req.Description == nil {
			return nil
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
// The rule, and it is the one worth stating: deleting a group takes its grant
// away from everyone in it and gives nobody anything. Memberships go with it,
// members keep their own direct grants and every other group they are in, and
// nothing is stranded -- an account with no groups left is an account that
// reaches whatever it was granted directly, which is the state every account
// starts in.
//
// It is allowed rather than refused while members remain, because narrowing is
// the safe direction and a group that cannot be deleted until it is emptied is
// a group an operator empties in a hurry with no record of what it held. The
// entry names how many members lost the grant and what the grant was, so the
// question the deletion raises is answerable afterwards.
func (s *Store) Delete(ctx context.Context, actor, id string) error {
	now := s.now().UnixMilli()
	return s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		var name, plugins string
		if err := tx.QueryRow(`SELECT name, plugins_json FROM groups WHERE id = ?`, id).
			Scan(&name, &plugins); err != nil {
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
		return tx.AppendAudit(sqlite.AdminAct{
			Kind:    "group.deleted",
			Actor:   actor,
			Subject: name,
			Action:  "delete",
			Detail: map[string]any{
				"group":   id,
				"plugins": decodeGrants(plugins),
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
		SELECT `+groupColumns+`
		  FROM groups g
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
// reaches, so it is audited in the same transaction, naming the administrator
// who decided. Adding somebody who is already a member writes nothing and is
// not an error -- a trail that records non-events is one nobody reads
// carefully -- while a group or a subject that does not exist is refused.
func (s *Store) AddMember(ctx context.Context, actor, groupID string, subject Subject) error {
	if !subject.Kind.Valid() || strings.TrimSpace(subject.ID) == "" {
		return ErrNoSuchMember
	}
	now := s.now().UnixMilli()
	return s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		name, err := groupName(tx, groupID)
		if err != nil {
			return err
		}
		added, err := AddMemberTx(tx, actor, groupID, subject, now)
		if err != nil {
			return err
		}
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
	var query string
	switch subject.Kind {
	case KindUser:
		query = `
			INSERT INTO group_members (group_id, user_id, key_id, added_by, added_at)
			SELECT ?,?,NULL,?,?
			 WHERE EXISTS (SELECT 1 FROM users WHERE id = ?)
			   AND NOT EXISTS (
			       SELECT 1 FROM group_members
			        WHERE group_id = ? AND user_id = ?)`
	case KindKey:
		query = `
			INSERT INTO group_members (group_id, user_id, key_id, added_by, added_at)
			SELECT ?,NULL,?,?,?
			 WHERE EXISTS (SELECT 1 FROM api_keys WHERE id = ?)
			   AND NOT EXISTS (
			       SELECT 1 FROM group_members
			        WHERE group_id = ? AND key_id = ?)`
	default:
		return false, ErrNoSuchMember
	}
	affected, err := tx.ExecAffected(query,
		groupID, subject.ID, actor, now, subject.ID, groupID, subject.ID)
	if err != nil {
		return false, err
	}
	if affected > 0 {
		return true, nil
	}
	// Nothing was written: either the subject is not there, or it was already
	// a member. Only the first is a refusal, and telling them apart takes one
	// read.
	var already int
	column := "user_id"
	if subject.Kind == KindKey {
		column = "key_id"
	}
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM group_members WHERE group_id = ? AND `+column+` = ?`,
		groupID, subject.ID).Scan(&already); err != nil {
		return false, err
	}
	if already > 0 {
		return false, nil
	}
	return false, ErrNoSuchMember
}

// RemoveMember takes an account or a key out of a group.
//
// It takes effect on the subject's next request, because grants are resolved
// per request rather than frozen when a session or a key was issued.
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
		affected, err := tx.ExecAffected(
			`DELETE FROM group_members WHERE group_id = ? AND `+column+` = ?`,
			groupID, subject.ID)
		if err != nil {
			return err
		}
		if affected == 0 {
			return ErrNoSuchMember
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
//
// The same three rules a display name is held to, for the same reasons: it is
// rendered in a list somebody reads, it appears in the audit trail, and a
// bidirectional override or a newline in it makes it read as something it is
// not. It is deliberately not a slug -- an operator names a group after the
// team it is for, and refusing spaces or capitals would mean asking them to
// spell it in a way nobody says out loud.
func ValidateName(raw string) (string, error) {
	return ValidateLabel("groups", "group name", raw)
}

// ValidateLabel is ValidateName with the noun supplied, so that a key's name
// is held to the same rules without a second copy of them and without an error
// message sending somebody to the wrong page.
func ValidateLabel(pkg, noun, raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", fmt.Errorf("%s: a %s is required", pkg, noun)
	}
	if !utf8.ValidString(name) {
		return "", fmt.Errorf("%s: a %s must be valid UTF-8", pkg, noun)
	}
	for _, r := range name {
		switch {
		case unicode.IsControl(r):
			return "", fmt.Errorf("%s: a %s cannot contain control characters", pkg, noun)
		case unicode.Is(unicode.Cf, r):
			return "", fmt.Errorf("%s: a %s cannot contain invisible formatting characters", pkg, noun)
		}
	}
	if utf8.RuneCountInString(name) > MaxNameRunes {
		return "", fmt.Errorf("%s: a %s must be at most %d characters", pkg, noun, MaxNameRunes)
	}
	return name, nil
}

// maxDescriptionRunes bounds the line under a group's name.
const maxDescriptionRunes = 200

func validateDescription(raw string) (string, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", nil
	}
	if !utf8.ValidString(text) {
		return "", fmt.Errorf("groups: a description must be valid UTF-8")
	}
	for _, r := range text {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return "", fmt.Errorf("groups: a description cannot contain control or invisible formatting characters")
		}
	}
	if utf8.RuneCountInString(text) > maxDescriptionRunes {
		return "", fmt.Errorf("groups: a description must be at most %d characters",
			maxDescriptionRunes)
	}
	return text, nil
}

// NormalizeGrants cleans a grant list before it is stored.
//
// Empty in, empty out: a group that grants nothing is the default and must
// survive a round trip through a form that sent an empty array. A wildcard
// absorbs everything beside it, so a list is never stored in a form that
// renders as smaller than what it means.
func NormalizeGrants(list []string) []string {
	return Union(list)
}

// --- plumbing --------------------------------------------------------------

type rowScanner interface{ Scan(...any) error }

func scanGroup(row rowScanner) (*Group, error) {
	var (
		g        Group
		plugins  string
		created  int64
		updated  int64
		members  int
		describe string
	)
	err := row.Scan(&g.ID, &g.Name, &describe, &plugins,
		&g.CreatedBy, &created, &updated, &members)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	g.Description = describe
	g.Plugins = decodeGrants(plugins)
	g.CreatedAt = time.UnixMilli(created).UTC()
	g.UpdatedAt = time.UnixMilli(updated).UTC()
	g.Members = members
	return &g, nil
}

// decodeGrants reads a stored grant list. A value this build cannot parse
// reads as no grants, which is the safe direction: a group nobody can decode
// hands out nothing rather than everything.
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
