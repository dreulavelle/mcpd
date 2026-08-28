-- 0019_mcp_server_headers: headers an operator adds to a remote MCP server
-- that its published document never declared.
--
-- Until now every credential a remote server could be sent was derived from
-- its server.json. That is the right source when the document is complete, and
-- it is the only source, which is the problem: roughly a third of the remote
-- servers published to the official registry declare no headers and no
-- variables at all, and most of those answer 401. Their documents are not
-- wrong about needing a credential -- they are silent, and a host that can
-- only send what a document declared can never talk to any of them.
--
-- So this is the operator's half of the same sentence. The publisher says what
-- the server offers; the operator says what this host must send to reach it.
--
-- Only the *declaration* lives here -- the header's name and whether it
-- carries a secret. The value does not: it goes into `settings` under the key
-- the declaration derives, which is where every other plugin credential
-- already lives and is already encrypted at rest, redacted on read back and
-- reconnected on change. Storing it twice would mean two authorities for one
-- credential, and the second one would be the one nobody remembered to rotate.
--
-- A table rather than a column of JSON on mcp_servers, for the reason
-- chatgpt_accounts is a table: headers are a collection, and a JSON array in a
-- column is a table with the constraints left out. Here a header name can be
-- unique per server, a name can be NOT NULL, and removing one is a guarded
-- DELETE rather than a read-modify-write of somebody else's document.
--
-- The document itself is never rewritten. mcp_servers.document stays verbatim
-- as imported, because an operator's re-export must be what they imported --
-- these rows are merged onto it on the way to building a client, and are
-- visibly the host's own addition rather than an edit smuggled into a
-- publisher's file.
CREATE TABLE mcp_server_headers (
    server_name TEXT    NOT NULL REFERENCES mcp_servers (name) ON DELETE CASCADE,
    -- The HTTP header name, exactly as it goes on the wire. Checked against
    -- the same pattern the document path uses before anything is stored, so a
    -- name that could not be sent cannot be saved.
    name        TEXT    NOT NULL,
    -- Shown beside the field on the settings page. The operator is writing
    -- this for whoever fills the value in later, who may not be them.
    description TEXT    NOT NULL DEFAULT '',
    -- Whether the value is a credential. Default 1 because that is what this
    -- table exists for, and because the failure directions are not
    -- symmetrical: a non-secret marked secret is merely inconvenient to read
    -- back, and a secret marked non-secret is a token rendered in a form field
    -- to anybody who can open the page.
    is_secret   INTEGER NOT NULL DEFAULT 1,
    -- Whether the server is unreachable without it. Default 1, because a
    -- header added by hand is one somebody added to make a 401 stop.
    is_required INTEGER NOT NULL DEFAULT 1,
    created_by  TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    -- One declaration per header per server. Two rows naming the same header
    -- would derive the same settings key, and the second would silently
    -- decide what the first one asked for.
    PRIMARY KEY (server_name, name),
    CHECK (name <> ''),
    CHECK (length(name) <= 128),
    CHECK (length(description) <= 512),
    CHECK (is_secret IN (0, 1)),
    CHECK (is_required IN (0, 1))
) STRICT;
