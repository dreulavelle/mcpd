package users

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/storage/sqlite"
)

// PendingLinkTTL bounds how long an offer to link a provider stays open.
//
// The same ten minutes a half-finished sign-in gets, and for the same reason:
// long enough to find a password manager, short enough that an offer left in a
// browser somebody walked away from is not a thing to come back to. It is
// enforced in the WHERE clause of every statement that touches the row, so one
// that outlives it is untidy rather than usable.
const PendingLinkTTL = 10 * time.Minute

// MaxPendingLinkFailures retires an offer after this many wrong passwords.
//
// The row names one account, so without a ceiling it is a password oracle with
// a ten-minute life against an address somebody already proved they can read
// mail at. Three is enough for a typo and a second try.
const MaxPendingLinkFailures = 3

// PendingLink is an offer to attach a provider identity to an account that
// already holds the address.
//
// It exists because the alternative at a collision was a dead end. An unlinked
// identity is still not an account -- what this adds is somewhere for the
// account's own password to be presented once, which is the proof the provider
// cannot give.
type PendingLink struct {
	Provider Provider
	Subject  string
	Email    string
	Name     string
	// UserID is the account the address belongs to. Resolved when the offer is
	// made and restated in the WHERE clause when it is claimed, because
	// everything about that account can change in between.
	UserID string
}

// OfferLink records a pending link and returns the two secrets to set on the
// browser it was offered to.
//
// Both are minted here rather than taken from the caller, and the flow's own
// binding is deliberately not reused. The callback retires that cookie on
// every exit -- including this one -- so a row bound to it is a row no browser
// can ever present: the offer was written, the screen was drawn, and every
// request for it answered "there is nothing waiting". That was the shape of
// the bug this signature exists to make impossible.
//
// Expired rows are purged in the same transaction as the row being written.
// The endpoint that reaches here needs no credential -- anybody who can start
// a sign-in for a taken address causes one insert -- so this is what actually
// bounds the table, whatever the background sweep is doing.
func (s *Store) OfferLink(ctx context.Context, link PendingLink) (token, binding string, err error) {
	if !link.Provider.Valid() {
		return "", "", fmt.Errorf("users: %q is not a provider this build knows", link.Provider)
	}
	if strings.TrimSpace(link.Subject) == "" || strings.TrimSpace(link.UserID) == "" {
		return "", "", errors.New("users: an offered link needs a subject and an account")
	}
	if token, err = generateSecret(); err != nil {
		return "", "", err
	}
	if binding, err = generateSecret(); err != nil {
		return "", "", err
	}

	now := s.now()
	err = s.db.WriteTx(ctx, now.UnixMilli(), func(tx *sqlite.UnitOfWork) error {
		if err := tx.Exec(`DELETE FROM sso_pending_links WHERE expires_at < ?`,
			now.UnixMilli()); err != nil {
			return err
		}
		// One live offer per account. A person who starts the flow twice
		// should be answering for the second attempt, not for whichever row a
		// lookup happened to find, and leaving both would multiply the
		// attempts a ceiling of three is there to bound.
		if err := tx.Exec(`DELETE FROM sso_pending_links WHERE user_id = ?`,
			link.UserID); err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO sso_pending_links (token_hash, provider, subject, email,
			                               display_name, user_id, binding_hash,
			                               failures, created_at, expires_at)
			VALUES (?,?,?,?,?,?,?,0,?,?)`,
			hashSecret(token), string(link.Provider), link.Subject, link.Email,
			SafeDisplayName(link.Name), link.UserID, hashSecret(binding),
			now.UnixMilli(), now.Add(PendingLinkTTL).UnixMilli())
	})
	if err != nil {
		return "", "", err
	}
	return token, binding, nil
}

// PendingLinkView is what the signed-out screen may be told about an offer.
//
// Deliberately thin. The address is already known to whoever holds the cookie
// -- they just signed in at the provider with it -- and everything else about
// the account is nobody's business until the password has been presented.
type PendingLinkView struct {
	Provider Provider
	Email    string
}

// PendingLinkFor reads the offer this browser is holding.
//
// Every condition the claim applies is applied here too, so a screen is drawn
// only for an offer that could actually be completed. ErrNotFound covers all
// of them together: from the browser's side "there is nothing here" and "there
// was, and it has expired" are one sentence and one thing to do.
func (s *Store) PendingLinkFor(ctx context.Context, token, binding string) (*PendingLinkView, error) {
	if token == "" || binding == "" {
		return nil, ErrNotFound
	}
	var provider, email string
	err := s.db.Reader().QueryRowContext(ctx, `
		SELECT l.provider, u.email
		  FROM sso_pending_links l
		  JOIN users u ON u.id = l.user_id
		 WHERE l.token_hash = ?
		   AND l.binding_hash = ?
		   AND l.expires_at > ?
		   AND l.failures < ?
		   AND u.disabled = 0
		   AND u.password_hash <> ?`,
		hashSecret(token), hashSecret(binding), s.now().UnixMilli(),
		MaxPendingLinkFailures, NoPassword).
		Scan(&provider, &email)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &PendingLinkView{Provider: Provider(provider), Email: email}, nil
}

// ClaimPendingLink confirms an offer with the account's own password.
//
// The order is the whole of it. The row is read under every condition the
// claim will be made under; the password is compared outside the write
// transaction, because bcrypt at this cost holds the single writer for a
// quarter of a second and a sign-in is not worth blocking every other write on;
// and then the delete, the identity and the audit entry go in one guarded
// transaction that restates what was read.
//
// What it restates is the point. The account may have been disabled, given a
// provider of its own, or had its password changed between the read and the
// write, and the hash compared against is carried into the WHERE clause so
// that a password changed underneath this refuses rather than links on the
// strength of a comparison against a hash nobody holds any more.
//
// A wrong password leaves the row for another try and counts against it. There
// is no session here to carry a CSRF token, so what closes login CSRF is the
// cookies: both are SameSite=Lax, which a browser will not send on a
// cross-site POST, and without them this matches no row. The JSON body is not
// part of that defence -- nothing here checks a content type -- so it is not
// counted as one.
func (s *Store) ClaimPendingLink(ctx context.Context, token, binding, password string) (*User, error) {
	if token == "" || binding == "" {
		return nil, ErrNotFound
	}
	tokenHash, bindingHash := hashSecret(token), hashSecret(binding)

	var (
		link         PendingLink
		provider     string
		displayName  string
		accountEmail string
		passwordHash string
	)
	err := s.db.Reader().QueryRowContext(ctx, `
		SELECT l.provider, l.subject, l.email, l.display_name, l.user_id,
		       u.email, u.password_hash
		  FROM sso_pending_links l
		  JOIN users u ON u.id = l.user_id
		 WHERE l.token_hash = ?
		   AND l.binding_hash = ?
		   AND l.expires_at > ?
		   AND l.failures < ?
		   AND u.disabled = 0
		   AND u.password_hash <> ?
		   AND NOT EXISTS (
		       SELECT 1 FROM user_identities i
		        WHERE i.user_id = u.id AND i.provider = l.provider
		   )`,
		tokenHash, bindingHash, s.now().UnixMilli(),
		MaxPendingLinkFailures, NoPassword).
		Scan(&provider, &link.Subject, &link.Email, &displayName, &link.UserID,
			&accountEmail, &passwordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	link.Provider = Provider(provider)
	link.Name = displayName

	if !comparePassword(passwordHash, password) {
		if err := s.recordLinkFailure(ctx, tokenHash, bindingHash); err != nil {
			return nil, err
		}
		return nil, ErrInvalidCredentials
	}

	// There is deliberately no fallback actor. The trail's value is that the
	// actor is the account that acted, and the address the provider reported
	// is exactly the thing this whole path refuses to treat as an identity.
	actor := "user:" + accountEmail

	now := s.now().UnixMilli()
	err = s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		// Single use, through the same guard the read applied. A second claim
		// -- a double submit, a replay of the request -- matches zero rows.
		affected, err := tx.ExecAffected(`
			DELETE FROM sso_pending_links
			 WHERE token_hash = ? AND binding_hash = ?
			   AND expires_at > ? AND failures < ?`,
			tokenHash, bindingHash, now, MaxPendingLinkFailures)
		if err != nil {
			return err
		}
		if affected == 0 {
			return ErrNotFound
		}

		// Every condition the read relied on, restated where a race cannot
		// slip past it: the account is still there, still enabled, still has
		// the password that was just compared, and still has no identity from
		// this provider.
		affected, err = tx.ExecAffected(`
			INSERT INTO user_identities (provider, subject, user_id, email,
			                             linked_by, created_at)
			SELECT ?,?,?,?,?,?
			 WHERE EXISTS (
			     SELECT 1 FROM users u
			      WHERE u.id = ? AND u.disabled = 0 AND u.password_hash = ?
			 )
			   AND NOT EXISTS (
			     SELECT 1 FROM user_identities i
			      WHERE i.user_id = ? AND i.provider = ?
			   )`,
			string(link.Provider), link.Subject, link.UserID, link.Email,
			actor, now,
			link.UserID, passwordHash,
			link.UserID, string(link.Provider))
		if err != nil {
			return err
		}
		if affected == 0 {
			// Two very different things failed the same statement, and they
			// need different answers. A provider already linked to this
			// account is a collision: there is something there, and saying so
			// is right. Anything else -- disabled, or a password changed
			// between the compare and this write -- is the offer no longer
			// being claimable, which is the same sentence as an expired one.
			var linked int
			if err := tx.QueryRow(
				`SELECT COUNT(*) FROM user_identities WHERE user_id = ? AND provider = ?`,
				link.UserID, string(link.Provider)).Scan(&linked); err != nil {
				return err
			}
			if linked > 0 {
				return ErrIdentityLinked
			}
			return ErrNotFound
		}
		// In the same transaction as the link it records. An entry written
		// separately is one that can disagree with the database about whether
		// the link happened at all.
		//
		// `confirmed` says which proof was given -- "password" here, "session"
		// from the profile page -- because assurance is about what can be
		// shown, and the two are different things to be able to show.
		return tx.AppendAudit(sqlite.AdminAct{
			Kind:    "account.identity_linked",
			Actor:   actor,
			Subject: link.Email,
			Action:  "link",
			Detail: map[string]any{
				"provider":  string(link.Provider),
				"account":   link.UserID,
				"confirmed": "password",
			},
		})
	})
	if isUniqueViolation(err) {
		// The subject belongs to somebody else here already. From the person's
		// side that is the same sentence as "this account already has one".
		return nil, ErrIdentityLinked
	}
	if err != nil {
		return nil, err
	}

	return s.ByID(ctx, link.UserID)
}

// DiscardPendingLink retires an offer somebody said no to.
//
// "Not now" has to actually retire the row rather than merely navigate away
// from it: an offer left live is one the next person at that browser is
// holding.
func (s *Store) DiscardPendingLink(ctx context.Context, token, binding string) error {
	if token == "" || binding == "" {
		return nil
	}
	now := s.now().UnixMilli()
	return s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		return tx.Exec(
			`DELETE FROM sso_pending_links WHERE token_hash = ? AND binding_hash = ?`,
			hashSecret(token), hashSecret(binding))
	})
}

// PurgeExpiredPendingLinks removes offers past their expiry.
//
// Hygiene rather than correctness, like the session sweep: expiry is a
// condition of every statement that reads one, so a row left behind stops
// being usable whether or not it is deleted.
func (s *Store) PurgeExpiredPendingLinks(ctx context.Context) error {
	now := s.now().UnixMilli()
	return s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		return tx.Exec(`DELETE FROM sso_pending_links WHERE expires_at < ?`, now)
	})
}

// recordLinkFailure counts a wrong password and retires the row at the ceiling.
//
// One statement for the count and one for the retirement, both guarded, so two
// attempts arriving together cannot each read two and both write three.
func (s *Store) recordLinkFailure(ctx context.Context, tokenHash, bindingHash string) error {
	now := s.now().UnixMilli()
	return s.db.WriteTx(ctx, now, func(tx *sqlite.UnitOfWork) error {
		if err := tx.Exec(`
			UPDATE sso_pending_links SET failures = failures + 1
			 WHERE token_hash = ? AND binding_hash = ?`,
			tokenHash, bindingHash); err != nil {
			return err
		}
		return tx.Exec(`
			DELETE FROM sso_pending_links
			 WHERE token_hash = ? AND failures >= ?`,
			tokenHash, MaxPendingLinkFailures)
	})
}
