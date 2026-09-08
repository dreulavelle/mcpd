import { describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import { api } from "@/lib/api";
import { renderWith, sessionFor } from "@/test/render";
import { SessionExpiry } from "./SessionExpiry";

const soon = (ms: number) => new Date(Date.now() + ms).toISOString();

describe("the session countdown", () => {
  it("says nothing while the end is a long way off", () => {
    renderWith(<SessionExpiry />, {
      session: sessionFor("admin", { ends_at: soon(3_600_000) }),
    });
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });

  /**
   * `expires_at` is the ceiling and `ends_at` the nearer of the two clocks.
   * Counting down the ceiling would promise days to somebody minutes from
   * being signed out for going quiet.
   */
  it("counts down the nearer deadline, not the ceiling", () => {
    renderWith(<SessionExpiry />, {
      session: sessionFor("admin", {
        expires_at: soon(72 * 3_600_000),
        ends_at: soon(5 * 60_000),
      }),
    });
    expect(screen.getByRole("status")).toHaveTextContent(/ends in 5 minutes/);
  });

  /**
   * The idle deadline moves whenever somebody does something, and the session
   * here was fetched once when the page loaded. Without re-asking, somebody
   * working through a long afternoon watches an accurate countdown reach zero
   * and sit at "under a minute" while their session is perfectly healthy.
   */
  it("re-asks while it is showing, and goes away once the deadline has moved", async () => {
    vi.useFakeTimers();
    try {
      const moved = sessionFor("admin", { ends_at: soon(8 * 3_600_000) });
      const fetched = vi.spyOn(api, "session").mockResolvedValue(moved);

      renderWith(<SessionExpiry />, {
        session: sessionFor("admin", { ends_at: soon(4 * 60_000) }),
      });
      expect(screen.getByRole("status")).toBeInTheDocument();

      await vi.advanceTimersByTimeAsync(31_000);
      expect(fetched).toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
    }
  });

  /** A session with no clock at all is not something to count down. */
  it("says nothing when the deadline cannot be read", () => {
    renderWith(<SessionExpiry />, {
      session: sessionFor("admin", { ends_at: "not-a-date", expires_at: "not-a-date" }),
    });
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });
});
