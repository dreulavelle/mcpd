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
-- Existing rows default to 1. Every mutation this build could already have
-- written declared a desired state and was in fact verified against it, so 1
-- is what those rows record rather than an assumption made about them.
ALTER TABLE operations ADD COLUMN verifiable INTEGER NOT NULL DEFAULT 1;

-- Frozen with the rest of the payload. What a mutation claims it can prove is
-- part of what was approved, and a value that could be flipped afterwards
-- would let the claim be rewritten after the decision was made.
CREATE TRIGGER trg_operations_verifiable_immutable
BEFORE UPDATE OF verifiable ON operations
FOR EACH ROW WHEN OLD.state <> 'draft'
BEGIN
    SELECT RAISE(ABORT, 'operation verifiability is immutable after submission');
END;
