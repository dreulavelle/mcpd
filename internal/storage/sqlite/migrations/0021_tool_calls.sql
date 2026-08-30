-- Who called what.
--
-- mcpd could already answer "how often was this tool called" from the metrics,
-- and "who approved this change" from the audit trail. It could not answer
-- "who called this", which is the question an incident actually asks and a
-- third of what this host exists to report.
--
-- The metric is labelled {plugin, tool, outcome} and deliberately carries no
-- principal: a Prometheus label with one value per credential is unbounded
-- cardinality, and the series would multiply until the endpoint fell over. The
-- audit trail is for administrative acts and mutations, inside the transaction
-- that made them. Neither is the right home for one row per read, so this is.

CREATE TABLE tool_calls (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    -- Milliseconds, matching every other timestamp here.
    at            INTEGER NOT NULL,
    -- Who made the call, as the principal reads elsewhere: "user:someone",
    -- "svc:chatgpt", "key:abc123". Never a credential, and never a token.
    principal     TEXT    NOT NULL,
    -- The role the principal held at the time. Recorded rather than joined,
    -- because a grant that changes later must not rewrite what a past call was
    -- permitted under -- the trail would then describe a system that never ran.
    role          TEXT    NOT NULL DEFAULT '',
    plugin        TEXT    NOT NULL,
    tool          TEXT    NOT NULL,
    -- ok | error | denied | rate_limited, the same four the metric uses. A
    -- refusal is as much a fact about who reached for what as a success is,
    -- and dropping them would hide exactly the calls worth reading about.
    outcome       TEXT    NOT NULL,
    -- Microseconds, and nullable. Two separate facts that a single number
    -- would collapse: NULL is "this call never ran, so nothing was timed" --
    -- a refusal by the gate or a rate limit -- and a value is a measurement.
    -- Zero would mean both, and an in-process plugin answering in 63us really
    -- does round to zero milliseconds, so the ambiguity is not hypothetical.
    -- The same distinction the executor draws between a check that did not
    -- happen and one that happened and found nothing.
    duration_us   INTEGER,
    -- The id the caller was given in a header and in any error body. It is the
    -- only thing somebody on a machine you cannot reach can quote back, and
    -- without it here a support call cannot cross from a log line to this row.
    correlation_id TEXT   NOT NULL DEFAULT ''
) STRICT;

-- What is deliberately absent: arguments and results.
--
-- The logging rule already draws this line -- what was asked, what was
-- decided, what the upstream said, never a response body or a query's
-- arguments -- and a ledger is exactly where somebody would be tempted to
-- cross it. A row here says a named principal called a named tool and how it
-- ended. It does not say what they searched for, and it must not learn to.

-- Reading is always "recently, and narrowed", so time leads every index.
CREATE INDEX idx_tool_calls_at ON tool_calls(at DESC);
CREATE INDEX idx_tool_calls_principal ON tool_calls(principal, at DESC);
CREATE INDEX idx_tool_calls_plugin ON tool_calls(plugin, at DESC);

-- Retention is a setting and pruning is by time, so the plain at DESC index
-- above serves the delete as well as the reads.
