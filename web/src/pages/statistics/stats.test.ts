import { describe, expect, it } from "vitest";
import type { Statistics, StatsPoint, ToolTotals } from "@/lib/api";
import {
  againstBudget, bytes, condense, count, headline, latency, successRate,
  tokensPerCall,
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
