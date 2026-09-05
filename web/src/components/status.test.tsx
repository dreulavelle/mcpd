import { describe, expect, it } from "vitest";
import { screen } from "@testing-library/react";
import type { OperationState } from "@/lib/api";
import { renderWith } from "@/test/render";
import { StateBadge, stateTone, VerifiedBadge } from "./status";

/**
 * `verified` is a tri-state, and the third value is the one that gets lost.
 *
 * Absent means nobody re-read the target, which says nothing either way about
 * whether the change landed. An increment in flight makes it the common case,
 * so rendering it as a tick would be wrong most of the time it appeared.
 */
describe("VerifiedBadge", () => {
  const cases: [string, boolean | null | undefined, string][] = [
    ["confirmed by a re-read", true, "Confirmed"],
    ["checked and did not match", false, "Did not match"],
    ["explicitly null", null, "Not checked"],
    ["absent from the payload", undefined, "Not checked"],
  ];

  for (const [name, value, expected] of cases) {
    it(`says "${expected}" when ${name}`, () => {
      renderWith(<VerifiedBadge verified={value} />);
      expect(screen.getByText(expected)).toBeInTheDocument();
    });
  }

  // "Confirmed" rather than "Verified", so this badge, the lifecycle's proofs
  // and the approvals list all name one fact with one word.
  it("never claims verification for an unchecked outcome", () => {
    renderWith(<VerifiedBadge verified={undefined} />);
    expect(screen.queryByText("Confirmed")).not.toBeInTheDocument();
    expect(screen.queryByText("Verified")).not.toBeInTheDocument();
  });

  // The one that is not a bug but reads like one: "did not match" is a
  // finding, "not checked" is the absence of one, and collapsing them would
  // report a problem nobody has established.
  it("distinguishes a failed check from no check", () => {
    const { unmount } = renderWith(<VerifiedBadge verified={false} />);
    expect(screen.getByText("Did not match")).toBeInTheDocument();
    unmount();
    renderWith(<VerifiedBadge verified={null} />);
    expect(screen.queryByText("Did not match")).not.toBeInTheDocument();
    expect(screen.getByText("Not checked")).toBeInTheDocument();
  });
});

/**
 * Indeterminate is not a failure.
 *
 * It means execution began and the outcome was never recorded, so the change
 * may be in place. Painting it the same as `failed` is what leads somebody to
 * retry and apply it twice.
 */
describe("operation state", () => {
  it("gives indeterminate its own label, not the one failed has", () => {
    const { unmount } = renderWith(<StateBadge state="indeterminate" />);
    expect(screen.getByText("Unknown")).toBeInTheDocument();
    unmount();
    renderWith(<StateBadge state="failed" />);
    expect(screen.getByText("Didn't run")).toBeInTheDocument();
    expect(screen.queryByText("Unknown")).not.toBeInTheDocument();
  });

  it("does not colour indeterminate as a problem", () => {
    expect(stateTone("indeterminate")).toBe("attention");
    expect(stateTone("failed")).toBe("problem");
  });

  it("has a tone for every state the API can send", () => {
    const states: OperationState[] = [
      "draft", "pending_approval", "approved", "executing", "succeeded",
      "failed", "indeterminate", "rejected", "expired", "cancelled",
    ];
    for (const s of states) {
      const { unmount } = renderWith(<StateBadge state={s} />);
      // A state with no entry falls through to its raw name, which is the
      // signal that the table and the API have drifted apart.
      expect(screen.queryByText(s)).not.toBeInTheDocument();
      unmount();
    }
  });
});
