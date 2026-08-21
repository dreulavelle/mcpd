-- 0005_idempotency_liveness: let a settled intent be proposed again.
--
-- Two mechanisms enforced idempotency and they disagreed about time.
--
-- idempotency_records carries an expires_at and a request hash. It is the one
-- that implements the actual semantic: replaying the same request inside the
-- TTL returns the original operation, and reusing the key with a different
-- body is refused.
--
-- ux_operations_idem carried neither. It was permanent and blind to state, so
-- once any operation existed for (plugin, action, key) no later one could ever
-- be inserted. After the record aged out, the pre-check in Propose found
-- nothing and waved the proposal through, and this index refused the insert --
-- reported as "idempotency key reused with a different payload", which was not
-- what had happened.
--
-- The constraint that was actually wanted is one *live* operation per intent.
-- Proposing "set label to hello" again after the last attempt expired, was
-- rejected, cancelled, or succeeded and was since undone is an ordinary thing
-- to want, and the record's TTL already governs how long an identical replay
-- collapses onto the original.
--
-- Indeterminate is deliberately counted as live. OperationState.IsTerminal
-- excludes it because it is resolvable by observation, and a second attempt at
-- an intent that may already have taken effect is exactly the case that should
-- stay blocked.

DROP INDEX ux_operations_idem;

CREATE UNIQUE INDEX ux_operations_idem
    ON operations (plugin, action, idempotency_key)
    WHERE state IN ('draft', 'pending_approval', 'approved', 'executing', 'indeterminate');
