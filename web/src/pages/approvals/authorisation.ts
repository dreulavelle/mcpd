import type { AuditRecord, Operation } from "@/lib/api";

/**
 * What a standing rule said, as recorded at the moment it said it.
 *
 * Everything but `rule` comes from the audit entry rather than from the policy
 * endpoint, and that is the whole point of reading it here. The operation
 * carries only the rule's id; the rule itself can be edited or deleted
 * afterwards, so asking `GET /api/approval-policy` what "routine-radio" means
 * answers a question about now, not about the authorisation that actually
 * happened. The entry was written with the rule's scope, ceiling and note in
 * full precisely so it stays true on its own.
 */
export interface PolicyAuthorisation {
  /** The rule's id, from the operation. Always present. */
  rule: string;
  /** `plugin/action for principal`, as it stood. */
  scope?: string;
  /** The ceiling it authorised up to. */
  maxRisk?: string;
  /** The operator's own reason for the rule. */
  note?: string;
  /** The host's sentence about why this was automatic. */
  reason?: string;
  /**
   * Whether the trail still holds the entry.
   *
   * False when the audit has been cleared or the entry predates the fields.
   * The id is still true -- it is on the operation row and immutable -- so the
   * page says the rule's name and admits it cannot say what the rule was.
   */
  recorded: boolean;
}

function text(detail: unknown, key: string): string | undefined {
  if (typeof detail !== "object" || detail === null) return undefined;
  const value = (detail as Record<string, unknown>)[key];
  return typeof value === "string" && value !== "" ? value : undefined;
}

/**
 * Reads how an operation came to be approved with nobody asked.
 *
 * Null when a person decided, which is the case `authorized_by_rule` exists to
 * separate. `approved_by` cannot answer this: it is `system:policy` for every
 * automatic approval and an account for every other one, and a page that
 * rendered it alone would say "approved by system:policy", which reads as
 * somebody having clicked.
 *
 * Pure, and separate from the components, so the matching rule can be tested
 * without a DOM.
 */
export function policyAuthorisation(
  operation: Operation,
  audit: AuditRecord[],
): PolicyAuthorisation | null {
  const rule = operation.authorized_by_rule;
  if (!rule) return null;

  // Matched on the rule the operation names rather than on the first approval
  // in the trail. An operation has one approval, but the trail is a list this
  // page did not build, and picking by kind alone would attach a different
  // entry's scope to this rule's id -- exactly the mismatch recording the rule
  // in full was meant to prevent.
  const entry = audit.find(
    (r) => r.kind === "operation.approved" && text(r.detail, "rule") === rule,
  );
  if (!entry) return { rule, recorded: false };

  return {
    rule,
    scope: text(entry.detail, "rule_scope"),
    maxRisk: text(entry.detail, "rule_max_risk"),
    note: text(entry.detail, "rule_note"),
    reason: text(entry.detail, "reason"),
    recorded: true,
  };
}
