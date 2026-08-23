import { describe, expect, it } from "vitest";
import type { AuditRecord, Operation } from "@/lib/api";
import { policyAuthorisation } from "./authorisation";

function operation(overrides: Partial<Operation> = {}): Operation {
  return {
    id: "op-1",
    plugin: "cnmaestro",
    action: "radio.channel.set",
    state: "succeeded",
    risk: "low",
    impact: "Moves one radio to another channel.",
    requested_by: "svc:assistant",
    requested_at: "2026-08-22T09:00:00Z",
    expires_at: "2026-08-22T10:00:00Z",
    attempts: 1,
    terminal: true,
    assurance: "reviewed_change",
    drift_checked: true,
    outcome_verifiable: true,
    ...overrides,
  };
}

function approvedByPolicy(rule: string, detail: Record<string, unknown> = {}): AuditRecord {
  return {
    seq: 2,
    at: "2026-08-22T09:00:01Z",
    kind: "operation.approved",
    actor: "system:policy",
    operation_id: "op-1",
    from_state: "pending_approval",
    to_state: "approved",
    detail: {
      reason: `rule ${rule} (cnmaestro/* for *) authorises low changes up to low`,
      channel: "policy",
      rule,
      rule_scope: "cnmaestro/* for *",
      rule_max_risk: "low",
      rule_note: "a channel change is undone by another channel change",
      proposed_by: "svc:assistant",
      asked_a_person: false,
      ...detail,
    },
  };
}

/**
 * `authorized_by_rule` is the discriminator, not `approved_by`.
 *
 * An auto-approved operation's `approved_by` is `system:policy` and its
 * `approved_by_name` is the same string. Branching on either would call a
 * person's decision and a standing rule's the same thing, which is the
 * confusion the field exists to end.
 */
describe("reading how a change was authorised", () => {
  it("says nothing about a change a person approved", () => {
    const op = operation({ approved_by: "user:alice@example.com" });
    expect(policyAuthorisation(op, [])).toBeNull();
  });

  it("reads the rule's scope, ceiling and note out of the entry that authorised it", () => {
    const op = operation({
      approved_by: "system:policy",
      authorized_by_rule: "routine-radio",
    });

    expect(policyAuthorisation(op, [approvedByPolicy("routine-radio")])).toEqual({
      rule: "routine-radio",
      scope: "cnmaestro/* for *",
      maxRisk: "low",
      note: "a channel change is undone by another channel change",
      reason: "rule routine-radio (cnmaestro/* for *) authorises low changes up to low",
      recorded: true,
    });
  });

  // The trail can be cleared. The id is on the operation row and is immutable,
  // so it survives; what the rule covered does not, and the page has to be
  // able to tell the difference rather than rendering blanks as facts.
  it("keeps the rule's name when the trail no longer holds the entry", () => {
    const op = operation({ authorized_by_rule: "routine-radio" });
    expect(policyAuthorisation(op, [])).toEqual({
      rule: "routine-radio",
      recorded: false,
    });
  });

  // Matching on kind alone would attach whatever approval came first to this
  // operation's rule id. Recording the rule in full only helps if the entry
  // read back is the one that names it.
  it("does not borrow another rule's scope from an entry that names it", () => {
    const op = operation({ authorized_by_rule: "routine-radio" });
    const entry = approvedByPolicy("something-else");

    expect(policyAuthorisation(op, [entry])).toEqual({
      rule: "routine-radio",
      recorded: false,
    });
  });

  it("treats a missing note as absent rather than as an empty one", () => {
    const op = operation({ authorized_by_rule: "routine-radio" });
    const entry = approvedByPolicy("routine-radio", { rule_note: "" });

    expect(policyAuthorisation(op, [entry])?.note).toBeUndefined();
  });

  // A detail this build does not recognise must not throw. The audit payload
  // is `unknown` on purpose, and a page that failed to render because an entry
  // was a string would take the whole change with it.
  it("survives an entry whose detail is not an object", () => {
    const op = operation({ authorized_by_rule: "routine-radio" });
    const odd: AuditRecord = {
      seq: 1, at: "2026-08-22T09:00:01Z", kind: "operation.approved",
      actor: "system:policy", detail: "authorised",
    };

    expect(policyAuthorisation(op, [odd])).toEqual({
      rule: "routine-radio",
      recorded: false,
    });
  });
});
