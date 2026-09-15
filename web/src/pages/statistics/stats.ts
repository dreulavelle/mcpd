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
  /** Every call that did not succeed, refusals included. Not "failed". */
  notOK: number;
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
    // The exact sum, not the mean times the count: the mean was already
    // integer-truncated server side, and the page's claim is that its
    // arithmetic is named rather than approximately right.
    durationSum += t.duration_sum_us;
    bytes += t.bytes_sum;
  }
  const contextBytes = bytes * s.wire_multiplier;
  return {
    calls, ok, denied, timed, bytes, contextBytes,
    notOK: calls - ok,
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
 * Puts the quiet spans back into a series.
 *
 * The rollup holds a row only for an hour something was called in, so the
 * series arrives with every idle span missing rather than zeroed. Drawn as it
 * comes, sixty-six scattered hours across a fortnight are sixty-six bars side
 * by side, which reads as a fortnight of steady traffic -- the opposite of
 * what happened. Every stride between the first point and the last gets a
 * point, and the empty ones are zero.
 *
 * Only between the two: the span the host holds is a fact, and padding out to
 * the window asked for would draw days this host has no answer for.
 */
export function fill(series: StatsPoint[], strideSeconds: number, limit = 500): StatsPoint[] {
  if (series.length < 2 || strideSeconds <= 0) return series;
  const stride = strideSeconds * 1000;
  const first = new Date(series[0]!.at).getTime();
  const last = new Date(series[series.length - 1]!.at).getTime();
  if (Number.isNaN(first) || Number.isNaN(last) || last <= first) return series;

  const spans = Math.round((last - first) / stride) + 1;
  // The server picks the stride so the populated span is about fifty points,
  // so this is never far from that. The bound is for a stride that disagrees
  // with the timestamps, where filling would allocate without end.
  if (spans <= series.length || spans > limit) return series;

  const have = new Map<number, StatsPoint>();
  for (const p of series) {
    const at = new Date(p.at).getTime();
    if (!Number.isNaN(at)) have.set(Math.round((at - first) / stride), p);
  }

  const out: StatsPoint[] = [];
  for (let i = 0; i < spans; i++) {
    const p = have.get(i);
    out.push(p ?? {
      at: new Date(first + i * stride).toISOString(),
      calls: 0, ok: 0, not_ok: 0, timed: 0, duration_sum_us: 0, mean_us: 0, bytes: 0,
    });
  }
  return out;
}

/**
 * Folds a series into at most `want` columns.
 *
 * A safety net rather than the thing that bounds the payload: the server
 * already returns at most fifty points, at a stride it reports. This only has
 * work to do if that ever changes.
 */
export function condense(series: StatsPoint[], want = 48): StatsPoint[] {
  if (series.length <= want) return series;
  const per = Math.ceil(series.length / want);
  const out: StatsPoint[] = [];
  for (let i = 0; i < series.length; i += per) {
    const slice = series.slice(i, i + per);
    let calls = 0, ok = 0, notOK = 0, timed = 0, durationSum = 0, bytes = 0;
    for (const p of slice) {
      calls += p.calls; ok += p.ok; notOK += p.not_ok;
      timed += p.timed; durationSum += p.duration_sum_us; bytes += p.bytes;
    }
    out.push({
      at: slice[0]!.at, calls, ok, not_ok: notOK, timed, bytes,
      duration_sum_us: durationSum,
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

/** What one point of the series is called on an axis. */
export function pointLabel(iso: string, strideSeconds: number): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return strideSeconds < 86_400
    ? d.toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" })
    : d.toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

/** A row's size against the largest, as a width. Never below zero. */
export function share(n: number, of: number): number {
  return of > 0 ? Math.max(0, Math.min(1, n / of)) : 0;
}

/**
 * The orders worth reading a hundred tools in.
 *
 * Busiest answers "what does this host do"; the other three answer "what
 * should I look at", which is the question a flat table sorted one way
 * cannot be asked at all.
 */
export type SortKey = "busiest" | "unreliable" | "slowest" | "largest";

export const SORTS: { value: SortKey; label: string }[] = [
  { value: "busiest", label: "Busiest" },
  { value: "unreliable", label: "Least reliable" },
  { value: "slowest", label: "Slowest" },
  { value: "largest", label: "Biggest answers" },
];

/**
 * How slow a tool is, for ranking.
 *
 * A tool whose 95th fell past the last band has no ceiling to report, and it
 * is the slowest thing on the host rather than the fastest -- sorting on a
 * missing figure as zero buries exactly the row somebody came to find.
 */
function slowness(t: ToolTotals): number {
  if (t.timed === 0) return -1;
  return t.p95_us ?? Number.POSITIVE_INFINITY;
}

/**
 * Tools in the order asked for. Never sorts the caller's array in place.
 *
 * Every comparison below tests equality before subtracting. Two tools that
 * both ran past the last latency band are both unbounded, and `Infinity -
 * Infinity` is NaN -- a comparator that returns NaN leaves the order
 * undefined, and `NaN || fallback` takes the fallback silently, so the bug
 * shows up as a list that is merely in the wrong order.
 */
export function sortTools(tools: ToolTotals[], key: SortKey): ToolTotals[] {
  const out = [...tools];
  switch (key) {
    case "unreliable":
      // Worst first, and a tool with no calls at all has no rate to rank.
      return out.sort((a, b) => {
        const ra = successRate(a), rb = successRate(b);
        if (ra === null || rb === null) return (ra === null ? 1 : 0) - (rb === null ? 1 : 0);
        return ra === rb ? b.calls - a.calls : ra - rb;
      });
    case "slowest":
      return out.sort((a, b) => {
        const sa = slowness(a), sb = slowness(b);
        return sa === sb ? b.calls - a.calls : sb - sa;
      });
    case "largest":
      return out.sort((a, b) => {
        const la = a.sized > 0 ? a.mean_bytes : -1;
        const lb = b.sized > 0 ? b.mean_bytes : -1;
        return la === lb ? b.calls - a.calls : lb - la;
      });
    default:
      return out.sort((a, b) =>
        b.calls === a.calls ? a.tool.localeCompare(b.tool) : b.calls - a.calls);
  }
}

/** One tool's share of the traffic, named for a chart. */
export interface Slice {
  /** What the axis reads. Unique across the rows: an axis cannot have two
   *  categories with the same name, and two plugins may both have a `search`. */
  name: string;
  calls: number;
  /** Whether this row is the remainder rather than a tool. */
  rest: boolean;
}

/**
 * The few tools that carry the traffic, with the remainder as one row.
 *
 * A hundred rows sorted by calls says nothing about concentration, and
 * concentration is the thing worth knowing: one tool answering half of
 * everything is where a slow answer or a large one costs the most.
 */
export function topTools(tools: ToolTotals[], n = 8): Slice[] {
  const ranked = sortTools(tools, "busiest");
  const head = ranked.slice(0, n).filter((t) => t.calls > 0);

  // Two plugins may expose the same tool name, and two ticks reading `search`
  // is a chart that cannot be read. Only the clashing ones carry the plugin.
  const seen = new Map<string, number>();
  for (const t of head) seen.set(t.tool, (seen.get(t.tool) ?? 0) + 1);

  const out: Slice[] = head.map((t) => ({
    name: (seen.get(t.tool) ?? 0) > 1 ? `${t.plugin}/${t.tool}` : t.tool,
    calls: t.calls,
    rest: false,
  }));

  const rest = ranked.slice(n).reduce((sum, t) => sum + t.calls, 0);
  if (rest > 0) {
    out.push({ name: `${ranked.length - n} more`, calls: rest, rest: true });
  }
  return out;
}

/** How wide one bar is, in the words a caption would use. */
export function strideLabel(seconds: number): string {
  const hours = Math.round(seconds / 3600);
  if (hours <= 1) return "hour";
  if (hours < 48) return `${hours} hours`;
  const days = Math.round(hours / 24);
  return days === 1 ? "day" : `${days} days`;
}
