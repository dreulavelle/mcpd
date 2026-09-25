-- What OpenAI said about each organisation and workspace an account pairs.
--
-- OpenAI verifies that the Platform organisation and the ChatGPT workspace
-- named on a tunnel belong together, and refuses a create it cannot verify
-- (tunnel_principal_association_unverified). The only remedy is a review by
-- OpenAI Support, and there is no endpoint that lists which pairings it will
-- accept: the one way to learn is to make a tunnel and see. An account whose
-- Check passed was refused at every Make, because the Check never named the
-- workspace -- and nothing kept the answer once somebody did find it.
--
-- One row per account and workspace, written by whatever last asked OpenAI:
-- saving the account, its Check, or a Make. A workspace with no row has not
-- been asked about, which is different from one that was and failed. The
-- organisation-only pairing is the empty workspace id.
--
-- A collection rather than keys in `settings`, for the reason the accounts
-- themselves are: the constraints -- one row per pair, a known status, gone
-- with the account -- are what make it a table.
CREATE TABLE chatgpt_pairings (
    account_id   TEXT    NOT NULL REFERENCES chatgpt_accounts(id) ON DELETE CASCADE,
    workspace_id TEXT    NOT NULL,
    -- verified: OpenAI made a tunnel naming this pair.
    -- unverified: OpenAI could not verify the pair belongs together; only its
    --   Support can review it, so asking again changes nothing until then.
    -- refused: OpenAI refused for another reason, such as the key's
    --   permissions, and a fixed key is worth another Check.
    status       TEXT    NOT NULL CHECK (status IN ('verified', 'unverified', 'refused')),
    -- The organisation the answer was about. An account whose organisation
    -- changes has asked a different question, so an answer about another
    -- organisation is not read as this one's.
    org_id       TEXT    NOT NULL,
    -- The sentence shown, the reason the dashboard branches on, and OpenAI's
    -- own words with the request id its Support looks a refusal up by.
    problem      TEXT    NOT NULL DEFAULT '',
    reason       TEXT    NOT NULL DEFAULT '',
    upstream     TEXT    NOT NULL DEFAULT '',
    checked_at   INTEGER NOT NULL,
    PRIMARY KEY (account_id, workspace_id)
);
