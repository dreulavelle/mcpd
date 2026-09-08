import { describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, type Statistics as Stats, type ToolTotals } from "@/lib/api";
import { renderWith } from "@/test/render";
import { Statistics } from "./Statistics";

function tool(over: Partial<ToolTotals> = {}): ToolTotals {
  return {
    plugin: "echo", tool: "say", calls: 0, ok: 0, errors: 0, denied: 0,
    rate_limited: 0, timed: 0, mean_us: 0, sized: 0, bytes_sum: 0,
    mean_bytes: 0, first_seen: "2026-09-01T00:00:00Z",
    last_seen: "2026-09-08T00:00:00Z", ...over,
  };
}

function stub(over: Partial<Stats> = {}) {
  const body: Stats = {
    window_hours: 168, recording: true,
    first: "2026-08-01T00:00:00Z", last: "2026-09-08T00:00:00Z",
    tools: [], plugins: [], series: [],
    result_budget_bytes: 40_000, wire_multiplier: 2, bytes_per_token: 4, ...over,
  };
  return vi.spyOn(api, "statistics").mockResolvedValue(body);
}

describe("the statistics page", () => {
  it("says so plainly before anything has been called", async () => {
    stub();
    renderWith(<Statistics />, { path: "/statistics" });
    expect(await screen.findByText(/Nothing has been called yet/)).toBeInTheDocument();
  });

  /**
   * The headline divides by the calls that ran. A host that refuses most of
   * what it is asked must not read as a fast one.
   */
  it("reports latency over the calls that ran, and refusals separately", async () => {
    stub({
      tools: [tool({
        calls: 10, ok: 2, denied: 8, timed: 2, mean_us: 50_000,
      })],
    });
    renderWith(<Statistics />, { path: "/statistics" });

    expect(await screen.findByText("50ms")).toBeInTheDocument();
    expect(screen.getByText(/averaged over the 2 that ran/)).toBeInTheDocument();
    expect(screen.getByText(/8 refused before running/)).toBeInTheDocument();
  });

  /**
   * A tool whose largest answer sits against the ceiling is one whose answers
   * are being cut mid-sentence, and nothing else on the page would say so.
   */
  it("flags a tool whose answers are against the result cap", async () => {
    stub({
      tools: [tool({
        calls: 5, ok: 5, timed: 5, mean_us: 1_000,
        sized: 5, bytes_sum: 10_000, mean_bytes: 2_000, max_bytes: 41_000,
      })],
    });
    renderWith(<Statistics />, { path: "/statistics" });
    expect(await screen.findByText("truncated")).toBeInTheDocument();
  });

  /**
   * The 95th percentile is read off fixed latency bands, and the last band has
   * no upper bound. Reporting a number for it would invent a ceiling.
   */
  it("says a call was over the last band rather than inventing a figure", async () => {
    stub({
      tools: [tool({ calls: 1, ok: 1, timed: 1, mean_us: 30_000_000, max_us: 30_000_000 })],
    });
    renderWith(<Statistics />, { path: "/statistics" });
    expect(await screen.findByText("over 10s")).toBeInTheDocument();
  });

  /** The window lives in the address, so a view can be linked to. */
  it("asks for the window in the address", async () => {
    const spy = stub({ tools: [tool({ calls: 1, ok: 1, timed: 1, mean_us: 1_000 })] });
    renderWith(<Statistics />, { path: "/statistics" });
    await screen.findByRole("radio", { name: "24 hours" });
    expect(spy).toHaveBeenCalledWith(168);

    await userEvent.click(screen.getByRole("radio", { name: "All time" }));
    expect(spy).toHaveBeenCalledWith(0);
  });

  /**
   * The token figures are an estimate: this host is the server and never sees
   * what the model was charged. The page has to say so rather than presenting
   * them as a measurement.
   */
  it("names the token figures as an estimate", async () => {
    stub({
      tools: [tool({ calls: 1, ok: 1, timed: 1, mean_us: 1_000, sized: 1, bytes_sum: 4_000, mean_bytes: 4_000 })],
    });
    renderWith(<Statistics />, { path: "/statistics" });
    expect(await screen.findByText(/Token figures are an estimate/)).toBeInTheDocument();
    expect(screen.getByText(/never sees what a model was actually charged/))
      .toBeInTheDocument();
  });

  it("shows each tool with its own row", async () => {
    stub({
      tools: [
        tool({ tool: "say", calls: 9, ok: 9, timed: 9, mean_us: 2_000, p50_us: 5_000 }),
        tool({ tool: "shout", plugin: "echo", calls: 3, ok: 1, errors: 2, timed: 3, mean_us: 9_000 }),
      ],
    });
    renderWith(<Statistics />, { path: "/statistics" });

    const shout = (await screen.findByText("shout")).closest("tr")!;
    // Two of three failed, so a third succeeded.
    expect(within(shout).getByText("33%")).toBeInTheDocument();
  });
});
