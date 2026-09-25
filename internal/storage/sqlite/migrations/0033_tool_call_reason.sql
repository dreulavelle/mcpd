-- Why a call that did not succeed failed, in the words its caller was given.
--
-- The ledger said a call failed and nothing else: the Activity page and a
-- tunnel's recent calls showed "error" or "denied" with no way to learn why
-- short of finding the correlation id in a log that may since have rotated.
-- The reason is the text the assistant itself received -- the tool's error,
-- or the gate's refusal -- so it holds nothing the caller did not already
-- see, and it lives and is pruned with the rest of the ledger. Empty for a
-- call that succeeded, and for every call recorded before this.
ALTER TABLE tool_calls ADD COLUMN reason TEXT NOT NULL DEFAULT '';
