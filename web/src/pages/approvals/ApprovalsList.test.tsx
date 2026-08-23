import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import { api, type Operation } from "@/lib/api";
import { renderWith } from "@/test/render";
import { ApprovalsList } from "./ApprovalsList";

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
    verified: true,
    assurance: "reviewed_change",
    drift_checked: true,
    outcome_verifiable: true,
    ...overrides,
  };
}

function mount(operations: Operation[]) {
  vi.spyOn(api, "operations").mockResolvedValue({
    operations, count: operations.length,
  });
  return renderWith(<ApprovalsList />);
}

/**
 * A standing rule approving a change is not somebody clicking Approve.
 *
 * The record says `approved_by: "system:policy"`, which is not an account and
 * has no name behind it. A list that rendered that field would say a person
 * decided, and nobody did -- so the list reads `authorized_by_rule` instead
 * and names the rule.
 */
describe("a change a rule authorised", () => {
  it("says a rule authorised it in advance, and names the rule", async () => {
    mount([operation({
      approved_by: "system:policy",
      approved_at: "2026-08-22T09:00:01Z",
      authorized_by_rule: "routine-radio",
    })]);

    expect(await screen.findByText(/No one was asked/i))
      .toBeInTheDocument();
    expect(screen.getByText("routine-radio")).toBeInTheDocument();
    // The approver field is never the thing rendered. "system:policy" on a row
    // reads as an account that pressed a button.
    expect(screen.queryByText(/system:policy/)).not.toBeInTheDocument();
  });

  it("says nothing of the sort about a change a person approved", async () => {
    mount([operation({
      approved_by: "user:alice@example.com",
      approved_at: "2026-08-22T09:00:01Z",
    })]);

    await screen.findByText("radio channel set");
    expect(screen.queryByText(/No one was asked/i)).not.toBeInTheDocument();
  });

  // Assurance says what can be proved; the rule says who authorised it. An
  // auto-approved change can carry every proof, so the two are separate chips
  // and neither implies the other.
  it("keeps the rule separate from what the record proves", async () => {
    mount([operation({
      authorized_by_rule: "routine-radio",
      assurance: "gated_call",
      drift_checked: false,
      outcome_verifiable: false,
    })]);

    expect(await screen.findByText("Gated call")).toBeInTheDocument();
    expect(screen.getByText(/No one was asked/i)).toBeInTheDocument();
  });

  it("shows the rule on a reviewed change too, where no assurance chip appears", async () => {
    mount([operation({ authorized_by_rule: "routine-radio" })]);

    expect(await screen.findByText(/No one was asked/i))
      .toBeInTheDocument();
    expect(screen.queryByText("Gated call")).not.toBeInTheDocument();
  });
});
