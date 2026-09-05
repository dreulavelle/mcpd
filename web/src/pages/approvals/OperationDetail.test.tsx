import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import { api, type AuditRecord, type Operation, type OperationState } from "@/lib/api";
import { renderWith, sessionFor } from "@/test/render";
import { OperationDetail } from "./OperationDetail";

function operationFixture(overrides: Partial<Operation> = {}): Operation {
  return {
    id: "op-1",
    plugin: "cnmaestro",
    action: "device.reboot",
    state: "pending_approval",
    risk: "high",
    impact: "Reboots one access point.",
    changes: [{ field: "power", from: "on", to: "cycled" }],
    requested_by: "svc:assistant",
    requested_at: "2026-08-22T09:00:00Z",
    expires_at: "2026-08-22T10:00:00Z",
    attempts: 0,
    terminal: false,
    // The full-proof case is the default, so a test that cares about the
    // weaker one has to say so rather than getting it by omission.
    assurance: "reviewed_change",
    drift_checked: true,
    outcome_verifiable: true,
    ...overrides,
  };
}

function mount(op: Operation, role: "user" | "admin", audit: AuditRecord[] = []) {
  vi.spyOn(api, "operation").mockResolvedValue({ operation: op, audit });
  return renderWith(<OperationDetail id={op.id} />, { session: sessionFor(role) });
}

describe("deciding on a change", () => {
  beforeEach(() => {
    vi.spyOn(api, "approve").mockResolvedValue(operationFixture({ state: "approved" }));
    vi.spyOn(api, "reject").mockResolvedValue(operationFixture({ state: "rejected" }));
    vi.spyOn(api, "cancel").mockResolvedValue(operationFixture({ state: "cancelled" }));
  });

  // Both roles carry approve. The test is that the control is gated on the
  // capability rather than on the role -- a build that checked `role ===
  // "admin"` would pass the admin case and fail this one.
  it("offers approve to a plain user, because a user carries the approve capability", async () => {
    mount(operationFixture(), "user");
    expect(await screen.findByRole("button", { name: "Approve" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Turn down" })).toBeInTheDocument();
  });

  it("offers approve to an administrator too", async () => {
    mount(operationFixture(), "admin");
    expect(await screen.findByRole("button", { name: "Approve" })).toBeInTheDocument();
  });

  it("offers withdraw, which needs only propose", async () => {
    mount(operationFixture(), "user");
    expect(await screen.findByRole("button", { name: "Withdraw" })).toBeInTheDocument();
  });

  const settled: OperationState[] = ["succeeded", "rejected", "expired", "cancelled"];
  for (const state of settled) {
    it(`offers no decision on a change that is ${state}`, async () => {
      mount(operationFixture({ state, terminal: true }), "admin");
      await screen.findByText("device reboot");
      expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Withdraw" })).not.toBeInTheDocument();
    });
  }

  it("does not offer approve once the change is already approved", async () => {
    mount(operationFixture({ state: "approved" }), "admin");
    await screen.findByText("device reboot");
    expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
    // Still withdrawable: it has not run yet.
    expect(screen.getByRole("button", { name: "Withdraw" })).toBeInTheDocument();
  });
});

describe("an indeterminate outcome", () => {
  it("is warned about as possibly-landed rather than reported as a failure", async () => {
    mount(
      operationFixture({ state: "indeterminate", attempts: 1, verified: null }),
      "admin",
    );

    expect(await screen.findByText(/This may have landed/i)).toBeInTheDocument();
    expect(screen.getByText(/a retry would apply the change a second time/i))
      .toBeInTheDocument();
    expect(screen.getByText("Not checked")).toBeInTheDocument();
  });

  it("reports a genuine failure differently", async () => {
    mount(
      operationFixture({
        state: "failed", terminal: true, attempts: 1,
        error_code: "upstream_refused", error_detail: "the controller said no",
      }),
      "admin",
    );

    expect(await screen.findByText(/It did not run/i)).toBeInTheDocument();
    expect(screen.queryByText(/This may have landed/i)).not.toBeInTheDocument();
  });
});

/**
 * "Reviewed change" and "gated call" are different words on purpose.
 *
 * The first carries exact fields, drift detection and a confirmed outcome; the
 * second carries a person's yes and nothing else. The console must not let the
 * second wear the first's name, and the operator has to be told which one they
 * are approving before they approve it.
 */
describe("what the record will prove", () => {
  it("says nothing extra when all three proofs are present", async () => {
    mount(operationFixture(), "admin");
    await screen.findByText("device reboot");
    expect(screen.getAllByText("Reviewed change").length).toBeGreaterThan(0);
    expect(screen.queryByText(/not a reviewed change/i)).not.toBeInTheDocument();
  });

  it("warns before the decision when the outcome cannot be confirmed", async () => {
    mount(
      operationFixture({
        assurance: "gated_call", drift_checked: true, outcome_verifiable: false,
      }),
      "admin",
    );

    expect(
      await screen.findByText(/This is a gated call, not a reviewed change/i),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/cannot be confirmed by reading the system again/i),
    ).toBeInTheDocument();
  });

  it("names both missing proofs when neither is held", async () => {
    mount(
      operationFixture({
        assurance: "gated_call", drift_checked: false, outcome_verifiable: false,
      }),
      "admin",
    );

    // Matched on the notice's own phrasing. The Record section states the same
    // two facts in its own words further down, which is the point -- one warns
    // before the decision, the other documents afterwards.
    await screen.findByText(
      /nothing recorded how the system looked before, so nothing will be compared, and its result cannot be confirmed by reading the system again/i,
    );
  });

  // A drift check that never ran is not one that passed. The record has to say
  // which, exactly as `verified` distinguishes unchecked from mismatched.
  it("distinguishes a drift check that did not run from one that found nothing", async () => {
    const { unmount } = mount(operationFixture({ drift_checked: false, assurance: "gated_call" }), "admin");
    expect(
      await screen.findByText(/Nothing recorded how it looked before, so nothing was compared/i),
    ).toBeInTheDocument();
    unmount();

    mount(operationFixture({ drift_checked: true }), "admin");
    expect(
      await screen.findByText(/compared against a record of how the system looked before/i),
    ).toBeInTheDocument();
  });
});

const POLICY_APPROVAL: AuditRecord = {
  seq: 2,
  at: "2026-08-22T09:00:01Z",
  kind: "operation.approved",
  actor: "system:policy",
  operation_id: "op-1",
  from_state: "pending_approval",
  to_state: "approved",
  detail: {
    reason: "rule routine-radio (cnmaestro/* for *) authorises low changes up to low",
    channel: "policy",
    rule: "routine-radio",
    rule_scope: "cnmaestro/* for *",
    rule_max_risk: "low",
    rule_note: "a channel change is undone by another channel change",
    proposed_by: "svc:assistant",
    asked_a_person: false,
  },
};

/**
 * Nobody clicked, and the page must not say otherwise.
 *
 * `approved_by` is `system:policy` on one of these and `approved_by_name` is
 * the same string -- not an account, nothing to resolve. The discriminator is
 * `authorized_by_rule`, and the scope and ceiling come from the audit entry
 * rather than from the policy endpoint, because the rule may have been edited
 * or deleted since and the entry is what actually authorised this change.
 */
describe("a change a standing rule authorised", () => {
  const autoApproved = () => operationFixture({
    state: "succeeded", terminal: true, verified: true, attempts: 1,
    approved_by: "system:policy",
    approved_at: "2026-08-22T09:00:01Z",
    authorized_by_rule: "routine-radio",
  });

  it("names the rule instead of rendering the approver field", async () => {
    mount(autoApproved(), "admin", [POLICY_APPROVAL]);

    expect(await screen.findByRole("heading", { name: "Approved by a rule" }))
      .toBeInTheDocument();
    expect(screen.getByText("Approved by")).toBeInTheDocument();
    expect(screen.queryByText("Decided by")).not.toBeInTheDocument();

    // The audit trail still says `system:policy`, because that is literally
    // what the entry says and the trail is the record rather than a rendering
    // of it. What must not exist is a second place saying it -- a "Decided by"
    // field, which reads as an account having pressed a button.
    const mentions = screen.getAllByText("system:policy");
    expect(mentions).toHaveLength(1);
    expect(mentions[0]!.closest("table")).not.toBeNull();
  });

  it("reads the scope, ceiling and note out of the entry that authorised it", async () => {
    mount(autoApproved(), "admin", [POLICY_APPROVAL]);

    expect(await screen.findByText("cnmaestro/* for *")).toBeInTheDocument();
    expect(screen.getByText("Up to")).toBeInTheDocument();
    expect(
      screen.getByText("a channel change is undone by another channel change"),
    ).toBeInTheDocument();
  });

  // The trail can be cleared. The id is on the operation row and survives;
  // what the rule covered does not, and inventing it from the policy endpoint
  // would describe today's rule rather than the authorisation that happened.
  it("admits it cannot show the scope once the trail is gone", async () => {
    mount(autoApproved(), "admin", []);

    expect(await screen.findByRole("heading", { name: "Approved by a rule" }))
      .toBeInTheDocument();
    expect(screen.getByText(/audit entry for this is gone/i)).toBeInTheDocument();
    expect(screen.queryByText("cnmaestro/* for *")).not.toBeInTheDocument();
  });

  it("still says a person decided where one did", async () => {
    mount(
      operationFixture({
        state: "succeeded", terminal: true, verified: true,
        approved_by: "user:alice@example.com",
        approved_at: "2026-08-22T09:00:01Z",
      }),
      "admin",
    );

    expect(await screen.findByText("Decided by")).toBeInTheDocument();
    expect(screen.getByText("user:alice@example.com")).toBeInTheDocument();
    expect(screen.queryByText(/No one was asked/i)).not.toBeInTheDocument();
  });

  // Assurance is orthogonal: an auto-approved change can carry every proof,
  // and a gated call that a rule authorised was still not approved by anyone.
  it("does not claim a person authorised a gated call a rule let through", async () => {
    mount(
      operationFixture({
        state: "succeeded", terminal: true, attempts: 1,
        assurance: "gated_call", drift_checked: false, outcome_verifiable: false,
        approved_by: "system:policy",
        authorized_by_rule: "routine-radio",
      }),
      "admin",
      [POLICY_APPROVAL],
    );

    expect(
      await screen.findByText(/A rule allowed it and the call was made/i),
    ).toBeInTheDocument();
    expect(
      screen.queryByText(/Approving it records that a person authorised it/i),
    ).not.toBeInTheDocument();
  });
});
