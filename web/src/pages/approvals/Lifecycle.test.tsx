import { describe, expect, it } from "vitest";
import { screen } from "@testing-library/react";
import type { AuditRecord, Operation, OperationState } from "@/lib/api";
import { renderWith } from "@/test/render";
import { Lifecycle, lifecycle } from "./Lifecycle";

function operation(overrides: Partial<Operation> = {}): Operation {
  return {
    id: "op-1",
    plugin: "cnmaestro",
    action: "device.reboot",
    state: "pending_approval",
    risk: "high",
    impact: "Reboots one access point.",
    requested_by: "svc:assistant",
    requested_at: "2026-08-22T09:00:00Z",
    expires_at: "2026-08-22T10:00:00Z",
    attempts: 0,
    terminal: false,
    assurance: "reviewed_change",
    drift_checked: true,
    outcome_verifiable: true,
    ...overrides,
  };
}

function mount(op: Operation, audit: AuditRecord[] = []) {
  return renderWith(<Lifecycle operation={op} audit={audit} />);
}

function nodeStatus(container: HTMLElement, state: OperationState): string {
  return container.querySelector(`[data-node="${state}"]`)?.getAttribute("data-status") ?? "";
}

/**
 * The reachability rules, without a DOM.
 *
 * They mirror the transition table in `internal/operations/state_machine.go`,
 * and getting them wrong is how a page tells an operator that something can
 * still happen when the server would refuse it.
 */
describe("reading the shape of one operation's life", () => {
  it("marks where it is, what it passed, and what is ahead", () => {
    const shape = lifecycle("approved");
    expect(shape.get("approved")).toBe("current");
    expect(shape.get("pending_approval")).toBe("past");
    expect(shape.get("executing")).toBe("ahead");
    expect(shape.get("succeeded")).toBe("ahead");
  });

  it("closes what can no longer happen", () => {
    const shape = lifecycle("rejected");
    expect(shape.get("rejected")).toBe("current");
    expect(shape.get("pending_approval")).toBe("past");
    // Turned down while waiting: it was never approved and never will be.
    expect(shape.get("approved")).toBe("closed");
    expect(shape.get("executing")).toBe("closed");
    expect(shape.get("succeeded")).toBe("closed");
  });

  /**
   * Indeterminate is not terminal. The outcome is unknown, which is resolvable
   * by observation, and a diagram that drew it as settled would be the visual
   * form of the mistake that invites a retry.
   */
  it("leaves an indeterminate outcome open", () => {
    const shape = lifecycle("indeterminate");
    expect(shape.get("indeterminate")).toBe("current");
    expect(shape.get("succeeded")).toBe("ahead");
    expect(shape.get("failed")).toBe("ahead");
  });

  it("settles a succeeded change completely", () => {
    const shape = lifecycle("succeeded");
    expect(shape.get("succeeded")).toBe("current");
    expect(shape.get("executing")).toBe("past");
    expect(shape.get("indeterminate")).toBe("closed");
  });

  /**
   * A state says only what it can prove. `expired` proves the change was
   * waiting at some point and says nothing about whether it was ever approved
   * -- the trail is what knows, when it has not been cleared.
   */
  it("takes the rest of the path from the trail when there is one", () => {
    expect(lifecycle("expired").get("approved")).toBe("closed");
    expect(lifecycle("expired", ["approved"]).get("approved")).toBe("past");
  });
});

describe("the diagram", () => {
  it("says where it is now, in a label a screen reader can read", () => {
    mount(operation({ state: "pending_approval" }));
    expect(screen.getByRole("img"))
      .toHaveAccessibleName(/waiting.*can still become/i);
  });

  it("draws the current state as current and the rest as themselves", () => {
    const { container } = mount(operation({ state: "executing" }));
    expect(nodeStatus(container, "executing")).toBe("current");
    expect(nodeStatus(container, "approved")).toBe("past");
    expect(nodeStatus(container, "rejected")).toBe("closed");
  });

  it("reads the path off the audit trail as well as off the state", () => {
    const { container } = mount(
      operation({ state: "expired", terminal: true }),
      [{
        seq: 4, at: "2026-08-22T09:30:00Z", kind: "operation.approved",
        actor: "user:alice", from_state: "pending_approval", to_state: "approved",
      }],
    );
    expect(nodeStatus(container, "approved")).toBe("past");
  });

  it("says nothing else can happen once it is settled", () => {
    mount(operation({ state: "succeeded", terminal: true, verified: true }));
    expect(screen.getByText(/Nothing else can happen/)).toBeInTheDocument();
  });

  /**
   * An indeterminate outcome is resolved by reading the target, never by
   * running it again. The wording is the guard against the retry.
   */
  it("offers observation rather than a retry on an indeterminate outcome", () => {
    mount(operation({ state: "indeterminate", attempts: 1 }));
    expect(screen.getByText(/Reading the target upstream settles it as:/))
      .toBeInTheDocument();
  });
});

/**
 * `verified` has three values, and the third is the one that matters. Absent
 * means nobody has checked, which is the ordinary state of anything still in
 * flight -- rendering it as a tick would be wrong most of the time it appeared.
 */
describe("the outcome proof", () => {
  it("confirms only what was re-read and matched", () => {
    mount(operation({ state: "succeeded", terminal: true, verified: true }));
    expect(screen.getByText("Confirmed")).toBeInTheDocument();
  });

  it("says a re-read that did not match, which is not the same as unchecked", () => {
    mount(operation({ state: "succeeded", terminal: true, verified: false }));
    expect(screen.getByText("Did not match")).toBeInTheDocument();
  });

  it("never renders an absent check as a tick", () => {
    mount(operation({ state: "succeeded", terminal: true, verified: null }));
    expect(screen.getByText("Never checked")).toBeInTheDocument();
    expect(screen.queryByText("Confirmed")).not.toBeInTheDocument();
  });

  it("distinguishes unprovable from unchecked", () => {
    mount(operation({
      state: "succeeded", terminal: true, verified: null,
      outcome_verifiable: false, assurance: "gated_call",
    }));
    expect(screen.getByText("Not provable")).toBeInTheDocument();
    expect(screen.queryByText("Never checked")).not.toBeInTheDocument();
  });
});

/**
 * A drift check that never ran is not one that found nothing. Two absent
 * snapshots comparing equal is the shape of the bug.
 */
describe("the drift proof", () => {
  it("says a snapshot is held", () => {
    mount(operation({ drift_checked: true }));
    expect(screen.getByText("Snapshot held")).toBeInTheDocument();
  });

  it("says nothing was compared when none was declared", () => {
    mount(operation({ drift_checked: false, assurance: "gated_call" }));
    expect(screen.getByText("None declared")).toBeInTheDocument();
    expect(screen.getByText(/never ran/)).toBeInTheDocument();
  });
});

/**
 * A change nobody was asked about still went through the machine.
 *
 * It really did pass through waiting and really did become approved, so every
 * node is drawn as it stands. What the picture cannot say is that no person
 * was involved -- "approved" reads as somebody having approved it -- so the
 * caption says it, and says it plainly rather than as a fault, because an
 * authorisation given in advance is a legitimate route through.
 */
describe("a change a standing rule authorised", () => {
  it("says nobody was asked, and names the rule", () => {
    mount(operation({
      state: "succeeded", terminal: true, verified: true,
      approved_by: "system:policy", authorized_by_rule: "routine-radio",
    }));

    expect(screen.getByText("No one was asked.")).toBeInTheDocument();
    expect(screen.getByText("routine-radio")).toBeInTheDocument();
  });

  // The caption sits beside the picture; somebody reaching the picture through
  // its description would otherwise be the one person not told.
  it("puts it in the diagram's description too", () => {
    mount(operation({
      state: "succeeded", terminal: true,
      authorized_by_rule: "routine-radio",
    }));

    expect(screen.getByRole("img")).toHaveAccessibleName(
      /Rule routine-radio allowed it, so no one was asked/i,
    );
  });

  it("says nothing of the sort where a person decided", () => {
    mount(operation({
      state: "succeeded", terminal: true,
      approved_by: "user:alice@example.com",
    }));

    expect(screen.queryByText("No one was asked.")).not.toBeInTheDocument();
  });
});
