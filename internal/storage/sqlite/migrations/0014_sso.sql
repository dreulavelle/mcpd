-- 0014_sso: sign in with an identity provider, and let strangers ask for an
-- account.
--
-- Three facts the schema did not have.
--
-- First, an account can now exist without a password. `password_hash` stays
-- NOT NULL rather than becoming nullable, because a nullable credential column
-- is one every comparison has to remember to check: the value for an SSO-only
-- account is a sentinel that is not a bcrypt hash at all, so bcrypt refuses it
-- structurally and no password can ever produce a match. The Go side refuses
-- such an account by name before it compares, and the sentinel is what makes
-- that a second gate rather than the only one.
--
-- Second, "waiting for an administrator" is not "an administrator switched
-- this off". `disabled` is a decision somebody made about an account that
-- exists; a pending registration is an account nobody has decided about yet.
-- Collapsing the two would make approving a registration indistinguishable
-- from re-enabling a suspended account in the record, and would give the
-- refusal the wrong words. A pending account may prove who it is -- that is
-- what signing in demonstrates -- and holds no capability at all until an
-- administrator says so.
--
-- Third, a provider identity is a row, never an inference. The table below is
-- the only thing that makes a Google subject into an mcpd account. There is
-- deliberately no path that matches on the email address: the whole of the
-- account-takeover risk in bolting SSO onto password accounts is a provider
-- sign-in for alice@corp.com silently adopting the password account for
-- alice@corp.com, which hands the mcpd account to whoever controls that
-- address at the provider. Linking is an act by the already-signed-in account,
-- and it writes a row here.

-- Pending is a state of the account, beside disabled rather than inside it.
--
-- Added rather than rebuilt into the table: the column carries its own CHECK,
-- and every existing row is 'active', which is the honest reading of an
-- account created before anybody could be asked to approve one.
ALTER TABLE users ADD COLUMN status TEXT NOT NULL DEFAULT 'active'
    CHECK (status IN ('active', 'pending'));

-- The pending queue is read whenever an administrator opens the Authentication
-- page, and is empty in most deployments; a partial index costs nothing when
-- there is nothing in it.
CREATE INDEX ix_users_pending ON users (created_at) WHERE status = 'pending';


-- One provider identity, linked to one account.
--
-- The primary key is the pair the provider actually guarantees: an issuer and
-- a subject. Not the email address, which providers let people change and
-- which is exactly the value an attacker would arrange to control.
CREATE TABLE user_identities (
    -- 'google', 'github' or 'entra'. The configured provider, not the issuer
    -- URL: a deployment moving an Entra tenant should not orphan every link.
    provider   TEXT    NOT NULL,
    -- The provider's immutable identifier for the person: `sub` for OIDC, the
    -- numeric account id for GitHub. Stable across a name or address change,
    -- which is the entire reason it and not the address is the key.
    subject    TEXT    NOT NULL,
    user_id    TEXT    NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- The address the provider reported when the link was made. For display
    -- and for explaining a link later; never consulted to decide who a
    -- sign-in is.
    email      TEXT    NOT NULL DEFAULT '',
    -- Who linked it: 'user:<address>' when an account attached a provider to
    -- itself, and 'self:<address>' when the link and the account were made
    -- together by a self-registration. The two are kept apart because they are
    -- different acts -- one was performed by somebody who had already proved
    -- they own the account, and the other by somebody who did not have one
    -- yet.
    linked_by  TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (provider, subject),
    -- One identity per provider per account. Two Google accounts on one mcpd
    -- account would make "which one signed in" unanswerable in the record,
    -- and unlinking ambiguous.
    UNIQUE (provider, user_id),
    CHECK (provider IN ('google', 'github', 'entra')),
    CHECK (subject <> '')
) STRICT, WITHOUT ROWID;

CREATE INDEX ix_user_identities_user ON user_identities (user_id);


-- The state a provider round trip is bound to.
--
-- Everything an OAuth callback must be checked against, held between the two
-- halves of the flow. It is a table rather than a signed cookie because
-- single-use has to be enforced by something that can refuse the second
-- attempt, and a self-contained token cannot: replaying a callback is one of
-- the two attacks this defends against, and the other -- a state the host
-- never issued -- is answered by there being no row.
CREATE TABLE sso_states (
    -- SHA-256 of the state parameter. The parameter travels in a URL, through
    -- the provider, and into whatever logs either end keeps; storing the
    -- digest means a leaked log is not a usable state.
    state_hash    TEXT    PRIMARY KEY,
    provider      TEXT    NOT NULL,
    -- 'signin' begins a session; 'link' attaches a provider to the account in
    -- user_id. They are separate because their callbacks do different things
    -- and a flow started for one must not be completed as the other.
    purpose       TEXT    NOT NULL,
    -- SHA-256 of the secret in the browser's own short-lived cookie. A state
    -- nobody can bind to the browser that started the flow is a state anybody
    -- can hand to anybody, which is how a person is signed in as an account
    -- they do not own without noticing.
    binding_hash  TEXT    NOT NULL,
    -- The account being linked to, for purpose='link'. NULL for a sign-in,
    -- where there is no account yet.
    user_id       TEXT    REFERENCES users(id) ON DELETE CASCADE,
    -- The PKCE verifier, and the OIDC nonce. Empty where the provider does
    -- not support the mechanism -- GitHub has neither an id token nor,
    -- historically, PKCE on every app -- rather than absent, so the row shape
    -- does not vary by provider.
    code_verifier TEXT    NOT NULL DEFAULT '',
    nonce         TEXT    NOT NULL DEFAULT '',
    -- The redirect_uri sent to the provider. Re-sent verbatim on exchange,
    -- which several providers require, and compared so a redirect base that
    -- changed mid-flow fails loudly instead of at the provider.
    redirect_uri  TEXT    NOT NULL,
    -- Where to send the browser once the flow finishes. A path within this
    -- dashboard, validated before it is stored; never an absolute URL.
    return_to     TEXT    NOT NULL DEFAULT '/',
    created_at    INTEGER NOT NULL,
    expires_at    INTEGER NOT NULL,
    -- Set by the guarded UPDATE that claims the state. A second callback
    -- carrying the same state matches zero rows and is refused.
    consumed_at   INTEGER,
    CHECK (purpose IN ('signin', 'link')),
    CHECK (provider IN ('google', 'github', 'entra')),
    -- A link with no account to link to is not a flow anybody can complete.
    CHECK (purpose <> 'link' OR user_id IS NOT NULL)
) STRICT, WITHOUT ROWID;

-- Housekeeping reads this; expiry is enforced in the WHERE clause of the claim
-- regardless, so a row left behind is untidy rather than dangerous.
CREATE INDEX ix_sso_states_expiry ON sso_states (expires_at);
