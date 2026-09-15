import { describe, expect, it } from "vitest";
import type { Statistics, StatsPoint, ToolTotals } from "@/lib/api";
import {
  againstBudget, bytes, condense, count, fill, headline, latency, share,
  sortTools, successRate, tokensPerCall, topTools,
} from "./stats";

function tool(over: Partial<ToolTotals> = {}): ToolTotals {
  return {
    plugin: "echo", tool: "say", calls: 0, ok: 0, errors: 0, denied: 0,
    rate_limited: 0, timed: 0, duration_sum_us: 0, mean_us: 0, sized: 0,
    bytes_sum: 0, mean_bytes: 0, ...over,
  };
}

function stats(over: Partial<Statistics> = {}): Statistics {
  return {
    window_hours: 0, tools: [], plugins: [], series: [], stride_seconds: 3600,
    result_budget_bytes: 40_000, wire_multiplier: 2, bytes_per_token: 4, ...over,
  };
}

describe("headline", () => {
  /**
   * A refusal never reached a handler. It counts as a call, because somebody
   * reached for the tool, but dividing the durations by every call rather than
   * by the ones that ran reports a host that refuses a great deal as a fast one.
   */
  it("averages latency over the calls that ran, not every call", () => {
    const h = headline(stats({
      tools: [tool({
        calls: 10, ok: 2, denied: 8, timed: 2,
        duration_sum_us: 100_000, mean_us: 50_000,
      })],
    }));
    expect(h.calls).toBe(10);
    expect(h.timed).toBe(2);
    expect(h.meanUS).toBe(50_000);
  });

  /** Nothing called is not a success rate of zero. */
  it("has no success rate before anything has been called", () => {
    expect(headline(stats()).successRate).toBeNull();
    expect(headline(stats()).meanUS).toBeNull();
  });

  it("weights the mean by how many calls each tool contributed", () => {
    const h = headline(stats({
      tools: [
        tool({ tool: "fast", calls: 99, ok: 99, timed: 99, duration_sum_us: 99000, mean_us: 1_000 }),
        tool({ tool: "slow", calls: 1, ok: 1, timed: 1, duration_sum_us: 100000, mean_us: 100_000 }),
      ],
    }));
    // Not (1_000 + 100_000) / 2.
    expect(h.meanUS).toBe(Math.round((99 * 1_000 + 100_000) / 100));
  });

  /**
   * The protocol carries a result as structured content and again as text, so
   * what a result costs a model's context is twice what this host sent once.
   */
  it("counts a result twice against the context, as the wire does", () => {
    const h = headline(stats({ tools: [tool({ calls: 1, ok: 1, sized: 1, bytes_sum: 4_000 })] }));
    expect(h.bytes).toBe(4_000);
    expect(h.contextBytes).toBe(8_000);
    expect(h.tokens).toBe(2_000);
  });
});

describe("tokensPerCall", () => {
  it("is unknown for a tool that has never returned anything", () => {
    expect(tokensPerCall(tool({ calls: 5, denied: 5 }), stats())).toBeNull();
  });

  it("doubles the answer before dividing it into tokens", () => {
    expect(tokensPerCall(tool({ sized: 2, mean_bytes: 1_000 }), stats())).toBe(500);
  });
});

describe("againstBudget", () => {
  /**
   * The budget caps one result, so the largest answer decides whether anything
   * is being cut. A tool that usually returns a line and occasionally returns
   * a database is exactly the one whose answers get truncated, and its mean
   * hides that.
   */
  it("measures the largest answer against the cap, never the mean", () => {
    const t = tool({ sized: 100, mean_bytes: 500, max_bytes: 39_000 });
    expect(againstBudget(t, 40_000)).toBeCloseTo(0.975);
  });

  it("is unknown when nothing has been measured", () => {
    expect(againstBudget(tool(), 40_000)).toBeNull();
  });
});

describe("successRate", () => {
  it("counts refusals against the rate, because a refused call did not work", () => {
    expect(successRate(tool({ calls: 4, ok: 3, denied: 1 }))).toBe(0.75);
  });
});

describe("condense", () => {
  function point(i: number, calls: number): StatsPoint {
    return {
      at: new Date(Date.UTC(2026, 8, 1, i)).toISOString(),
      calls, ok: calls, not_ok: 0, timed: calls,
      duration_sum_us: calls * 1_000, mean_us: 1_000, bytes: calls * 10,
    };
  }

  it("leaves a short series alone", () => {
    const s = [point(0, 1), point(1, 2)];
    expect(condense(s, 48)).toBe(s);
  });

  /** Summing buckets must not lose calls -- a chart that drops them is a lie. */
  it("keeps every call when it folds buckets together", () => {
    const s = Array.from({ length: 100 }, (_, i) => point(i, i + 1));
    const folded = condense(s, 10);
    expect(folded.length).toBeLessThanOrEqual(10);
    const before = s.reduce((n, p) => n + p.calls, 0);
    expect(folded.reduce((n, p) => n + p.calls, 0)).toBe(before);
  });

  it("re-averages latency over the folded buckets rather than averaging averages", () => {
    const s = [
      { ...point(0, 1), timed: 1, duration_sum_us: 100, mean_us: 100 },
      { ...point(1, 9), timed: 9, duration_sum_us: 9900, mean_us: 1_100 },
    ];
    const [folded] = condense(s, 1);
    expect(folded!.mean_us).toBe(Math.round((100 + 9_900) / 10));
  });
});

describe("fill", () => {
  function at(hour: number, calls: number): StatsPoint {
    return {
      at: new Date(Date.UTC(2026, 8, 1, hour)).toISOString(),
      calls, ok: calls, not_ok: 0, timed: calls,
      duration_sum_us: calls * 1_000, mean_us: 1_000, bytes: calls * 10,
    };
  }

  /**
   * The bug this exists for. The rollup holds a row only for an hour something
   * was called in, so an idle week is absent rather than zero. Drawn as it
   * came, two hours a fortnight apart were two bars side by side, which reads
   * as a fortnight of steady traffic.
   */
  it("puts the quiet spans back rather than closing the gap", () => {
    const filled = fill([at(0, 5), at(4, 3)], 3600);
    expect(filled).toHaveLength(5);
    expect(filled.map((p) => p.calls)).toEqual([5, 0, 0, 0, 3]);
  });

  it("keeps every call it was given", () => {
    const series = [at(0, 5), at(9, 3), at(20, 7)];
    const before = series.reduce((n, p) => n + p.calls, 0);
    expect(fill(series, 3600).reduce((n, p) => n + p.calls, 0)).toBe(before);
  });

  /** A span with nothing in it has nothing to average, and a mean of zero
   *  would drag a latency line to the floor across an idle week. */
  it("leaves an invented span timed as nothing", () => {
    const gap = fill([at(0, 5), at(2, 5)], 3600)[1]!;
    expect(gap.timed).toBe(0);
    expect(gap.duration_sum_us).toBe(0);
  });

  it("leaves a series that is already dense alone", () => {
    const series = [at(0, 1), at(1, 2), at(2, 3)];
    expect(fill(series, 3600)).toBe(series);
  });

  /** A stride that disagrees with the timestamps would have this allocating
   *  a point per second between two distant hours. */
  it("refuses to invent more points than a chart could draw", () => {
    const series = [at(0, 1), at(20, 1)];
    expect(fill(series, 1)).toBe(series);
  });

  it("folds to a drawable width once the gaps are back", () => {
    const series = [at(0, 1), at(40, 1)];
    expect(condense(fill(series, 3600), 10).length).toBeLessThanOrEqual(10);
  });
});

describe("sortTools", () => {
  const busy = tool({ tool: "busy", calls: 100, ok: 100, timed: 100, p95_us: 5_000, max_us: 5_000 });
  const slow = tool({ tool: "slow", calls: 2, ok: 2, timed: 2, max_us: 30_000_000 });
  const flaky = tool({ tool: "flaky", calls: 10, ok: 5, errors: 5, timed: 10, p95_us: 1_000 });
  // Bounded latency, so the only thing extreme about it is the answer size.
  const fat = tool({
    tool: "fat", calls: 4, ok: 4, timed: 4, p95_us: 2_000, max_us: 2_000,
    sized: 4, mean_bytes: 90_000,
  });
  const all = [busy, slow, flaky, fat];

  it("puts the busiest first by default", () => {
    expect(sortTools(all, "busiest")[0]!.tool).toBe("busy");
  });

  /**
   * A tool whose 95th fell past the last band has no ceiling to report. Sorting
   * on the missing figure as zero buried the slowest thing on the host at the
   * bottom of the list somebody opened to find it.
   */
  it("ranks a tool past the last band as the slowest, not the fastest", () => {
    expect(sortTools(all, "slowest")[0]!.tool).toBe("slow");
  });

  /**
   * Both of these ran past the last band, so both are unbounded. Subtracting
   * one from the other is NaN, a comparator returning NaN leaves the order
   * undefined, and `NaN || fallback` takes the fallback without a sound -- so
   * the whole list came back ordered by something nobody asked for.
   */
  it("keeps a total order when two tools are both past the last band", () => {
    const quiet = tool({ tool: "quiet", calls: 2, ok: 2, timed: 2, max_us: 30_000_000 });
    const loud = tool({ tool: "loud", calls: 40, ok: 40, timed: 40, max_us: 30_000_000 });
    const order = sortTools([quiet, loud, busy], "slowest").map((t) => t.tool);
    // The two unbounded ones first, busiest of them first, and the tool with
    // a ceiling behind both.
    expect(order).toEqual(["loud", "quiet", "busy"]);
  });

  it("puts the least reliable first", () => {
    expect(sortTools(all, "unreliable")[0]!.tool).toBe("flaky");
  });

  it("ranks by the biggest answer, ignoring tools that returned nothing", () => {
    const order = sortTools(all, "largest");
    expect(order[0]!.tool).toBe("fat");
    expect(order[order.length - 1]!.calls).toBeGreaterThan(0);
  });

  it("does not reorder the array it was handed", () => {
    const given = [...all];
    sortTools(given, "slowest");
    expect(given.map((t) => t.tool)).toEqual(all.map((t) => t.tool));
  });
});

describe("topTools", () => {
  it("gathers everything past the head into one row", () => {
    const many = Array.from({ length: 12 }, (_, i) =>
      tool({ tool: `t${i}`, calls: 12 - i, ok: 12 - i }));
    const rows = topTools(many, 8);
    expect(rows).toHaveLength(9);
    expect(rows[8]).toMatchObject({ rest: true, name: "4 more" });
    // 4 + 3 + 2 + 1, the four it did not name.
    expect(rows[8]!.calls).toBe(10);
  });

  /** Two plugins may both expose a `search`, and two ticks reading `search`
   *  is a chart that cannot be read. */
  it("names the plugin only where a tool name clashes", () => {
    const rows = topTools([
      tool({ plugin: "acme", tool: "search", calls: 9, ok: 9 }),
      tool({ plugin: "globex", tool: "search", calls: 8, ok: 8 }),
      tool({ plugin: "acme", tool: "list", calls: 7, ok: 7 }),
    ]);
    expect(rows.map((r) => r.name)).toEqual(["acme/search", "globex/search", "list"]);
  });

  it("leaves out a tool nothing has called", () => {
    expect(topTools([tool({ tool: "never", calls: 0 })])).toHaveLength(0);
  });
});

describe("share", () => {
  it("is a fraction, and never leaves the bar", () => {
    expect(share(1, 4)).toBe(0.25);
    expect(share(5, 4)).toBe(1);
    expect(share(1, 0)).toBe(0);
  });
});

describe("formatting", () => {
  it("shows a duration in the coarsest unit that still says something", () => {
    expect(latency(400)).toBe("400µs");
    expect(latency(4_200)).toBe("4.2ms");
    expect(latency(250_000)).toBe("250ms");
    expect(latency(3_500_000)).toBe("3.5s");
    expect(latency(undefined)).toBe("—");
  });

  it("shows bytes and counts at a size a person reads", () => {
    expect(bytes(900)).toBe("900 B");
    expect(bytes(40_000)).toBe("40 KB");
    expect(bytes(5_400_000)).toBe("5.4 MB");
    expect(count(1_500)).toBe("1.5K");
    expect(count(2_400_000)).toBe("2.4M");
  });
});
