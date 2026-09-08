-- What every tool has ever done, kept for good.
--
-- Three places already count tool calls and none of them can answer "is this
-- plugin faster than it was last month". The Prometheus registry is in process
-- memory, so it starts at zero on every restart and the Performance page it
-- feeds shows only what has happened since. The `tool_calls` ledger is durable
-- but pruned on `calls.retention_days`, because it holds a principal and a
-- correlation id per row and keeping those for ever is a liability rather than
-- an asset. So the two facts an operator wants over a long horizon -- how much,
-- how fast -- are the two nothing keeps.
--
-- This does. One row per hour per {plugin, tool, outcome}, holding sums rather
-- than rows, and nothing prunes it: an hour of a busy host is a few hundred
-- bytes, and a year of thirty tools is smaller than one day of raw calls. It
-- carries no principal, no arguments and no correlation id, which is what makes
-- keeping it for ever reasonable in the first place.
--
-- Percentiles are why the bucket columns exist. A p95 cannot be recovered from
-- a mean, and averaging the p95s of twenty-four hours gives a number that is
-- not the p95 of the day. Counting each call into a fixed latency bucket lets
-- any span be summed and a quantile read back off the totals, the way the
-- histogram this replaces already did.
CREATE TABLE tool_call_stats (
    -- Start of the hour, in milliseconds, matching every other timestamp here.
    bucket        INTEGER NOT NULL,
    plugin        TEXT    NOT NULL,
    tool          TEXT    NOT NULL,
    -- ok | error | denied | rate_limited, the same four the ledger records.
    outcome       TEXT    NOT NULL,

    -- Every call, including the ones that never ran.
    calls         INTEGER NOT NULL DEFAULT 0,

    -- Only the calls that were actually timed. A refusal by the gate or a rate
    -- limit reached no handler, so it is counted above and nowhere below --
    -- the same distinction the ledger's nullable duration_us draws. Dividing
    -- duration_sum by `calls` rather than by `timed` would report a host that
    -- refuses a lot as a host that is fast.
    timed         INTEGER NOT NULL DEFAULT 0,
    duration_sum  INTEGER NOT NULL DEFAULT 0,
    duration_min  INTEGER,
    duration_max  INTEGER,

    -- Latency, in microseconds, counted into fixed buckets. Not cumulative:
    -- each column holds the calls that fell in that band alone, so summing a
    -- span is addition and needs no subtraction to undo an overlap.
    le_1ms        INTEGER NOT NULL DEFAULT 0,
    le_5ms        INTEGER NOT NULL DEFAULT 0,
    le_25ms       INTEGER NOT NULL DEFAULT 0,
    le_100ms      INTEGER NOT NULL DEFAULT 0,
    le_500ms      INTEGER NOT NULL DEFAULT 0,
    le_2s         INTEGER NOT NULL DEFAULT 0,
    le_10s        INTEGER NOT NULL DEFAULT 0,
    over_10s      INTEGER NOT NULL DEFAULT 0,

    -- How large the answers were, in bytes of marshalled result. Measured only
    -- for a call that produced one, so `sized` is its own denominator for the
    -- same reason `timed` is. The wire cost is about twice this: the protocol
    -- carries a result as structured content and again as text, and a page
    -- reporting context cost has to say so rather than quietly halving it.
    sized         INTEGER NOT NULL DEFAULT 0,
    bytes_sum     INTEGER NOT NULL DEFAULT 0,
    bytes_max     INTEGER,

    PRIMARY KEY (bucket, plugin, tool, outcome)
) STRICT;

-- Reading is almost always "this span, everything in it", so the hour leads.
CREATE INDEX idx_tool_call_stats_bucket ON tool_call_stats(bucket DESC);

-- And "this tool, over time", for a scorecard or a comparison between builds.
CREATE INDEX idx_tool_call_stats_tool ON tool_call_stats(plugin, tool, bucket DESC);

-- Seed from the ledger, so a host that has been running does not start at
-- zero. Those rows are pruned on their own schedule and would otherwise be
-- thrown away without ever having been counted into anything permanent; this
-- is the one moment both exist. Hosts with an empty ledger insert nothing.
INSERT INTO tool_call_stats (
    bucket, plugin, tool, outcome,
    calls, timed, duration_sum, duration_min, duration_max,
    le_1ms, le_5ms, le_25ms, le_100ms, le_500ms, le_2s, le_10s, over_10s
)
SELECT
    -- Truncate to the hour the rollup is keyed on. Integer division floors,
    -- and `at` is milliseconds since the epoch, so this is the hour's start.
    (at / 3600000) * 3600000,
    plugin, tool, outcome,
    COUNT(*),
    COUNT(duration_us),
    COALESCE(SUM(duration_us), 0),
    MIN(duration_us),
    MAX(duration_us),
    COUNT(CASE WHEN duration_us <=     1000 THEN 1 END),
    COUNT(CASE WHEN duration_us >     1000 AND duration_us <=     5000 THEN 1 END),
    COUNT(CASE WHEN duration_us >     5000 AND duration_us <=    25000 THEN 1 END),
    COUNT(CASE WHEN duration_us >    25000 AND duration_us <=   100000 THEN 1 END),
    COUNT(CASE WHEN duration_us >   100000 AND duration_us <=   500000 THEN 1 END),
    COUNT(CASE WHEN duration_us >   500000 AND duration_us <=  2000000 THEN 1 END),
    COUNT(CASE WHEN duration_us >  2000000 AND duration_us <= 10000000 THEN 1 END),
    COUNT(CASE WHEN duration_us > 10000000 THEN 1 END)
FROM tool_calls
GROUP BY 1, plugin, tool, outcome;
