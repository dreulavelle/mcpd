import type { PluginTotals, Statistics, StatsPoint, ToolTotals } from "@/lib/api";

/**
 * The arithmetic the statistics page draws, kept out of the component so the
 * denominators can be tested without rendering anything.
 *
 * The denominators are the whole point. `calls` counts everything including
 * refusals that never reached a handler; `timed` is what a duration divides
 * by, and `sized` what a byte figure does. Swapping them reports a host that
 * refuses a great deal as a fast one, which is the opposite of the truth.
 */

/** How far back a view asks for. Zero is everything the host has ever kept. */
export const WINDOWS = [
  { hours: 24, label: "24 hours" },
  { hours: 24 * 7, label: "7 days" },
  { hours: 24 * 30, label: "30 days" },
  { hours: 0, label: "All time" },
] as const;

export type WindowHours = (typeof WINDOWS)[number]["hours"];

/** The whole host, summed from the per-tool rows. */
export interface Headline {
  calls: number;
  ok: number;
  failed: number;
  denied: number;
  /** Successes over calls, or null when nothing has been called at all --
   *  which is not the same as a success rate of zero. */
  successRate: number | null;
  timed: number;
  meanUS: number | null;
  bytes: number;
  /** Bytes of context, which is not bytes of result: the protocol carries a
   *  result as structured content and again as text. */
  contextBytes: number;
  tokens: number;
  tools: number;
}

export function headline(s: Statistics): Headline {
  let calls = 0, ok = 0, denied = 0, timed = 0, durationSum = 0, bytes = 0;
  for (const t of s.tools) {
    calls += t.calls;
    ok += t.ok;
    denied += t.denied + t.rate_limited;
    timed += t.timed;
    durationSum += t.mean_us * t.timed;
    bytes += t.bytes_sum;
  }
  const contextBytes = bytes * s.wire_multiplier;
  return {
    calls, ok, denied, timed, bytes, contextBytes,
    failed: calls - ok,
    successRate: calls > 0 ? ok / calls : null,
    meanUS: timed > 0 ? Math.round(durationSum / timed) : null,
    tokens: Math.round(contextBytes / s.bytes_per_token),
    tools: s.tools.length,
  };
}

/** What one tool costs a model's context, per call. */
export function tokensPerCall(t: ToolTotals, s: Statistics): number | null {
  if (t.sized === 0) return null;
  return Math.round((t.mean_bytes * s.wire_multiplier) / s.bytes_per_token);
}

/** Successes over every call, or null when the tool has never been called. */
export function successRate(t: ToolTotals | PluginTotals): number | null {
  return t.calls > 0 ? t.ok / t.calls : null;
}

/**
 * Whether a tool's answers are being cut.
 *
 * The budget is a ceiling on one result, so the largest answer is what decides
 * it, never the mean: a tool that usually returns a line and occasionally
 * returns a database is exactly the one whose answers get truncated.
 */
export function againstBudget(t: ToolTotals, budget: number): number | null {
  if (t.max_bytes === undefined || budget <= 0) return null;
  return t.max_bytes / budget;
}

/**
 * Buckets a series into at most `want` columns.
 *
 * A month of hours is 720 bars in a chart a few hundred pixels wide, which is
 * a smear rather than a shape. Summing adjacent hours keeps every call counted
 * while giving the eye something it can read.
 */
export function condense(series: StatsPoint[], want = 48): StatsPoint[] {
  if (series.length <= want) return series;
  const per = Math.ceil(series.length / want);
  const out: StatsPoint[] = [];
  for (let i = 0; i < series.length; i += per) {
    const slice = series.slice(i, i + per);
    let calls = 0, ok = 0, failed = 0, timed = 0, durationSum = 0, bytes = 0;
    for (const p of slice) {
      calls += p.calls; ok += p.ok; failed += p.failed;
      timed += p.timed; durationSum += p.mean_us * p.timed; bytes += p.bytes;
    }
    out.push({
      at: slice[0]!.at, calls, ok, failed, timed, bytes,
      mean_us: timed > 0 ? Math.round(durationSum / timed) : 0,
    });
  }
  return out;
}

/** A duration in the coarsest unit that still says something. */
export function latency(us: number | undefined | null): string {
  if (us === undefined || us === null) return "—";
  if (us < 1000) return `${us}µs`;
  if (us < 10_000) return `${(us / 1000).toFixed(1)}ms`;
  if (us < 1_000_000) return `${Math.round(us / 1000)}ms`;
  return `${(us / 1_000_000).toFixed(1)}s`;
}

/** Bytes, rounded to something a person reads. */
export function bytes(n: number): string {
  if (n >= 1_000_000_000) return `${(n / 1_000_000_000).toFixed(1)} GB`;
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)} MB`;
  if (n >= 1_000) return `${Math.round(n / 1000)} KB`;
  return `${n} B`;
}

/** A count, shortened once it stops being readable in full. */
export function count(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 10_000) return `${Math.round(n / 1000)}K`;
  if (n >= 1_000) return `${(n / 1000).toFixed(1)}K`;
  return String(n);
}
