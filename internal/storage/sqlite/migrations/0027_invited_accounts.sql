-- 0027_invited_accounts: two ways to arrive at an account that already exists.
--
-- Both exist because the rule that an unlinked provider identity is not an
-- account left every arrival at a taken address as a dead end. The rule does
-- not change here; what changes is that there are now two acceptable proofs of
-- ownership beside "sign in and link it from the profile page", and each is
-- written down as a row rather than inferred from a matching address.
--
-- The first proof is the account's own password, presented once at the
-- collision. `sso_pending_links` is the half-finished half of that: a provider
-- round trip has completed, this host knows which account the address belongs
-- to, and it is waiting for the one thing the provider cannot say.
--
-- The second is an administrator asserting the address in advance.
-- `invite_provider` on an account marks it invited, and the first verified
-- sign-in through that provider claims it. That claim deliberately bypasses
-- RegistrationPolicy -- closed registration, the domain allow-list and the
-- approval step do not apply -- because the administrator already made the
-- decision the policy exists to make about strangers, and the person arriving
-- is not one.

-- '' means not invited. Anything else is the one provider the invitation may
-- be claimed through; the closed set is the same one user_identities and
-- sso_states carry, so a misspelling is a provider nobody configured rather
-- than an invitation nobody can claim.
--
-- "Invited implies password_hash = '!'" is a two-column constraint SQLite
-- cannot add by ALTER, so it is enforced in Go and restated in the WHERE
-- clause of every statement that acts on it.
ALTER TABLE users ADD COLUMN invite_provider TEXT NOT NULL DEFAULT ''
    CHECK (invite_provider IN ('', 'google', 'github', 'entra', 'oidc'));

-- When the invitation stops being claimable. NULL is an invitation with no
-- expiry, which is what every row written before this column existed has and
-- what none written after it does. An address can be reassigned to a different
-- person -- a mailbox at a company somebody left, a Workspace account recycled
-- -- and an invitation that never lapses is an account handed to whoever holds
-- the address next.
ALTER TABLE users ADD COLUMN invite_expires_at INTEGER;

-- One offered link. Written when a sign-in arrives for an address a password
-- account already holds, and consumed by that account's password.
--
-- Not sso_states: that table has no column for a subject or a name, its CHECK
-- constraints name a closed set of purposes, and adding either would mean
-- rebuilding it. What it does share is the shape -- a digest for a key, a
-- browser binding beside it, an expiry in the WHERE clause of the claim -- and
-- the shape is the part that matters.
CREATE TABLE sso_pending_links (
    token_hash   TEXT    PRIMARY KEY,
    provider     TEXT    NOT NULL,
    -- What the provider actually established. The identity is written from
    -- these when the password confirms the account, so it is the subject seen
    -- at the collision that gets linked, never one presented later.
    subject      TEXT    NOT NULL,
    email        TEXT    NOT NULL DEFAULT '',
    display_name TEXT    NOT NULL DEFAULT '',
    user_id      TEXT    NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- The browser this offer was made to, digested like the state binding. An
    -- offer nobody can bind to a browser is one anybody can hand to anybody.
    binding_hash TEXT    NOT NULL,
    -- Wrong passwords so far. This row names an account, so without a ceiling
    -- it is a password oracle with a ten-minute life; at the third the row is
    -- retired and the sign-in has to start again.
    failures     INTEGER NOT NULL DEFAULT 0,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    CHECK (provider IN ('google', 'github', 'entra', 'oidc')),
    CHECK (subject <> ''),
    CHECK (failures >= 0)
) STRICT, WITHOUT ROWID;

CREATE INDEX ix_sso_pending_links_expiry ON sso_pending_links (expires_at);
