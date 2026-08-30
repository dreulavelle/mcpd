-- A window in which this host stops asking, and which closes on its own.
--
-- The problem it solves is written down in CLAUDE.md: "an approval that costs a
-- context switch is one people arrange not to need, and the arrangement they
-- reach for is a rule broader than the one they meant to write." Somebody
-- working through a change at two in the morning has exactly one lever today --
-- widen a standing rule -- and a widened rule has no expiry and is still there
-- next month.
--
-- So this is the narrower lever, and its defining property is that it cannot be
-- made permanent.

CREATE TABLE approval_bypasses (
    id         TEXT    PRIMARY KEY,
    created_at INTEGER NOT NULL,
    -- Bounded at write time and again at read time. There is deliberately no
    -- way to express "forever": an indefinite bypass is the failure this
    -- feature exists to prevent, not a configuration of it.
    expires_at INTEGER NOT NULL,
    created_by TEXT    NOT NULL,
    -- Why, in the operator's words. Not optional in spirit -- the whole point
    -- of a bounded window is that somebody can read back what it was for.
    reason     TEXT    NOT NULL DEFAULT '',
    -- Empty covers every plugin. A named one covers only that instance, which
    -- is the difference between "I am working on the firewall" and "stop
    -- asking me about anything".
    plugin     TEXT    NOT NULL DEFAULT '',
    -- The highest risk this window authorises. Never critical: the rule set
    -- refuses a critical ceiling because a level an operator can opt out of is
    -- not a level, and a temporary window is a weaker authority than a rule
    -- rather than a stronger one.
    ceiling    TEXT    NOT NULL,
    revoked_at INTEGER,
    revoked_by TEXT
) STRICT;

-- There is deliberately no counter column here for "how many changes this
-- window let through", even though that number is the point -- "stop asking me
-- for an hour" and "this approved nine changes nobody looked at" are the same
-- event described before and after, and only the second says what it cost.
--
-- It is a query instead. Every operation already records the authority that
-- approved it, and a bypass records itself as "bypass:<id>", so the count is a
-- SELECT against rows that must be right anyway. A column would need a write
-- on every bypassed approval, would need that write not to fail the approval
-- it describes, and could then disagree with the operations it claims to
-- count. One authority for the fact, not two.

-- What a bypass deliberately cannot do, enforced in Go rather than here
-- because it is a judgement about the request rather than about the row:
--
--   * override an exclusion. A rule that authorises nothing is somebody
--     writing "never" about a specific action, and a window opened to get
--     through an evening's work must not quietly cancel it.
--   * authorise an irreversible mutation. The argument for authorising
--     anything in advance is that a mistake is cheap to correct, and it does
--     not hold where there is no correction. A rule cannot do this; neither
--     can this.
--   * authorise a risk it does not name, or one this host does not recognise.

-- Reads are always "is anything in force right now", so the index is on the
-- window rather than on the key.
CREATE INDEX idx_approval_bypasses_active ON approval_bypasses(expires_at DESC, revoked_at);
