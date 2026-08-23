-- 0010_verifiable: let a mutation say whether it can prove its own outcome.
--
-- The executor settled every mutation as verified, whether or not anything was
-- compared. A mutation with no declared desired state short-circuited to
-- "verified", the operation recorded outcome_verified = 1, and the model was
-- told the change had been "confirmed by re-reading the target". Nothing had
-- been re-read. It was harmless only because every mutation this build ships
-- does declare one, which is exactly the kind of accident that stops being
-- harmless the moment somebody adds a write that cannot verify.
--
-- Verifiability is now declared by the mutation rather than inferred from an
-- empty field. An absent desired state is ambiguous on its own: for a delete
-- it means "the target should be gone", which is a real thing to observe, and
-- for a write that cannot be read back it means nothing at all.
--
-- It lives on the row because the executor reads every fact it acts on from
-- here rather than from the plugin it is about to run. When this is 0 the
-- executor performs no verification and leaves outcome_verified null -- "not
-- checked", which is a different fact from "checked and did not match".
--
-- Existing rows default to 1, then the ones that cannot have been verified are
-- corrected below. Most historical operations were genuinely verified: they
-- declared a desired state, and the executor compared it. Recording those as
-- unverifiable would rewrite settled history into "nobody checked", which is
-- the opposite lie to the one this migration exists to remove.
ALTER TABLE operations ADD COLUMN verifiable INTEGER NOT NULL DEFAULT 1;

-- The exception, and the reason a blanket default would not do. An
-- out-of-process plugin registering mutations and no tools mounted fine under
-- the old build -- the broken schema check lived in the tool path, not the
-- mutation path -- and one of its mutations returning an empty desired state
-- hit the short circuit and was settled outcome_verified = 1 having read
-- nothing. Those rows exist in principle, they are indistinguishable
-- afterwards, and blessing them here would have this migration reintroduce
-- the exact claim it removes.
--
-- An absent desired state is the signature: with nothing to compare against,
-- the old verify() returned true on the first pass without looking at the
-- target. This runs before the trigger below is created, because the trigger
-- fires on migration SQL too and would abort it.
UPDATE operations SET verifiable = 0
 WHERE desired_json IS NULL OR desired_json IN ('null','{}','[]');

-- Frozen with the rest of the payload. What a mutation claims it can prove is
-- part of what was approved, and a value that could be flipped afterwards
-- would let the claim be rewritten after the decision was made.
CREATE TRIGGER trg_operations_verifiable_immutable
BEFORE UPDATE OF verifiable ON operations
FOR EACH ROW WHEN OLD.state <> 'draft'
BEGIN
    SELECT RAISE(ABORT, 'operation verifiability is immutable after submission');
END;
