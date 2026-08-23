import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import { api, type Operation, type OperationState } from "@/lib/api";
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
    ...overrides,
  };
}

function mount(op: Operation, role: "user" | "admin") {
  vi.spyOn(api, "operation").mockResolvedValue({ operation: op, audit: [] });
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
