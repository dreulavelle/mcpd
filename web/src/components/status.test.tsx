import { describe, expect, it } from "vitest";
import { screen } from "@testing-library/react";
import type { OperationState } from "@/lib/api";
import { renderWith } from "@/test/render";
import { StateBadge, stateTone } from "./status";

/*
 * `VerifiedBadge` was here, and it is gone: nothing rendered it once the
 * approvals list stopped being a table and the detail page stopped repeating
 * the lifecycle's proofs. The tri-state rule it defended is defended in the
 * two places that now say it -- `confirmationWord` in lib/format.test.ts, and
 * the result proof in pages/approvals/OperationDetail.test.tsx, which asserts
 * three different sentences for true, false and absent.
 */

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
