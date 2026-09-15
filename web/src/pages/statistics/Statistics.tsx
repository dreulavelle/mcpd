import { useEffect, useMemo, useState } from "react";
import {
  Bar, BarChart, CartesianGrid, ComposedChart, Line, XAxis, YAxis,
} from "recharts";
import { api, type Statistics as Stats, type StatsPoint, type ToolTotals } from "@/lib/api";
import { EmptyState, Loading, Notice, PageHeader, Section } from "@/components/chrome";
import { Segmented } from "@/components/Segmented";
import { Chip } from "@/components/status";
import { Button } from "@/components/ui/button";
import {
  ChartContainer, ChartTooltip, ChartTooltipContent, type ChartConfig,
} from "@/components/ui/chart";
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";
import { relative } from "@/lib/format";
import { useQueryParam } from "@/lib/router";
import { cn } from "@/lib/utils";
import {
  SORTS, WINDOWS, againstBudget, bytes, condense, count, fill, headline, latency,
  pointLabel, share, sortTools, strideLabel, successRate, tokensPerCall, topTools,
  type SortKey,
} from "./stats";

/**
 * What this host has ever done, as opposed to what this process has seen.
 *
 * The page it replaces read the Prometheus registry, which lives in this
 * process's memory: every restart put it back to zero, so the console could
 * report on the last few minutes and nothing else. These numbers come from a
 * rollup written on the same call path and pruned by nothing, which is what
 * makes "is this plugin slower than it was last month" a question with an
 * answer.
 *
 * Every denominator here is named rather than assumed. A refusal is counted as
 * a call and timed as nothing, so the durations divide by the calls that ran;
 * a result size is measured only where there was a result, so the byte figures
 * divide by those. Collapsing either would report a host that refuses a great
 * deal as a fast one.
 */
export function Statistics() {
  const [windowParam, setWindow] = useQueryParam("window");
  const hours = WINDOWS.some((w) => String(w.hours) === windowParam)
    ? Number(windowParam)
    : 24 * 7;

  const [stats, setStats] = useState<Stats | null>(null);
  const [failed, setFailed] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    setStats(null);
    setFailed(null);
    api.statistics(hours)
      .then((s) => { if (live) setStats(s); })
      .catch((e: unknown) => {
        if (live) setFailed(e instanceof Error ? e.message : "The statistics could not be read.");
      });
    return () => { live = false; };
  }, [hours]);

  return (
    <>
      <PageHeader
        title="Statistics"
        lede="Every tool call this host has served, kept for good. Nothing here is reset by a restart."
      />

      <div className="mb-6 flex flex-wrap items-center gap-3">
        <Segmented
          label="How far back"
          value={String(hours)}
          onChange={(next) => setWindow(next === String(24 * 7) ? "" : next)}
          options={WINDOWS.map((w) => ({ value: String(w.hours), label: w.label }))}
          size="md"
        />
        {stats?.first && (
          <p className="text-sm text-muted-foreground">
            Recording since {relative(stats.first)}.
          </p>
        )}
      </div>

      {failed && <Notice tone="problem">{failed}</Notice>}
      {!failed && !stats && <Loading rows={6} />}
      {stats && <Body stats={stats} />}
    </>
  );
}

function Body({ stats }: { stats: Stats }) {
  const totals = useMemo(() => headline(stats), [stats]);
  // Gaps back in before anything folds buckets together, so an idle week is
  // an idle week on the chart rather than a week that never happened.
  const series = useMemo(
    () => condense(fill(stats.series, stats.stride_seconds), 60),
    [stats.series, stats.stride_seconds],
  );

  if (totals.calls === 0) {
    return (
      <EmptyState title="Nothing has been called yet">
        Once an assistant calls a tool on this host, what it did and how long it
        took is recorded here and kept. Connect a client from the Clients page
        to get started.
      </EmptyState>
    );
  }

  return (
    <div className="space-y-8">
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <Tile label="Calls served" value={count(totals.calls)}
          help={`${count(totals.ok)} succeeded, ${count(totals.notOK)} did not`} />
        <Tile
          label="Succeeded"
          value={totals.successRate === null ? "—" : `${Math.round(totals.successRate * 100)}%`}
          help={totals.denied > 0 ? `${count(totals.denied)} refused before running` : "none refused"}
          tone={totals.successRate !== null && totals.successRate < 0.9 ? "problem" : undefined}
          meter={totals.successRate ?? undefined}
        />
        <Tile label="Typical call" value={latency(totals.meanUS)}
          help={`averaged over the ${count(totals.timed)} that ran`} />
        <Tile
          label="Context spent"
          value={`~${count(totals.tokens)} tokens`}
          help={`${bytes(totals.contextBytes)} on the wire, estimated`}
        />
      </div>

      {series.length > 1 && <Traffic series={series} stride={stats.stride_seconds} />}

      {stats.tools.length >= 4 && <Concentration stats={stats} calls={totals.calls} />}

      {stats.plugins.length > 1 && (
        <Section title="By plugin" description="The same calls, summed per integration.">
          <ByPlugin stats={stats} />
        </Section>
      )}

      <EveryTool stats={stats} />

      <p className="text-xs text-muted-foreground">
        Token figures are an estimate. This host is the server and never sees
        what a model was actually charged, so they are the bytes it sent —
        counted twice, because the protocol carries a result as structured
        content and again as text — divided by {stats.bytes_per_token}.
      </p>
    </div>
  );
}

function Tile({ label, value, help, tone, meter }: {
  label: string;
  value: string;
  help?: string;
  tone?: "problem";
  /** Nought to one, drawn as a bar under the figure. */
  meter?: number;
}) {
  return (
    <div className="rounded-lg border bg-card p-4">
      <p className="text-xs text-muted-foreground">{label}</p>
      <p className={cn("mt-1 text-2xl font-semibold tabular-nums",
        tone === "problem" && "text-problem")}>
        {value}
      </p>
      {meter !== undefined && (
        <div className="mt-2 h-1 w-full overflow-hidden rounded-full bg-muted">
          <div
            className={cn("h-full rounded-full", tone === "problem" ? "bg-problem" : "bg-good")}
            style={{ width: `${Math.round(share(meter, 1) * 100)}%` }}
          />
        </div>
      )}
      {help && <p className="mt-1 text-xs text-muted-foreground">{help}</p>}
    </div>
  );
}

const trafficConfig = {
  worked: { label: "Worked", color: "var(--chart-1)" },
  other: { label: "Did not succeed", color: "var(--attention)" },
  mean: { label: "Typical call (ms)", color: "var(--chart-2)" },
} satisfies ChartConfig;

/**
 * Calls over the span, with how long they took over the top.
 *
 * Bars rather than a line for the counts: the series is a count per bucket,
 * and a line between two counts implies a value at every moment between them.
 * The latency is a line, because it is a level rather than an amount and one
 * bar beside another of a different unit reads as a comparison.
 *
 * The upper band is deliberately not called failure. A refused call is in it,
 * and a refusal is a working host doing its job -- painting a burst of them
 * red and labelling it "failures" sends somebody hunting a broken integration
 * that does not exist.
 */
function Traffic({ series, stride }: { series: StatsPoint[]; stride: number }) {
  const rows = useMemo(() => series.map((p) => ({
    label: pointLabel(p.at, stride),
    worked: p.ok,
    other: p.not_ok,
    // Milliseconds to one place, so the tooltip prints a figure rather than a
    // microsecond count nobody reads. Null in a span with nothing timed, so
    // the line breaks instead of dropping to the floor.
    mean: p.timed > 0 ? Math.round(p.mean_us / 100) / 10 : null,
  })), [series, stride]);

  const calls = series.reduce((n, p) => n + p.calls, 0);
  const notOK = series.reduce((n, p) => n + p.not_ok, 0);
  const busiest = Math.max(...series.map((p) => p.calls), 0);

  // Both ends and the middle, taken from the rows themselves: a tick that is
  // not one of the axis's own categories falls outside the scale and draws
  // nothing.
  const ticks = rows.length > 2
    ? [...new Set([rows[0]!.label, rows[Math.floor(rows.length / 2)]!.label, rows[rows.length - 1]!.label])]
    : rows.map((r) => r.label);

  const label = `${count(calls)} calls per ${strideLabel(stride)} between `
    + `${pointLabel(series[0]!.at, stride)} and ${pointLabel(series[series.length - 1]!.at, stride)}, `
    + `${count(notOK)} of them did not succeed. The busiest span served ${count(busiest)}.`;

  return (
    <Section
      title="When it was used"
      description={`Calls per ${strideLabel(stride)}, and how long a call took. The band on top is calls that did not succeed, refusals included.`}
    >
      <div className="rounded-lg border bg-card p-4">
        <ChartContainer
          config={trafficConfig}
          className="w-full"
          style={{ height: 200 }}
          role="img"
          aria-label={label}
        >
          <ComposedChart data={rows} margin={{ top: 4, right: 8, bottom: 0, left: 0 }}>
            <CartesianGrid vertical={false} />
            <XAxis
              dataKey="label" ticks={ticks} tickLine={false} axisLine={false}
              tickMargin={8} className="text-[10px]"
            />
            <YAxis
              yAxisId="calls" tickLine={false} axisLine={false} width={36}
              allowDecimals={false} tickMargin={4} className="text-[10px]"
            />
            <YAxis
              yAxisId="ms" orientation="right" tickLine={false} axisLine={false}
              width={44} tickMargin={4} className="text-[10px]"
              tickFormatter={(v: number) => `${v}ms`}
            />
            <ChartTooltip content={<ChartTooltipContent />} />
            {/* Animation off: a window change redraws this, and bars growing
                from zero every time read as a span that just started. */}
            <Bar yAxisId="calls" dataKey="worked" stackId="calls"
              fill="var(--color-worked)" isAnimationActive={false} />
            <Bar yAxisId="calls" dataKey="other" stackId="calls"
              fill="var(--color-other)" isAnimationActive={false} />
            <Line
              yAxisId="ms" dataKey="mean" type="monotone" dot={false}
              stroke="var(--color-mean)" strokeWidth={2}
              connectNulls={false} isAnimationActive={false}
            />
          </ComposedChart>
        </ChartContainer>
        <Legend />
      </div>
    </Section>
  );
}

function Legend() {
  return (
    <div className="mt-3 flex flex-wrap gap-4 text-xs text-muted-foreground">
      <Key className="bg-[var(--chart-1)]">Worked</Key>
      <Key className="bg-attention">Did not succeed</Key>
      <Key className="bg-[var(--chart-2)]">Typical call</Key>
    </div>
  );
}

function Key({ className, children }: { className: string; children: string }) {
  return (
    <span className="inline-flex items-center gap-1.5">
      <span className={cn("inline-block size-2 rounded-sm", className)} />
      {children}
    </span>
  );
}

const topConfig = {
  calls: { label: "Calls", color: "var(--chart-1)" },
} satisfies ChartConfig;

/**
 * Which tools the traffic actually goes to.
 *
 * A hundred rows sorted by calls says nothing about concentration, and
 * concentration is what decides where a slow answer or a large one costs
 * anything: one tool answering half of everything is the one worth tuning.
 */
function Concentration({ stats, calls }: { stats: Stats; calls: number }) {
  const rows = useMemo(() => topTools(stats.tools), [stats.tools]);
  const busiest = rows[0];
  const lead = busiest && !busiest.rest && calls > 0
    ? `${busiest.name} is ${Math.round(share(busiest.calls, calls) * 100)}% of every call.`
    : "";

  return (
    <Section title="Where the calls go" description={lead || "The busiest tools on this host."}>
      <div className="rounded-lg border bg-card p-4">
        <ChartContainer
          config={topConfig}
          className="w-full"
          style={{ height: Math.max(120, rows.length * 30 + 16) }}
          role="img"
          aria-label={`The busiest tools of ${count(stats.tools.length)}, by calls.`}
        >
          <BarChart data={rows} layout="vertical" margin={{ top: 0, right: 40, bottom: 0, left: 0 }}>
            <XAxis type="number" dataKey="calls" hide />
            <YAxis
              type="category" dataKey="name" width={150} tickLine={false} axisLine={false}
              className="text-[11px]"
            />
            <ChartTooltip content={<ChartTooltipContent />} />
            <Bar dataKey="calls" fill="var(--color-calls)" radius={3} isAnimationActive={false} />
          </BarChart>
        </ChartContainer>
      </div>
    </Section>
  );
}

function ByPlugin({ stats }: { stats: Stats }) {
  const busiest = Math.max(...stats.plugins.map((p) => p.calls), 1);

  return (
    <div className="space-y-2">
      {stats.plugins.map((p) => {
        const rate = successRate(p);
        return (
          <div key={p.plugin} className="rounded-md border bg-card px-3 py-2">
            <div className="flex items-center gap-3">
              <span className="min-w-0 flex-1 truncate text-sm font-medium">{p.plugin}</span>
              <span className="hidden w-24 text-xs text-muted-foreground tabular-nums sm:block">
                {p.tools} {p.tools === 1 ? "tool" : "tools"}
              </span>
              <span className="w-20 text-right text-sm tabular-nums">{count(p.calls)}</span>
              <span className={cn("w-16 text-right text-sm tabular-nums text-muted-foreground",
                rate !== null && rate < 0.9 && "text-problem")}>
                {rate === null ? "—" : `${Math.round(rate * 100)}%`}
              </span>
              <span className="w-20 text-right text-sm text-muted-foreground tabular-nums">
                {latency(p.mean_us)}
              </span>
            </div>
            {/* The share of the busiest, so nine plugins read as a shape
                rather than as nine numbers to compare by eye. */}
            <div className="mt-1.5 h-1 w-full overflow-hidden rounded-full bg-muted">
              <div className="h-full rounded-full bg-[var(--chart-1)]"
                style={{ width: `${share(p.calls, busiest) * 100}%` }} />
            </div>
          </div>
        );
      })}
    </div>
  );
}

/**
 * How many tools a page holds.
 *
 * Ten, not a hundred: a host serving nine plugins has around a hundred tools,
 * and the whole list under the charts pushed everything above it off the
 * screen. "Show more" reaches the rest.
 */
const PAGE = 10;

function EveryTool({ stats }: { stats: Stats }) {
  const [sort, setSort] = useState<SortKey>("busiest");
  const [showing, setShowing] = useState(PAGE);

  const sorted = useMemo(() => sortTools(stats.tools, sort), [stats.tools, sort]);

  // A new order or a new window is a new list, and keeping the old depth would
  // show the first eighty of an order nobody has read the first ten of.
  useEffect(() => { setShowing(PAGE); }, [sort, stats]);

  const rest = sorted.length - showing;

  return (
    <Section
      title="Every tool"
      description="What each one costs to call, and how often it answers."
      actions={
        <Segmented
          label="Order"
          value={sort}
          onChange={(next) => setSort(next)}
          options={SORTS}
        />
      }
    >
      <div className="scroll-x rounded-lg border">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Tool</TableHead>
              <TableHead className="text-right">Calls</TableHead>
              <TableHead className="text-right">Succeeded</TableHead>
              <TableHead className="text-right">Median</TableHead>
              <TableHead className="text-right" title="Nineteen calls in twenty were faster than this">
                95th
              </TableHead>
              <TableHead className="text-right">Slowest</TableHead>
              <TableHead className="text-right">Answer</TableHead>
              <TableHead className="text-right">Context</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {sorted.slice(0, showing).map((t) => (
              <ToolRow key={`${t.plugin}/${t.tool}`} t={t} stats={stats} />
            ))}
          </TableBody>
        </Table>
      </div>

      {rest > 0 && (
        <div className="mt-3 flex flex-col items-center gap-1">
          <Button variant="outline" size="sm" onClick={() => setShowing((n) => n + PAGE)}>
            Show more ({count(rest)} left)
          </Button>
          <p className="text-xs text-muted-foreground">
            Showing {count(showing)} of {count(sorted.length)} tools.
          </p>
        </div>
      )}
    </Section>
  );
}

/**
 * One percentile, drawn as the bound it is.
 *
 * The figure is a latency band's ceiling, not a measurement, so it is prefixed
 * with a bound marker unless it has been held down to the slowest call --
 * where it is exact. Absent with calls behind it means the band past the last
 * boundary, which has no ceiling to report and so is named in words.
 */
function QuantileCell({ us, timed, max }: {
  us: number | undefined;
  timed: number;
  max: number | undefined;
}) {
  if (us === undefined) {
    return (
      <TableCell className="text-right tabular-nums text-muted-foreground">
        {timed > 0 ? "over 10s" : "—"}
      </TableCell>
    );
  }
  return (
    <TableCell className="text-right tabular-nums">
      {us === max ? "" : "≤"}{latency(us)}
    </TableCell>
  );
}

function ToolRow({ t, stats }: { t: ToolTotals; stats: Stats }) {
  const rate = successRate(t);
  const tokens = tokensPerCall(t, stats);
  const budget = againstBudget(t, stats.result_budget_bytes);

  return (
    <TableRow>
      <TableCell>
        <span className="block text-sm font-medium">{t.tool}</span>
        <span className="block font-mono text-xs text-muted-foreground">{t.plugin}</span>
      </TableCell>
      <TableCell className="text-right tabular-nums">
        {count(t.calls)}
        {t.denied + t.rate_limited > 0 && (
          <span className="block text-xs text-muted-foreground">
            {count(t.denied + t.rate_limited)} refused
          </span>
        )}
      </TableCell>
      <TableCell className={cn("text-right tabular-nums",
        rate !== null && rate < 0.9 && "text-problem")}>
        {rate === null ? "—" : `${Math.round(rate * 100)}%`}
      </TableCell>
      <QuantileCell us={t.p50_us} timed={t.timed} max={t.max_us} />
      <QuantileCell us={t.p95_us} timed={t.timed} max={t.max_us} />
      <TableCell className="text-right tabular-nums text-muted-foreground">
        {latency(t.max_us)}
      </TableCell>
      <TableCell className="text-right tabular-nums">
        {t.sized === 0 ? "—" : bytes(t.mean_bytes)}
        {budget !== null && budget >= 0.8 && (
          <Chip tone="attention" className="ml-2">
            {budget >= 1 ? "truncated" : "near the cap"}
          </Chip>
        )}
      </TableCell>
      <TableCell className="text-right tabular-nums text-muted-foreground">
        {tokens === null ? "—" : `~${count(tokens)}`}
      </TableCell>
    </TableRow>
  );
}
