import { describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, type Statistics as Stats, type ToolTotals } from "@/lib/api";
import { renderWith } from "@/test/render";
import { Statistics } from "./Statistics";

function tool(over: Partial<ToolTotals> = {}): ToolTotals {
  return {
    plugin: "echo", tool: "say", calls: 0, ok: 0, errors: 0, denied: 0,
    rate_limited: 0, timed: 0, duration_sum_us: 0, mean_us: 0, sized: 0,
    bytes_sum: 0, mean_bytes: 0, ...over,
  };
}

function stub(over: Partial<Stats> = {}) {
  const body: Stats = {
    window_hours: 168, first: "2026-08-01T00:00:00Z",
    tools: [], plugins: [], series: [], stride_seconds: 3600,
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
        calls: 10, ok: 2, denied: 8, timed: 2, duration_sum_us: 100000, mean_us: 50_000,
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
        calls: 5, ok: 5, timed: 5, duration_sum_us: 5000, mean_us: 1_000,
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
      tools: [tool({ calls: 1, ok: 1, timed: 1, duration_sum_us: 30000000, mean_us: 30_000_000, max_us: 30_000_000 })],
    });
    renderWith(<Statistics />, { path: "/statistics" });
    // Both the median and the 95th: the fix that gave the median an "over 10s"
    // is what stops it rendering "—" beside a populated 95th, which reads as
    // missing data rather than as the slowest tool on the host.
    expect(await screen.findAllByText("over 10s")).toHaveLength(2);
  });

  /**
   * A percentile is a latency band's ceiling, never a measurement. Printing it
   * bare put a 1.0ms median beside a 500µs slowest on every fast in-process
   * tool -- a row that contradicts itself.
   */
  it("marks a percentile as a bound, and never prints one above the slowest call", async () => {
    stub({
      tools: [tool({
        calls: 20, ok: 20, timed: 20, duration_sum_us: 10_000,
        mean_us: 500, max_us: 500, p50_us: 500, p95_us: 500,
      })],
    });
    renderWith(<Statistics />, { path: "/statistics" });
    // Clamped to the maximum, so it is exact and carries no bound marker.
    expect(await screen.findAllByText("500µs")).not.toHaveLength(0);
    expect(screen.queryByText("≤1.0ms")).not.toBeInTheDocument();
  });

  it("shows a bound marker when the band ceiling is not the maximum", async () => {
    stub({
      tools: [tool({
        calls: 20, ok: 20, timed: 20, duration_sum_us: 200_000,
        mean_us: 10_000, max_us: 90_000, p50_us: 25_000, p95_us: 100_000,
      })],
    });
    renderWith(<Statistics />, { path: "/statistics" });
    expect(await screen.findByText("≤25ms")).toBeInTheDocument();
  });

  /** The window lives in the address, so a view can be linked to. */
  it("asks for the window in the address", async () => {
    const spy = stub({ tools: [tool({ calls: 1, ok: 1, timed: 1, duration_sum_us: 1000, mean_us: 1_000 })] });
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
      tools: [tool({ calls: 1, ok: 1, timed: 1, duration_sum_us: 1000, mean_us: 1_000, sized: 1, bytes_sum: 4_000, mean_bytes: 4_000 })],
    });
    renderWith(<Statistics />, { path: "/statistics" });
    expect(await screen.findByText(/Token figures are an estimate/)).toBeInTheDocument();
    expect(screen.getByText(/never sees what a model was actually charged/))
      .toBeInTheDocument();
  });

  /**
   * A host serving nine plugins has around a hundred tools, and the whole list
   * under the charts pushed everything above it off the screen.
   */
  it("holds the list to ten tools and reaches the rest with Show more", async () => {
    stub({
      tools: Array.from({ length: 12 }, (_, i) =>
        tool({ tool: `tool-${i}`, calls: 100 - i, ok: 100 - i, timed: 100 - i, duration_sum_us: 1_000 })),
    });
    renderWith(<Statistics />, { path: "/statistics" });

    // The table alone: the chart above it names the busiest tools too.
    const table = await screen.findByRole("table");
    expect(within(table).getAllByRole("row")).toHaveLength(11); // ten and a header
    expect(screen.getByText("Showing 10 of 12 tools.")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Show more (2 left)" }));
    expect(within(table).getAllByRole("row")).toHaveLength(13);
    expect(screen.queryByRole("button", { name: /Show more/ })).not.toBeInTheDocument();
  });

  /**
   * Busiest answers "what does this host do". Finding the slow one in a
   * hundred rows sorted by calls is the question a single order cannot be
   * asked at all.
   */
  it("reorders the list when another order is chosen", async () => {
    stub({
      tools: [
        tool({ tool: "busy", calls: 500, ok: 500, timed: 500, duration_sum_us: 500, p95_us: 1_000, max_us: 1_000 }),
        tool({ tool: "quick", calls: 9, ok: 9, timed: 9, duration_sum_us: 900, p95_us: 1_000, max_us: 1_000 }),
        tool({ tool: "middling", calls: 8, ok: 8, timed: 8, duration_sum_us: 800, p95_us: 25_000, max_us: 25_000 }),
        // Past the last band, so it has no ceiling to report and is the
        // slowest thing here.
        tool({ tool: "crawler", calls: 2, ok: 2, timed: 2, duration_sum_us: 60_000_000, max_us: 30_000_000 }),
      ],
    });
    renderWith(<Statistics />, { path: "/statistics" });

    const table = await screen.findByRole("table");
    const firstTool = () => within(within(table).getAllByRole("row")[1]!).getAllByText(/./)[0]!.textContent;
    expect(firstTool()).toBe("busy");

    await userEvent.click(screen.getByRole("radio", { name: "Slowest" }));
    expect(firstTool()).toBe("crawler");
  });

  /**
   * The chart is a picture, so what it covers has to be readable without it.
   */
  it("says what the traffic chart covers", async () => {
    stub({
      tools: [tool({ calls: 14, ok: 14, timed: 14, duration_sum_us: 14_000, mean_us: 1_000 })],
      series: [
        { at: "2026-09-01T00:00:00Z", calls: 10, ok: 10, not_ok: 0, timed: 10, duration_sum_us: 10_000, mean_us: 1_000, bytes: 0 },
        // Four hours later: the spans between are idle and absent from the
        // rollup, and the chart has to draw them rather than close the gap.
        { at: "2026-09-01T04:00:00Z", calls: 4, ok: 3, not_ok: 1, timed: 4, duration_sum_us: 4_000, mean_us: 1_000, bytes: 0 },
      ],
    });
    renderWith(<Statistics />, { path: "/statistics" });

    const chart = await screen.findByRole("img", { name: /calls per hour/ });
    expect(chart).toHaveAccessibleName(/14 calls/);
    expect(chart).toHaveAccessibleName(/1 of them did not succeed/);
    expect(chart).toHaveAccessibleName(/busiest span served 10/);
  });

  it("shows each tool with its own row", async () => {
    stub({
      tools: [
        tool({ tool: "say", calls: 9, ok: 9, timed: 9, duration_sum_us: 18000, mean_us: 2_000, p50_us: 5_000 }),
        tool({ tool: "shout", plugin: "echo", calls: 3, ok: 1, errors: 2, timed: 3, duration_sum_us: 27000, mean_us: 9_000 }),
      ],
    });
    renderWith(<Statistics />, { path: "/statistics" });

    const shout = (await screen.findByText("shout")).closest("tr")!;
    // Two of three failed, so a third succeeded.
    expect(within(shout).getByText("33%")).toBeInTheDocument();
  });
});
