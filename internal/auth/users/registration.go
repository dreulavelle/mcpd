package users

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/storage/sqlite"
)

// ErrLastCredential reports unlinking the only thing an account can sign in
// with. An account nobody can sign in to is not a locked account, it is a
// deleted one that still appears in the list.
var ErrLastCredential = errors.New("users: that is the only way this account can sign in")

// Provider is a configured identity provider.
//
// A short closed set rather than a free string. The name is a primary-key
// column, it decides which flow runs, and it appears in the audit trail; a
// misspelling would be a second provider nobody configured.
type Provider string

const (
	ProviderGoogle Provider = "google"
	ProviderGitHub Provider = "github"
	ProviderEntra  Provider = "entra"
)

// Valid reports whether p is a provider this build knows.
func (p Provider) Valid() bool {
	switch p {
	case ProviderGoogle, ProviderGitHub, ProviderEntra:
		return true
	}
	return false
}

func (p Provider) String() string { return string(p) }

// Identity is one provider account attached to one mcpd account.
type Identity struct {
	Provider Provider  `json:"provider"`
	Subject  string    `json:"subject"`
	UserID   string    `json:"user_id"`
	Email    string    `json:"email"`
	LinkedBy string    `json:"linked_by"`
	LinkedAt time.Time `json:"linked_at"`
}

// RegistrationPolicy is what a host will accept from a stranger.
//
// The zero value accepts nothing, which is what an upgrade has to produce: a
// deployment that had no sign-ups before this existed must not have them
// afterwards because a default said so.
type RegistrationPolicy struct {
	// Enabled opens registration at all.
	Enabled bool
	// RequireApproval lands a new account pending rather than active.
	RequireApproval bool
	// AllowedDomains restricts which addresses may register. Empty allows any
	// address, which is only reachable once Enabled is deliberately set.
	AllowedDomains []string
}

// Allows reports whether an address may register under this policy.
//
// The address is expected to be normalised already; it is lowercased here
// anyway, because a rule that depends on the caller having remembered to
// normalise is a rule with a way past it.
func (p RegistrationPolicy) Allows(email string) error {
	if !p.Enabled {
		return ErrRegistrationClosed
	}
	if len(p.AllowedDomains) == 0 {
		return nil
	}
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return ErrDomainNotAllowed
	}
	domain := strings.ToLower(strings.TrimSpace(email[at+1:]))
	for _, allowed := range p.AllowedDomains {
		// A leading "@" or "." is what people type; neither changes the
		// domain being named, and refusing them would make the setting read
		// as broken.
		want := strings.ToLower(strings.TrimSpace(allowed))
		want = strings.TrimPrefix(want, "@")
		want = strings.TrimPrefix(want, ".")
		if want != "" && want == domain {
			return nil
		}
	}
	return ErrDomainNotAllowed
}

// RegisterRequest describes a stranger asking for an account.
type RegisterRequest struct {
	Email       string
	DisplayName string
	// Password is empty for a registration that arrives through a provider.
	// Such an account stores NoPassword and can only sign in through the
	// identity linked below.
	Password string
	// Identity attaches a provider account in the same transaction as the
	// account is created. Without it the registration is a password one.
	Identity *Identity
	// Policy is what the host will accept, read at the moment of the request
	// rather than captured at startup.
	Policy RegistrationPolicy
}

// Register creates an account for somebody who asked for one.
//
// Every rule that decides whether a stranger may have an account is applied
// here, once, whichever door they came through. A password registration and a
// provider registration are the same act with a different credential, and
// building one path that checks the policy and one that does not is how a host
// ends up refusing sign-ups on a form while accepting them through Google.
//
// Four refusals, and the order matters only in what it tells whoever asked:
//
//   - The instance must already be claimed. CreateFirst is what makes somebody
//     an administrator, and a stranger completing a flow at a third party must
//     never be that: a fresh host reachable by anyone would belong to whoever
//     got there first with a Google account.
//   - Registration must be open. Off is the default and an upgrade does not
//     change it.
//   - The address must be inside the allow-list, when there is one.
//   - The address must not already belong to an account. For a provider
//     registration that refusal is the whole defence against the takeover:
//     adopting the existing account would hand it to whoever controls the
//     address at the provider.
//
// The account, its identity and the audit entry are one transaction. The
// claimed check and the address check are conditions the write is guarded by,
// not reads performed before it, so two strangers racing produce one account
// and one refusal.
func (s *Store) Register(ctx context.Context, req RegisterRequest) (*User, error) {
	email, err := NormalizeEmail(req.Email)
	if err != nil {
		return nil, err
	}
	if err := req.Policy.Allows(email); err != nil {
		return nil, err
	}
	displayName, err := ValidateDisplayName(req.DisplayName)
	if err != nil {
		return nil, err
	}

	hash := NoPassword
	if req.Identity == nil {
		if hash, err = HashPassword(req.Password); err != nil {
			return nil, err
		}
	} else if !req.Identity.Provider.Valid() {
		return nil, fmt.Errorf("users: %q is not a provider this build knows",
			req.Identity.Provider)
	} else if strings.TrimSpace(req.Identity.Subject) == "" {
		return nil, fmt.Errorf("users: a provider identity needs a subject")
	}

	id, err := newID("usr_")
	if err != nil {
		return nil, err
	}

	status := StatusActive
	if req.Policy.RequireApproval {
		status = StatusPending
	}

	// A self-registration is an ordinary user: it reads, proposes and
	// approves, and reaches every mounted integration. Narrower would need a
	// choice nobody is present to make, and the account holds none of it until
	// somebody approves anyway when approval is required. What an
	// administrator does afterwards on the Users page is unchanged.
	u := &User{
		ID:           id,
		Email:        email,
		PasswordHash: hash,
		DisplayName:  displayName,
		Role:         auth.RoleUser,
		Plugins:      []string{auth.Wildcard},
		Status:       status,
	}
	plugins, err := json.Marshal(u.Plugins)
	if err != nil {
		return nil, fmt.Errorf("users: encode plugin grants: %w", err)
	}

	actor := "self:" + email
	now := s.now().UnixMilli()
	err = s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		affected, err := tx.ExecAffected(`
			INSERT INTO users (id, email, password_hash, display_name, role,
			                   plugins_json, disabled, status, created_at, updated_at)
			SELECT ?,?,?,?,?,?,0,?,?,?
			-- Claimed: at least one account already exists. An unclaimed
			-- instance is claimed from the setup form and nowhere else.
			WHERE EXISTS (SELECT 1 FROM users)
			-- Free: nobody holds this address, and nobody renders as it.
			AND NOT EXISTS (SELECT 1 FROM users WHERE email = ?)
			AND NOT EXISTS (SELECT 1 FROM users WHERE lower(display_name) = ?)
			AND (? = '' OR NOT EXISTS (
				SELECT 1 FROM users WHERE email = lower(?)
			))`,
			u.ID, u.Email, u.PasswordHash, u.DisplayName, string(u.Role),
			string(plugins), string(u.Status), now, now,
			u.Email, u.Email, u.DisplayName, u.DisplayName)
		if err != nil {
			return err
		}
		if affected == 0 {
			// Three conditions failed together in one statement, so which one
			// is answered by a read afterwards rather than by the write. This
			// is the only place in the transaction that reads to explain
			// rather than to decide.
			var n int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return ErrUnclaimed
			}
			var taken int
			if err := tx.QueryRow(
				`SELECT COUNT(*) FROM users WHERE email = ?`, u.Email).Scan(&taken); err != nil {
				return err
			}
			if taken > 0 {
				return ErrAddressTaken
			}
			return ErrNameCollides
		}

		if req.Identity != nil {
			if err := insertIdentity(tx, Identity{
				Provider: req.Identity.Provider,
				Subject:  req.Identity.Subject,
				UserID:   u.ID,
				Email:    req.Identity.Email,
				LinkedBy: actor,
			}, now); err != nil {
				return err
			}
		}

		// Registered, not approved. Approval is its own entry, written when
		// somebody actually decides -- collapsing the two would put a
		// privilege grant in the trail that nobody performed.
		return tx.AppendAudit(sqlite.AdminAct{
			Kind:    "account.registered",
			Actor:   actor,
			Subject: u.Email,
			Action:  "register",
			Detail: map[string]any{
				"status":   string(u.Status),
				"role":     string(u.Role),
				"provider": providerName(req.Identity),
			},
		})
	})
	if isUniqueViolation(err) {
		// The identity insert is the only unique constraint the guard above
		// does not cover: the provider subject may already belong to somebody.
		return nil, ErrIdentityLinked
	}
	if err != nil {
		return nil, err
	}
	u.CreatedAt = time.UnixMilli(now).UTC()
	u.UpdatedAt = u.CreatedAt
	return u, nil
}

func providerName(i *Identity) string {
	if i == nil {
		return "password"
	}
	return string(i.Provider)
}

func insertIdentity(tx *sqlite.UnitOfWork, i Identity, now int64) error {
	return tx.Exec(`
		INSERT INTO user_identities (provider, subject, user_id, email, linked_by, created_at)
		VALUES (?,?,?,?,?,?)`,
		string(i.Provider), i.Subject, i.UserID, i.Email, i.LinkedBy, now)
}

// UserByIdentity resolves a provider identity to the account it is linked to.
//
// The only way a provider sign-in becomes an mcpd account. There is
// deliberately no fallback to matching on the address: an unlinked identity
// whose address happens to equal an account's is not that account, and
// treating it as one hands the account to whoever controls the address at the
// provider.
func (s *Store) UserByIdentity(ctx context.Context, provider Provider, subject string) (*User, error) {
	if !provider.Valid() || subject == "" {
		return nil, ErrNotFound
	}
	return s.scanUser(s.db.Reader().QueryRowContext(ctx, `
		SELECT `+prefixed(userColumns, "u")+`
		  FROM users u
		  JOIN user_identities i ON i.user_id = u.id
		 WHERE i.provider = ? AND i.subject = ?`, string(provider), subject))
}

// prefixed qualifies a column list with a table alias, so the shared list can
// be used in a join without every name becoming ambiguous.
func prefixed(columns, alias string) string {
	parts := strings.Split(columns, ",")
	for i, p := range parts {
		parts[i] = alias + "." + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

// IdentitiesFor lists the providers an account can sign in with.
func (s *Store) IdentitiesFor(ctx context.Context, userID string) ([]Identity, error) {
	rows, err := s.db.Reader().QueryContext(ctx, `
		SELECT provider, subject, user_id, email, linked_by, created_at
		  FROM user_identities WHERE user_id = ? ORDER BY provider`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Identity{}
	for rows.Next() {
		var i Identity
		var provider string
		var created int64
		if err := rows.Scan(&provider, &i.Subject, &i.UserID, &i.Email,
			&i.LinkedBy, &created); err != nil {
			return nil, err
		}
		i.Provider = Provider(provider)
		i.LinkedAt = time.UnixMilli(created).UTC()
		out = append(out, i)
	}
	return out, rows.Err()
}

// LinkIdentity attaches a provider account to an existing account.
//
// This is the only way an account that already exists gains a provider, and it
// is performed by that account while signed in. That is the whole of the
// answer to the takeover: proving control of an address at Google says nothing
// about who owns the mcpd account with that address, while proving control of
// the mcpd account and then completing a flow at Google says exactly the thing
// that needs saying.
//
// The insert is guarded rather than checked: the primary key refuses a subject
// somebody else already linked, and the UNIQUE (provider, user_id) refuses a
// second Google account on one mcpd account. Both surface as
// ErrIdentityLinked, because from the person's side they are the same
// sentence.
func (s *Store) LinkIdentity(ctx context.Context, actor string, i Identity) error {
	if !i.Provider.Valid() {
		return fmt.Errorf("users: %q is not a provider this build knows", i.Provider)
	}
	if strings.TrimSpace(i.Subject) == "" || strings.TrimSpace(i.UserID) == "" {
		return fmt.Errorf("users: a link needs a subject and an account")
	}
	now := s.now().UnixMilli()
	err := s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		var exists int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM users WHERE id = ?`, i.UserID).
			Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return ErrNotFound
		}
		i.LinkedBy = actor
		if err := insertIdentity(tx, i, now); err != nil {
			return err
		}
		return tx.AppendAudit(sqlite.AdminAct{
			Kind:    "account.identity_linked",
			Actor:   actor,
			Subject: i.Email,
			Action:  "link",
			Detail: map[string]any{
				"provider": string(i.Provider),
				"account":  i.UserID,
			},
		})
	})
	if isUniqueViolation(err) {
		return ErrIdentityLinked
	}
	return err
}

// UnlinkIdentity detaches a provider from an account.
//
// Refused when it is the only way in. An account with no password and no
// identity cannot be signed in to by anybody, which is a deletion wearing the
// appearance of an edit -- and the person performing it is usually the one who
// would be locked out.
func (s *Store) UnlinkIdentity(ctx context.Context, actor, userID string, provider Provider) error {
	now := s.now().UnixMilli()
	return s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		var hash string
		if err := tx.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, userID).
			Scan(&hash); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		// The condition is in the WHERE clause: an account with a password, or
		// with another identity beside this one, may drop this one. Anything
		// else matches zero rows.
		affected, err := tx.ExecAffected(`
			DELETE FROM user_identities
			 WHERE user_id = ? AND provider = ?
			   AND (? = 1 OR EXISTS (
			       SELECT 1 FROM user_identities other
			        WHERE other.user_id = ? AND other.provider <> ?
			   ))`,
			userID, string(provider),
			boolInt(hash != NoPassword && hash != ""), userID, string(provider))
		if err != nil {
			return err
		}
		if affected == 0 {
			// Either there was no such link, or removing it would have left
			// nothing. Telling them apart takes one read, and the two need
			// different words.
			var linked int
			if err := tx.QueryRow(
				`SELECT COUNT(*) FROM user_identities WHERE user_id = ? AND provider = ?`,
				userID, string(provider)).Scan(&linked); err != nil {
				return err
			}
			if linked == 0 {
				return ErrNotFound
			}
			return ErrLastCredential
		}
		return tx.AppendAudit(sqlite.AdminAct{
			Kind:    "account.identity_unlinked",
			Actor:   actor,
			Subject: userID,
			Action:  "unlink",
			Detail:  map[string]any{"provider": string(provider)},
		})
	})
}

// PendingRegistrations lists the accounts waiting for a decision.
func (s *Store) PendingRegistrations(ctx context.Context) ([]*User, error) {
	rows, err := s.db.Reader().QueryContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE status = ? ORDER BY created_at`,
		string(StatusPending))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []*User{}
	for rows.Next() {
		u, err := s.scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ApproveRegistration turns a pending account into an ordinary one.
//
// A privilege grant, and audited as one: this is the moment somebody gains the
// ability to read this host's state, propose changes against it, and approve
// them. The entry names the administrator who decided, and it is written in
// the same transaction as the status change -- an approval recorded separately
// from the grant it authorised is a trail that can disagree with the database
// about who may do what.
//
// Guarded on the status it expects. Two administrators approving the same
// registration produce one approval and one ErrNotPending, rather than two
// entries claiming a grant that happened once.
func (s *Store) ApproveRegistration(ctx context.Context, actor, id string) (*User, error) {
	now := s.now().UnixMilli()
	err := s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		var email, role string
		if err := tx.QueryRow(`SELECT email, role FROM users WHERE id = ?`, id).
			Scan(&email, &role); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		affected, err := tx.ExecAffected(`
			UPDATE users SET status = ?, updated_at = ?
			 WHERE id = ? AND status = ?`,
			string(StatusActive), now, id, string(StatusPending))
		if err != nil {
			return err
		}
		if affected == 0 {
			return ErrNotPending
		}
		return tx.AppendAudit(sqlite.AdminAct{
			Kind:    "account.approved",
			Actor:   actor,
			Subject: email,
			Action:  "approve",
			Detail: map[string]any{
				"account": id,
				"role":    role,
			},
		})
	})
	if err != nil {
		return nil, err
	}
	return s.ByID(ctx, id)
}

// RejectRegistration removes a pending account.
//
// The row goes rather than being marked refused. A rejected registration is
// not an account somebody may later re-enable -- it is a request that was
// declined, and leaving a disabled row behind would reserve the address
// against the person ever asking again, or against an administrator creating
// the account deliberately. What survives is the audit entry, which is the
// record that the request happened and who declined it.
//
// Guarded on pending, so a rejection cannot delete an approved account by
// arriving late.
func (s *Store) RejectRegistration(ctx context.Context, actor, id string) error {
	now := s.now().UnixMilli()
	return s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		var email string
		if err := tx.QueryRow(`SELECT email FROM users WHERE id = ?`, id).Scan(&email); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		// The identity goes with the account. It cascades on the foreign key,
		// and is spelled out for the same reason Delete spells it out: a row
		// left behind would reserve a provider account against the person ever
		// asking again, or against an administrator making the account
		// deliberately.
		if err := tx.Exec(`
			DELETE FROM user_identities
			 WHERE user_id IN (SELECT id FROM users WHERE id = ? AND status = ?)`,
			id, string(StatusPending)); err != nil {
			return err
		}
		affected, err := tx.ExecAffected(
			`DELETE FROM users WHERE id = ? AND status = ?`, id, string(StatusPending))
		if err != nil {
			return err
		}
		if affected == 0 {
			return ErrNotPending
		}
		return tx.AppendAudit(sqlite.AdminAct{
			Kind:    "account.rejected",
			Actor:   actor,
			Subject: email,
			Action:  "reject",
			Detail:  map[string]any{"account": id},
		})
	})
}
