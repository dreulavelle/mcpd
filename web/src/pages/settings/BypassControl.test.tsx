import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, ApiError, type Bypass, type BypassStatus } from "@/lib/api";
import { renderWith, sessionFor } from "@/test/render";
import { BypassControl } from "./BypassControl";

function bypass(overrides: Partial<Bypass> = {}): Bypass {
  return {
    id: "byp_1",
    ceiling: "medium",
    reason: "migrating the edge switches",
    created_by: "user:someone",
    created_at: "2026-08-30T11:00:00Z",
    expires_at: "2026-08-30T12:00:00Z",
    active: true,
    seconds_left: 1800,
    approved: 0,
    ...overrides,
  };
}

function stub(status: Partial<BypassStatus> = {}) {
  vi.spyOn(api, "bypassStatus").mockResolvedValue({
    active: false, recent: [], max_minutes: 480, ...status,
  });
}

describe("stopping the asking for a while", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    window.localStorage.clear();
  });

  // The defining property, said on the page rather than only enforced in the
  // database: there is no option that means "forever".
  it("offers no window longer than the ceiling", async () => {
    stub();
    renderWith(<BypassControl />);

    const form = within(await screen.findByRole("form", { name: "Stop asking" }));
    const options = within(form.getByLabelText("For")).getAllByRole("option");
    const minutes = options.map((o) => Number((o as HTMLOptionElement).value));

    expect(Math.max(...minutes)).toBeLessThanOrEqual(480);
    expect(minutes.some((m) => Number.isNaN(m) || m <= 0)).toBe(false);
  });

  // Critical is refused by the rule set for the same reason, and a window is a
  // weaker authority than a rule rather than a stronger one.
  it("does not offer to authorise critical changes", async () => {
    stub();
    renderWith(<BypassControl />);

    const form = within(await screen.findByRole("form", { name: "Stop asking" }));
    const values = within(form.getByLabelText("Up to"))
      .getAllByRole("option")
      .map((o) => (o as HTMLOptionElement).value);

    expect(values).not.toContain("critical");
  });

  // A window with no stated purpose is a widened rule with a timer.
  it("will not open one without a reason", async () => {
    stub();
    renderWith(<BypassControl />);

    const form = within(await screen.findByRole("form", { name: "Stop asking" }));
    expect(form.getByRole("button", { name: "Stop asking" })).toBeDisabled();

    const user = userEvent.setup();
    await user.type(form.getByLabelText("What for"), "migrating the switches");
    expect(form.getByRole("button", { name: "Stop asking" })).toBeEnabled();
  });

  it("opens a window with what was chosen", async () => {
    stub();
    const open = vi.spyOn(api, "openBypass").mockResolvedValue(bypass());
    renderWith(<BypassControl />);

    const user = userEvent.setup();
    const form = within(await screen.findByRole("form", { name: "Stop asking" }));
    await user.selectOptions(form.getByLabelText("For"), "240");
    await user.selectOptions(form.getByLabelText("Up to"), "high");
    await user.type(form.getByLabelText("What for"), "migrating the switches");
    await user.click(form.getByRole("button", { name: "Stop asking" }));

    expect(open).toHaveBeenCalledWith({
      minutes: 240, ceiling: "high", reason: "migrating the switches",
    });
  });

  // What it cost is the half an operator needs afterwards. "Stop asking for an
  // hour" and "this approved nine changes nobody saw" are the same event
  // described before and after.
  it("says how many changes an open window has let through", async () => {
    stub({ active: true, current: bypass({ approved: 9 }) });
    renderWith(<BypassControl />);

    expect(await screen.findByText(/9 changes approved without anybody being asked/))
      .toBeInTheDocument();
  });

  it("says plainly when nothing has used it yet", async () => {
    stub({ active: true, current: bypass({ approved: 0 }) });
    renderWith(<BypassControl />);

    expect(await screen.findByText(/Nothing has used it yet/)).toBeInTheDocument();
  });

  it("offers a way to start asking again", async () => {
    stub({ active: true, current: bypass() });
    const revoke = vi.spyOn(api, "revokeBypasses").mockResolvedValue({ closed: 1 });
    renderWith(<BypassControl />);

    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Start asking again" }));
    expect(revoke).toHaveBeenCalled();
  });

  // An operator can see that something is open; only an administrator can
  // change it. Seeing is the half that matters for noticing.
  it("shows an operator the state without offering the controls", async () => {
    stub({ active: true, current: bypass() });
    renderWith(<BypassControl />, { session: sessionFor("user") });

    expect(await screen.findByText(/Open until/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Start asking again" }))
      .not.toBeInTheDocument();
  });

  it("reports what the host said when a window is refused", async () => {
    stub();
    vi.spyOn(api, "openBypass").mockRejectedValue(
      new ApiError(400, "too_long", "a bypass can last at most 480 minutes"),
    );
    renderWith(<BypassControl />);

    const user = userEvent.setup();
    const form = within(await screen.findByRole("form", { name: "Stop asking" }));
    await user.type(form.getByLabelText("What for"), "x");
    await user.click(form.getByRole("button", { name: "Stop asking" }));

    expect(await screen.findByText(/at most 480 minutes/)).toBeInTheDocument();
  });
});
