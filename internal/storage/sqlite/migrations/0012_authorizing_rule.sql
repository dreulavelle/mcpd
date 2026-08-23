-- 0012_authorizing_rule: record what authorised a change nobody was asked about.
--
-- Risk was computed on every proposal, stored, and displayed, and no code
-- consulted it to decide whether to interrupt anybody. Every mutation was
-- equally consequential as far as the gate was concerned, which is how a gate
-- becomes an obstacle and an obstacle becomes something people route around.
--
-- A standing rule can now authorise a class of change in advance. Nothing else
-- about the operation changes: the row is written, the payload is frozen and
-- hashed, plan/apply/observe runs, drift is checked, the outcome is verified
-- where the mutation can prove one, and every transition is in the audit chain.
-- What is skipped is the interruption, and only that.
--
-- Which means the record has to say what authorised it. "Auto-approved" with
-- nothing naming the rule is precisely the unprovable approval this schema
-- exists to prevent, so the rule's identifier lives on the row beside
-- approved_by, written in the same guarded UPDATE as the approval itself.
--
-- NULL is the correct value for every existing row and for every future
-- approval a person makes: this column answers "which rule", and where a human
-- decided there was no rule. It is deliberately not a boolean with a separate
-- name column, because the two could disagree.
ALTER TABLE operations ADD COLUMN authorized_by_rule TEXT;

-- Once written it is history. The approval it accompanies is already immutable
-- in practice -- approved_by is set by a guarded transition out of
-- pending_approval and never revisited -- but this is the field a later reader
-- trusts to say who authorised the change, and a value that could be rewritten
-- afterwards would let that account be edited after the fact.
--
-- Clearing it is refused for the same reason as changing it. An operation that
-- was authorised by a rule cannot become one a person approved.
CREATE TRIGGER trg_operations_rule_immutable
BEFORE UPDATE OF authorized_by_rule ON operations
FOR EACH ROW WHEN OLD.authorized_by_rule IS NOT NULL
              AND NEW.authorized_by_rule IS NOT OLD.authorized_by_rule
BEGIN
    SELECT RAISE(ABORT, 'the rule that authorised an operation cannot be changed');
END;
