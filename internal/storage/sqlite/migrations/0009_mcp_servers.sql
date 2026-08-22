-- 0009_mcp_servers: remote MCP servers, and the tools they were last seen to
-- offer.
--
-- A remote server is a third party. Two consequences shape these tables.
--
-- The first is that boot must not depend on it being up. The tools a remote
-- server offers are therefore snapshotted here at discovery and mounted from
-- this snapshot, never from the network -- so an outage at the far end costs
-- an unhealthy indicator and failing calls, not a host that comes up with no
-- tools and a model that concludes the integration was removed.
--
-- The second is that nothing it says is authority. `tools/list` is a claim
-- about what exists, and the MCP specification is explicit that a tool's
-- annotations are untrusted. So a tool arrives here `pending` and is not
-- mounted until an administrator says otherwise, and what they classify is a
-- specific descriptor rather than a name: descriptor_hash is carried in the
-- guard on every state change, so a tool whose schema changed underneath an
-- approval cannot inherit it.

CREATE TABLE mcp_servers (
    -- The instance name, which is also the plugin name: the endpoint path
    -- segment, the tool prefix, and the entry in a credential's plugin list.
    -- Same rule as every other instance, so the two namespaces cannot collide.
    name           TEXT    PRIMARY KEY,
    -- The imported server.json, verbatim. Kept whole rather than shredded
    -- into columns because it is the operator's document: re-deriving the
    -- settings form from it after a schema change must produce what they
    -- imported, not what an earlier parser understood of it.
    document       TEXT    NOT NULL,
    -- The $schema URI the document declared. Recorded so a later import of a
    -- newer format is a visible difference rather than a silent reinterpretation.
    schema_version TEXT    NOT NULL,
    -- Resolved from the document's chosen remote. Only streamable-http is
    -- served today, and the CHECK below says so: the database refuses a
    -- transport this build has no code for, which is the guard that survives a
    -- write path skipping validation. SQLite cannot alter a CHECK, so adding
    -- sse means a migration that rebuilds this table -- which is the price of
    -- having the constraint at all, and worth it for a column that decides
    -- how mcpd talks to somebody else's server.
    transport      TEXT    NOT NULL,
    -- The URL template, still holding its {variables}. Substitution happens at
    -- dial time from resolved settings, because a variable may be a secret and
    -- a secret does not belong in a column beside the document.
    url            TEXT    NOT NULL,
    enabled        INTEGER NOT NULL DEFAULT 1,
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL,
    CHECK (enabled IN (0, 1)),
    CHECK (transport IN ('streamable-http'))
) STRICT, WITHOUT ROWID;

CREATE TABLE mcp_server_tools (
    server_name     TEXT    NOT NULL REFERENCES mcp_servers (name) ON DELETE CASCADE,
    -- The upstream name, unmodified. Normalising it would put a name in front
    -- of the model that the far end does not answer to.
    tool_name       TEXT    NOT NULL,
    -- The descriptor as it was last seen: name, description, input schema,
    -- annotations. This is what Register builds a tool from.
    descriptor      TEXT    NOT NULL,
    -- A hash of the descriptor, and the guard on every classification. An
    -- administrator approves a descriptor, not a name.
    descriptor_hash TEXT    NOT NULL,
    state           TEXT    NOT NULL,
    -- Why this tool cannot be enabled, when it cannot: an input schema that is
    -- not an object, a name outside the specification's charset, a qualified
    -- name too long to address. NULL means it is classifiable.
    --
    -- Recorded rather than dropped, so an operator looking for a tool that
    -- never appeared finds it here with the reason beside it.
    problem         TEXT,
    first_seen_at   INTEGER NOT NULL,
    last_seen_at    INTEGER NOT NULL,
    PRIMARY KEY (server_name, tool_name),
    CHECK (state IN ('pending', 'enabled', 'disabled'))
) STRICT, WITHOUT ROWID;

-- Register reads exactly one shape: the enabled tools of one server.
CREATE INDEX ix_mcp_server_tools_state ON mcp_server_tools (server_name, state);
