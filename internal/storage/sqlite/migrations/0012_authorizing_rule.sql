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

-- Where the tamper-evidence actually lives, and why this column still needs a
-- trigger.
--
-- The authoritative record of what authorised a change is the audit entry:
-- audit_events is append-only and hash-chained, chainHash covers detail_json,
-- and the operation.approved entry carries the rule's id, scope, ceiling and
-- note inside that detail. Rewriting it breaks the chain and VerifyChain says
-- where. This column is a denormalised copy of that fact -- it exists so the
-- executor and the dashboard can read provenance from the row they already
-- have instead of walking the trail for every operation.
--
-- Putting the column itself inside the chain is not available: the chain is
-- over audit_events, one row per event, and operations rows are mutable by
-- design -- state, lease, attempt count and outcome all change after the fact.
-- A chain over a mutable row is not a chain. What is achievable, and what this
-- does, is to stop the copy from ever disagreeing with the chained original.
--
-- The first attempt guarded only a row that already carried a rule
-- (OLD.authorized_by_rule IS NOT NULL), which implemented half the property.
-- A human-approved operation holds NULL, so
--
--     UPDATE operations SET authorized_by_rule = 'fabricated-rule' WHERE id = ?
--
-- succeeded: the operation began reading as auto-approved, no audit event
-- recorded the change, and the hash chain still verified because the column is
-- outside it. Fabricating provenance was easier than editing it.
--
-- So the rule is not "it cannot be changed once set" but "it can only be set
-- by the approval it belongs to". A BEFORE UPDATE trigger sees OLD and NEW
-- together, which is enough to say so: the only write permitted is one that is
-- simultaneously moving the operation out of pending_approval into approved,
-- which is exactly the guarded statement in OperationStore.Transition. A bare
-- UPDATE of this column leaves state untouched, so OLD.state = NEW.state and
-- the guard fires. Setting it on an already-approved row fires. Renaming it
-- fires. Clearing it fires.
--
-- What this does not defend, and cannot: somebody writing the whole approving
-- statement by hand -- state, approved_by, approved_at and this column at once.
-- That is approving by direct SQL, it is equally available for approved_by
-- today, and no trigger on one column addresses it. The audit chain is what
-- catches that, because the approval it invents has no entry in it.
CREATE TRIGGER trg_operations_rule_set_only_at_approval
BEFORE UPDATE OF authorized_by_rule ON operations
FOR EACH ROW WHEN NEW.authorized_by_rule IS NOT OLD.authorized_by_rule
              AND NOT (OLD.authorized_by_rule IS NULL
                       AND OLD.state = 'pending_approval'
                       AND NEW.state = 'approved')
BEGIN
    SELECT RAISE(ABORT, 'the rule that authorised an operation may only be set by the approval itself');
END;
