import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import { api, type Bypass } from "@/lib/api";
import { renderWith, sessionFor } from "@/test/render";
import { BypassBanner } from "./BypassBanner";

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
    approved: 3,
    ...overrides,
  };
}

/**
 * The whole risk of a bypass is somebody opening one, being pulled away, and
 * nobody remembering it is open. So the banner is on every page rather than on
 * the one where a window is opened.
 */
describe("the standing warning about an open window", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    window.localStorage.clear();
  });

  it("says nothing when nothing is open", async () => {
    vi.spyOn(api, "bypassStatus").mockResolvedValue({ active: false, max_minutes: 480 });
    const { container } = renderWith(<BypassBanner />);

    // Nothing to assert by text, so assert the absence of the warning itself.
    await new Promise((r) => setTimeout(r, 0));
    expect(container.textContent).not.toMatch(/without asking anyone/);
  });

  it("warns, and says what the window has cost so far", async () => {
    vi.spyOn(api, "bypassStatus").mockResolvedValue({
      active: true, open: 1, current: bypass(), max_minutes: 480,
    });
    renderWith(<BypassBanner />);

    expect(await screen.findByText(/without asking anyone/)).toBeInTheDocument();
    expect(screen.getByText(/3 so far/)).toBeInTheDocument();
  });

  // Two windows scoped to different plugins are not comparable, so the banner
  // shows the widest and has to say the others exist.
  it("says when more than one window is open", async () => {
    vi.spyOn(api, "bypassStatus").mockResolvedValue({
      active: true, open: 2, current: bypass(), max_minutes: 480,
    });
    renderWith(<BypassBanner />);

    expect(await screen.findByText(/2 windows are open; this is the widest/))
      .toBeInTheDocument();
  });

  // An operator who cannot close it can still tell that it is open, which is
  // the half that matters for noticing.
  it("shows an operator the warning without the button", async () => {
    vi.spyOn(api, "bypassStatus").mockResolvedValue({
      active: true, open: 1, current: bypass(), max_minutes: 480,
    });
    renderWith(<BypassBanner />, { session: sessionFor("user") });

    expect(await screen.findByText(/without asking anyone/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Close it now" })).not.toBeInTheDocument();
  });

  // A host without the feature must not put an error above every page.
  it("stays silent when the host does not have bypasses", async () => {
    vi.spyOn(api, "bypassStatus").mockRejectedValue(new Error("not implemented"));
    const { container } = renderWith(<BypassBanner />);

    await new Promise((r) => setTimeout(r, 0));
    expect(container.textContent).not.toMatch(/without asking anyone/);
  });
});
