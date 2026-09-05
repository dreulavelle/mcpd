package users

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/auth/groups"
	"github.com/spoked/mcpd/internal/auth/roles"
	"github.com/spoked/mcpd/internal/storage/sqlite"
)

// Store persists accounts and sessions. Passwords are held as bcrypt hashes
// and session tokens as SHA-256 digests, so a database leak yields neither a
// usable password nor a usable session.
type Store struct {
	db     *sqlite.DB
	groups *groups.Store
	now    func() time.Time
}

// NewStore returns a store backed by db.
func NewStore(db *sqlite.DB, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, groups: groups.NewStore(db, now), now: now}
}

// Resolve returns everything an account may do and reach.
//
// A thin pass-through, and deliberately not an implementation. The union lives
// in one function in one package; this exists so that the dashboard's Accounts
// interface can ask for it without depending on the groups package, and so
// that there is a single obvious answer to "where does an account's access
// come from" beside the account store.
func (s *Store) Resolve(ctx context.Context, userID string) (groups.Resolved, error) {
	return s.groups.Resolve(ctx, groups.User(userID))
}

const userColumns = `id, email, password_hash, display_name, role_id, grants_json,
	                 disabled, status, invite_provider, invite_expires_at,
	                 created_at, updated_at, last_login_at`

// --- accounts --------------------------------------------------------------

// InviteTTL is how long an invitation stays claimable.
//
// Long enough for somebody starting on a Monday to arrive, short enough that a
// row nobody used stops being a way in. Re-inviting is saving the account
// again, which is one control on a page rather than a second concept.
const InviteTTL = 14 * 24 * time.Hour

// CreateRequest describes a new account.
type CreateRequest struct {
	Email string
	// Password is what the account signs in with, and is required unless
	// InviteProvider names a provider instead.
	Password    string
	DisplayName string
	RoleID      string
	Grants      auth.Grants
	// InviteProvider makes this an invited account: no password of its own,
	// claimed by the first verified sign-in through that provider. An
	// administrator asserting the address in advance is the second acceptable
	// proof of ownership -- see Store.ClaimInvite -- and it is the only reason
	// an account may be created with no credential at all.
	InviteProvider Provider
	// Groups the account joins, in the same transaction as it is created. An
	// account created into a group and an account granted plugins directly are
	// the same act from the administrator's side, so they are one write.
	Groups []string
	// Actor names who is creating the account, for the membership rows.
	Actor string
}

// Create provisions an account.
func (s *Store) Create(ctx context.Context, req CreateRequest) (*User, error) {
	email, err := NormalizeEmail(req.Email)
	if err != nil {
		return nil, err
	}
	roleID := strings.TrimSpace(req.RoleID)
	if roleID == "" {
		return nil, errors.New("a role is required")
	}
	if err := req.Grants.Validate(); err != nil {
		return nil, err
	}
	// An empty direct grant used to be refused, on the grounds that it was
	// almost never what somebody meant to type. That was true while a direct
	// grant was the only kind: an account with none reached nothing and there
	// was no other way for it to reach anything.
	//
	// Groups changed the fact rather than the rule. An account whose reach
	// comes from a group is created with nothing of its own and is the
	// ordinary case, so refusing it here would refuse the arrangement this
	// exists to make possible. Empty still denies everything -- the reading a
	// principal has always taken of one -- and the Users page says "Nothing"
	// until something grants it.
	displayName, err := ValidateDisplayName(req.DisplayName)
	if err != nil {
		return nil, err
	}
	// An invitation stands in place of a password, and it has to be one or the
	// other: an account holding both would be one whose invitation can still
	// be claimed by whoever holds the address, after somebody was given a
	// password for it.
	hash := NoPassword
	if req.InviteProvider != "" {
		if !req.InviteProvider.Valid() {
			return nil, fmt.Errorf("users: %q is not a provider this build knows",
				req.InviteProvider)
		}
		if req.Password != "" {
			return nil, errors.New(
				"an invited account signs in with the provider, so it takes no password")
		}
	} else if hash, err = HashPassword(req.Password); err != nil {
		return nil, err
	}
	id, err := newID("usr_")
	if err != nil {
		return nil, err
	}

	u := &User{
		ID:             id,
		Email:          email,
		PasswordHash:   hash,
		DisplayName:    displayName,
		RoleID:         roleID,
		Grants:         req.Grants.Normalize(),
		InviteProvider: req.InviteProvider,
	}

	now := s.now().UnixMilli()
	var inviteExpires any
	if u.Invited() {
		expires := s.now().Add(InviteTTL)
		u.InviteExpiresAt = &expires
		inviteExpires = expires.UnixMilli()
	}
	err = s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		// The role has to exist, and the check is inside the transaction so
		// that a role deleted between the form and the write is a refusal
		// with a sentence rather than an account holding nothing.
		if ok, err := roles.Exists(tx, roleID); err != nil {
			return err
		} else if !ok {
			return ErrNoSuchRole
		}
		// Both directions of the same rule: the new address must not already
		// be somebody's display name, and the new display name must not be
		// somebody's address. The two are rendered in the same places, and a
		// list of who did what stops being one the moment two rows read as
		// the same person.
		//
		// Conditions in the statement rather than checks before it. Two
		// administrators adding accounts at once would each read a table
		// without the other's row and both proceed.
		affected, err := tx.ExecAffected(`
			INSERT INTO users (id, email, password_hash, display_name, role_id,
			                   grants_json, disabled, invite_provider,
			                   invite_expires_at, created_at, updated_at)
			SELECT ?,?,?,?,?,?,0,?,?,?,?
			WHERE NOT EXISTS (
				SELECT 1 FROM users WHERE lower(display_name) = ?
			)
			AND (? = '' OR NOT EXISTS (
				SELECT 1 FROM users WHERE email = lower(?)
			))`,
			u.ID, u.Email, u.PasswordHash, u.DisplayName, u.RoleID,
			auth.EncodeGrants(u.Grants), string(u.InviteProvider), inviteExpires,
			now, now,
			u.Email, u.DisplayName, u.DisplayName)
		if err != nil {
			return err
		}
		if affected == 0 {
			return ErrNameCollides
		}
		actor := req.Actor
		if actor == "" {
			actor = "system"
		}
		// Recorded, not merely inserted. An account created into a group
		// reaches whatever that group reaches from the moment this commits,
		// and a membership written without an entry would leave "how did this
		// person come to reach that plugin" answerable only by inference. It
		// is the same entry the Groups page produces, because one membership
		// fact should read the same however it arose.
		for _, groupID := range req.Groups {
			if err := groups.AddMemberAudited(tx, actor, groupID,
				groups.User(u.ID), now); err != nil {
				return err
			}
		}
		return nil
	})
	if isUniqueViolation(err) {
		return nil, ErrDuplicateEmail
	}
	if err != nil {
		return nil, err
	}
	u.CreatedAt = time.UnixMilli(now).UTC()
	u.UpdatedAt = u.CreatedAt
	return u, nil
}

// CreateFirst provisions the first account, and only when there are none.
//
// This is what backs the registration form a new instance shows instead of a
// sign-in form. The emptiness check runs inside the write transaction: two
// browsers reaching an unclaimed instance at the same moment would otherwise
// both see zero accounts and both be made administrator, which is the one
// outcome this endpoint must not have.
//
// The first account is always an administrator. There is nobody to grant it
// the role afterwards, and an instance whose first account cannot manage
// accounts is one nobody can finish setting up.
//
// It is also granted the wildcard, and that is not the breach of default-none
// it resembles. The claimant is this host's administrator with nobody above
// them, and an administrator can grant themselves any plugin whenever they
// like -- so the wildcard changes no security property, and withholding it
// would only mean the first person to arrive sees an empty console until they
// have granted themselves access to look at it. Default none is a rule about
// principals somebody else decides for: a new group, an account an
// administrator creates, a key, a self-registration. This is none of those.
func (s *Store) CreateFirst(ctx context.Context, email, password, displayName string) (*User, error) {
	normalized, err := NormalizeEmail(email)
	if err != nil {
		return nil, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}
	id, err := newID("usr_")
	if err != nil {
		return nil, err
	}

	name, err := ValidateDisplayName(displayName)
	if err != nil {
		return nil, err
	}

	u := &User{
		ID:           id,
		Email:        normalized,
		PasswordHash: hash,
		DisplayName:  name,
		RoleID:       auth.RoleAdministrator,
		Grants:       auth.GrantsAt([]string{auth.Wildcard}, auth.LevelWrite),
	}

	now := s.now().UnixMilli()
	err = s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrAlreadyClaimed
		}
		return tx.Exec(`
			INSERT INTO users (id, email, password_hash, display_name, role_id,
			                   grants_json, disabled, created_at, updated_at)
			VALUES (?,?,?,?,?,?,0,?,?)`,
			u.ID, u.Email, u.PasswordHash, u.DisplayName, u.RoleID,
			auth.EncodeGrants(u.Grants), now, now)
	})
	if err != nil {
		return nil, err
	}
	u.CreatedAt = time.UnixMilli(now).UTC()
	u.UpdatedAt = u.CreatedAt
	return u, nil
}

// ByEmail loads an account by address. The address is normalised first, so a
// caller may pass whatever the sign-in form collected.
func (s *Store) ByEmail(ctx context.Context, email string) (*User, error) {
	normalized, err := NormalizeEmail(email)
	if err != nil {
		return nil, ErrNotFound
	}
	return s.scanUser(s.db.Reader().QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE email = ?`, normalized))
}

// ByID loads an account by identifier.
func (s *Store) ByID(ctx context.Context, id string) (*User, error) {
	return s.scanUser(s.db.Reader().QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE id = ?`, id))
}

// List returns every account, for the dashboard.
func (s *Store) List(ctx context.Context) ([]*User, error) {
	rows, err := s.db.Reader().QueryContext(ctx,
		`SELECT `+userColumns+` FROM users ORDER BY email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*User
	for rows.Next() {
		u, err := s.scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// Count reports how many accounts exist.
//
// Zero is what makes an instance unclaimed: the dashboard offers to create the
// first account rather than asking for a sign-in nobody can complete.
func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	err := s.db.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// UpdateRequest describes an edit. Nil fields are left alone, which is what
// lets the dashboard send only what changed.
type UpdateRequest struct {
	DisplayName *string
	RoleID      *string
	Grants      *auth.Grants
	Disabled    *bool
}

// Update edits an account.
//
// The last-administrator guard runs inside the write transaction. Checking
// first and writing after would let two concurrent edits each observe the
// other's administrator and both proceed, leaving a host no one can administer.
func (s *Store) Update(ctx context.Context, id string, req UpdateRequest) (*User, error) {
	if req.RoleID != nil && strings.TrimSpace(*req.RoleID) == "" {
		return nil, errors.New("a role is required")
	}
	if req.Grants != nil {
		if err := req.Grants.Validate(); err != nil {
			return nil, err
		}
	}
	// An empty direct grant is legitimate; see Create. It denies everything on
	// its own, and a group is how such an account reaches anything.
	var displayName string
	if req.DisplayName != nil {
		validated, err := ValidateDisplayName(*req.DisplayName)
		if err != nil {
			return nil, err
		}
		displayName = validated
	}

	now := s.now().UnixMilli()
	err := s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		var exists int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM users WHERE id = ?`, id).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return ErrNotFound
		}
		// Whether this edit strands the host is judged by comparing who can
		// manage access before the write with who can after, inside the
		// transaction, so that two concurrent edits cannot each observe the
		// other's administrator and both proceed.
		adminsBefore, err := roles.CountAdministrators(tx)
		if err != nil {
			return err
		}
		if req.RoleID != nil {
			if ok, err := roles.Exists(tx, strings.TrimSpace(*req.RoleID)); err != nil {
				return err
			} else if !ok {
				return ErrNoSuchRole
			}
		}

		sets := []string{"updated_at = ?"}
		args := []any{now}
		// A guard that only applies when a name is being set. It goes into
		// the WHERE clause below rather than into a check up here, so that
		// two accounts racing for the same name produce one write and one
		// refusal instead of two writes.
		guard := ""
		var guardArgs []any
		if req.DisplayName != nil {
			sets = append(sets, "display_name = ?")
			args = append(args, displayName)
			if displayName != "" {
				guard = ` AND NOT EXISTS (
					SELECT 1 FROM users other
					WHERE other.id <> ? AND other.email = lower(?)
				)`
				guardArgs = append(guardArgs, id, displayName)
			}
		}
		if req.RoleID != nil {
			sets = append(sets, "role_id = ?")
			args = append(args, strings.TrimSpace(*req.RoleID))
		}
		if req.Grants != nil {
			sets = append(sets, "grants_json = ?")
			args = append(args, auth.EncodeGrants(*req.Grants))
		}
		if req.Disabled != nil {
			sets = append(sets, "disabled = ?")
			args = append(args, boolInt(*req.Disabled))
		}
		args = append(args, id)
		args = append(args, guardArgs...)
		affected, err := tx.ExecAffected(
			`UPDATE users SET `+strings.Join(sets, ", ")+` WHERE id = ?`+guard, args...)
		if err != nil {
			return err
		}
		if affected == 0 {
			// The row is there -- it was read at the top of this transaction
			// -- so the only condition that can have failed is the guard.
			return ErrNameCollides
		}
		if err := roles.GuardAdminRemains(tx, adminsBefore); err != nil {
			return ErrLastAdmin
		}

		// A disabled account must not keep browsing on a session it already
		// holds, and a demoted one must not keep the rights its old session
		// was resolved with. Sessions carry no capabilities themselves -- they
		// are re-resolved per request -- so only the disable case strictly
		// needs this, but dropping them on any privilege change keeps the rule
		// simple: an edit takes effect immediately, everywhere.
		if req.Disabled != nil || req.RoleID != nil || req.Grants != nil {
			return tx.Exec(`DELETE FROM user_sessions WHERE user_id = ?`, id)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.ByID(ctx, id)
}

// SetPassword replaces an account's password and signs out its sessions.
//
// Ending the sessions is the point of a password change as often as not: the
// person changing it may be doing so because they believe the old one leaked,
// and leaving live sessions behind would defeat that.
//
// It retires any invitation in the same statement. An invited account is one
// with no credential of its own, waiting for a provider to establish who holds
// the address; giving it a password answers that question a different way, and
// leaving the invitation live would let whoever holds the address at the
// provider claim an account somebody is already using.
func (s *Store) SetPassword(ctx context.Context, id, password string) error {
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	now := s.now().UnixMilli()
	return s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		affected, err := tx.ExecAffected(`
			UPDATE users
			   SET password_hash = ?, invite_provider = '',
			       invite_expires_at = NULL, updated_at = ?
			 WHERE id = ?`,
			hash, now, id)
		if err != nil {
			return err
		}
		if affected == 0 {
			return ErrNotFound
		}
		return tx.Exec(`DELETE FROM user_sessions WHERE user_id = ?`, id)
	})
}

// Delete removes an account and every session it holds.
func (s *Store) Delete(ctx context.Context, id string) error {
	now := s.now().UnixMilli()
	return s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		var exists int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM users WHERE id = ?`, id).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return ErrNotFound
		}
		adminsBefore, err := roles.CountAdministrators(tx)
		if err != nil {
			return err
		}
		// Sessions and linked providers cascade on the foreign key, but saying
		// so here keeps the behaviour true of a database restored without
		// foreign keys on. The identities matter more than the sessions do: a
		// row left behind would reserve a provider account against the person
		// ever having an account here again.
		if err := tx.Exec(`DELETE FROM user_sessions WHERE user_id = ?`, id); err != nil {
			return err
		}
		if err := tx.Exec(`DELETE FROM user_identities WHERE user_id = ?`, id); err != nil {
			return err
		}
		// And any half-finished offer to link a provider to it, for the same
		// reason: the row names an account that is about to stop existing.
		if err := tx.Exec(`DELETE FROM sso_pending_links WHERE user_id = ?`, id); err != nil {
			return err
		}
		// Memberships too, and for the same reason: a row left behind would
		// put a deleted account back into a group if its identifier were ever
		// reused, and would make a group's member count disagree with the
		// people it names.
		if err := tx.Exec(`DELETE FROM group_members WHERE user_id = ?`, id); err != nil {
			return err
		}
		if err := tx.Exec(`DELETE FROM users WHERE id = ?`, id); err != nil {
			return err
		}
		// Judged after the delete, so the question is asked of the state it
		// would leave, and a refusal rolls it back.
		if err := roles.GuardAdminRemains(tx, adminsBefore); err != nil {
			return ErrLastAdmin
		}
		return nil
	})
}

// Authenticate verifies an email and password.
//
// A wrong address and a wrong password are indistinguishable to the caller and
// cost the same time, because an unmatched address is still compared against a
// decoy hash. Together those keep the sign-in form from answering "does this
// account exist?".
//
// An account with no password of its own is refused explicitly, by name,
// before anything is compared. Leaving it to the comparison would work today
// -- the sentinel is not a bcrypt hash and bcrypt refuses it -- and would be
// the wrong kind of correct: it would make "an SSO-only account cannot be
// signed in to with a password" a property of a string constant rather than a
// rule the code states. The decoy is still compared so the refusal costs the
// same time as any other.
//
// A pending account is deliberately not refused here. It has to be able to
// prove who it is in order to be shown a page saying it is waiting; what it
// does not get is any capability, and that is settled on the principal.
func (s *Store) Authenticate(ctx context.Context, email, password string) (*User, error) {
	u, err := s.ByEmail(ctx, email)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	hash := dummyHash
	if u != nil && !u.Disabled && u.HasPassword() {
		hash = u.PasswordHash
	}
	matched := comparePassword(hash, password)
	switch {
	case u == nil || u.Disabled:
		return nil, ErrInvalidCredentials
	case !u.HasPassword():
		return nil, ErrNoPassword
	case !matched:
		return nil, ErrInvalidCredentials
	}
	return u, nil
}

// RecordLogin stamps a successful sign-in.
func (s *Store) RecordLogin(ctx context.Context, userID string) error {
	now := s.now().UnixMilli()
	return s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		return tx.Exec(`UPDATE users SET last_login_at = ? WHERE id = ?`, now, userID)
	})
}

type rowScanner interface{ Scan(...any) error }

func (s *Store) scanUser(row rowScanner) (*User, error) {
	var (
		u             User
		grantsJSON    string
		disabled      int
		status        string
		inviteProv    string
		inviteExpires sql.NullInt64
		created       int64
		updated       int64
		lastLogin     sql.NullInt64
	)
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.DisplayName, &u.RoleID,
		&grantsJSON, &disabled, &status, &inviteProv, &inviteExpires,
		&created, &updated, &lastLogin)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.Grants = auth.DecodeGrants(grantsJSON)
	u.Disabled = disabled != 0
	// An unrecognised status reads as pending rather than active. A row whose
	// status this build does not understand is one nobody here has decided
	// about, and the safe reading of "I do not know what this means" is no
	// capabilities rather than all of them.
	u.Status = Status(status)
	if !u.Status.Valid() {
		u.Status = StatusPending
	}
	// An invitation naming a provider this build does not know is read as no
	// invitation at all. The column's CHECK makes that unreachable through
	// this process; what it covers is a row written by a later build and read
	// by this one, where the safe reading of "I do not know what this means"
	// is that nothing may claim the account.
	u.InviteProvider = Provider(inviteProv)
	if !u.InviteProvider.Valid() {
		u.InviteProvider = ""
	}
	if inviteExpires.Valid {
		t := time.UnixMilli(inviteExpires.Int64).UTC()
		u.InviteExpiresAt = &t
	}
	u.CreatedAt = time.UnixMilli(created).UTC()
	u.UpdatedAt = time.UnixMilli(updated).UTC()
	if lastLogin.Valid {
		t := time.UnixMilli(lastLogin.Int64).UTC()
		u.LastLoginAt = &t
	}
	return &u, nil
}

// --- sessions --------------------------------------------------------------

// NewSession issues a browser session and returns the token to set as a
// cookie. The token is returned once and never stored in recoverable form.
func (s *Store) NewSession(ctx context.Context, userID string, ttl time.Duration) (token string, sess *Session, err error) {
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}
	token, err = generateSecret()
	if err != nil {
		return "", nil, err
	}
	csrf, err := generateSecret()
	if err != nil {
		return "", nil, err
	}
	id, err := newID("ses_")
	if err != nil {
		return "", nil, err
	}

	now := s.now()
	expires := now.Add(ttl)
	err = s.db.WriteTx(ctx, now.UnixMilli(), func(tx *sqlite.UnitOfWork) error {
		return tx.Exec(`
			INSERT INTO user_sessions (session_hash, id, user_id, csrf_token, created_at, expires_at)
			VALUES (?,?,?,?,?,?)`,
			hashSecret(token), id, userID, csrf, now.UnixMilli(), expires.UnixMilli())
	})
	if err != nil {
		return "", nil, err
	}
	return token, &Session{
		ID:        id,
		UserID:    userID,
		CSRFToken: csrf,
		CreatedAt: now.UTC(),
		ExpiresAt: expires.UTC(),
	}, nil
}

// ResolveSession turns a session token into its account.
//
// A session for a disabled account resolves to nothing: rights are re-read on
// every request rather than frozen at sign-in, so switching an account off
// takes effect on its next call.
func (s *Store) ResolveSession(ctx context.Context, token string) (*User, *Session, error) {
	if token == "" {
		return nil, nil, ErrNotFound
	}
	var (
		sess    Session
		created int64
		expires int64
	)
	err := s.db.Reader().QueryRowContext(ctx, `
		SELECT id, user_id, csrf_token, created_at, expires_at
		FROM user_sessions WHERE session_hash = ? AND expires_at > ?`,
		hashSecret(token), s.now().UnixMilli()).
		Scan(&sess.ID, &sess.UserID, &sess.CSRFToken, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	sess.CreatedAt = time.UnixMilli(created).UTC()
	sess.ExpiresAt = time.UnixMilli(expires).UTC()

	u, err := s.ByID(ctx, sess.UserID)
	if err != nil {
		return nil, nil, err
	}
	if u.Disabled {
		return nil, nil, ErrNotFound
	}
	return u, &sess, nil
}

// DeleteSession ends one browser session.
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	now := s.now().UnixMilli()
	return s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		return tx.Exec(`DELETE FROM user_sessions WHERE session_hash = ?`, hashSecret(token))
	})
}

// PurgeExpiredSessions removes sessions past their expiry.
func (s *Store) PurgeExpiredSessions(ctx context.Context) error {
	now := s.now().UnixMilli()
	return s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		return tx.Exec(`DELETE FROM user_sessions WHERE expires_at < ?`, now)
	})
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// isUniqueViolation reports whether err is a uniqueness failure.
//
// The driver's error is matched by text because the SQLite drivers in use
// disagree on their error types, and the constraint name is stable across all
// of them.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// Housekeeper removes expired sessions and expired offers to link a provider.
//
// Expiry is enforced on read, so this is hygiene rather than correctness: a
// row past its expires_at stops resolving whether or not it has been deleted.
// What it prevents is the tables growing without bound in a deployment that
// signs in often and never restarts.
type Housekeeper struct {
	store    *Store
	interval time.Duration
}

// NewHousekeeper returns a background cleaner.
func NewHousekeeper(store *Store, interval time.Duration) *Housekeeper {
	if interval <= 0 {
		interval = time.Hour
	}
	return &Housekeeper{store: store, interval: interval}
}

// Run cleans until ctx is cancelled.
func (h *Housekeeper) Run(ctx context.Context) error {
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := h.store.PurgeExpiredSessions(ctx); err != nil {
				return fmt.Errorf("users: housekeeping: %w", err)
			}
			if err := h.store.PurgeExpiredPendingLinks(ctx); err != nil {
				return fmt.Errorf("users: housekeeping: %w", err)
			}
		}
	}
}
