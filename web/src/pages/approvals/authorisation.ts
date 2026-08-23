import type { AuditRecord, Operation } from "@/lib/api";

/** What a rule said, as recorded at the moment it said it. */
export interface PolicyAuthorisation {
  /** The rule's id, from the operation. Always present. */
  rule: string;
  /** `plugin/action for principal`, as it stood. */
  scope?: string;
  maxRisk?: string;
  note?: string;
  reason?: string;
  /** False once the audit entry is gone, leaving only the id. */
  recorded: boolean;
}

function text(detail: unknown, key: string): string | undefined {
  if (typeof detail !== "object" || detail === null) return undefined;
  const value = (detail as Record<string, unknown>)[key];
  return typeof value === "string" && value !== "" ? value : undefined;
}

/**
 * How an operation came to be approved with nobody asked; null when a person
 * decided. From the audit entry, not the policy endpoint: the rule may change.
 */
export function policyAuthorisation(
  operation: Operation,
  audit: AuditRecord[],
): PolicyAuthorisation | null {
  const rule = operation.authorized_by_rule;
  if (!rule) return null;

  // Matched on the rule the operation names, not on kind alone: another
  // entry's scope under this rule's id is the mismatch the full record exists
  // to prevent.
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
