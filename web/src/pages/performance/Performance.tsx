import { useCallback, useMemo, useState } from "react";
import { Bar, BarChart, CartesianGrid, Cell, ReferenceLine, XAxis, YAxis } from "recharts";
import { api, type Bucket, type Distribution, type Performance as Perf, type ToolStats } from "@/lib/api";
import { useLoader } from "@/lib/hooks";
import { Loading, Notice, PageHeader } from "@/components/chrome";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { NativeSelect } from "@/components/ui/native-select";
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";
import {
  ChartContainer, ChartTooltip, ChartTooltipContent, type ChartConfig,
} from "@/components/ui/chart";

/**
 * What the host's own collectors have seen.
 *
 * The page exists because the two questions worth asking about a tool — is it
 * slow, and is its answer too big — were both unanswerable from inside mcpd
 * until they were measured. A number nobody can see is a number nobody acts
 * on, and "it feels slow" is not something to tune against.
 *
 * Everything here is cumulative since the process started. There is no history:
 * the registry holds counts as of now, so this says what has happened, not when
 * it happened. That is deliberate for the moment — a trend needs somewhere to
 * keep the earlier sample, which is a storage decision rather than a chart one.
 */
export function Performance() {
  // Ten seconds: fast enough that a call made while watching shows up, slow
  // enough that a page left open is not a load of its own.
  const load = useCallback(() => api.performance(), []);
  const { data, error } = useLoader(load, "Couldn't read performance data.", 10_000);

  return (
    <>
      <PageHeader
        title="Performance"
        lede="How long this host's tools take, and how much they send back."
      />
      <div className="space-y-4">
        {error && <Notice tone="problem">{error}</Notice>}
        {!data ? <Loading rows={6} /> : <Body perf={data} />}
      </div>
    </>
  );
}

function Body({ perf }: { perf: Perf }) {
  const busiest = [...perf.tools].sort(
    (a, b) => total(b.calls) - total(a.calls),
  )[0];

  if (!busiest) {
    return (
      <Card>
        <CardContent className="py-10 text-center text-sm text-muted-foreground">
          Nothing has been called yet. Numbers appear here once an assistant
          uses a tool — and reset when the host restarts.
        </CardContent>
      </Card>
    );
  }

  return (
    <>
      <Headline perf={perf} />
      <Distributions perf={perf} initial={key(busiest)} />
      <CacheCard perf={perf} />
      <ToolTable perf={perf} />
    </>
  );
}

/* -------------------------------------------------------------------------- */

/**
 * The four numbers worth reading before any chart.
 *
 * Tiles rather than charts: each is one value, and a bar of length one is a
 * number wearing a costume.
 */
function Headline({ perf }: { perf: Perf }) {
  const calls = perf.tools.reduce((n, t) => n + total(t.calls), 0);
  const failed = perf.tools.reduce((n, t) => n + t.calls.error, 0);
  const refused = perf.tools.reduce(
    (n, t) => n + t.calls.denied + t.calls.rate_limited, 0,
  );
  const slowest = perf.tools
    .filter((t) => t.duration && t.duration.count > 0)
    .sort((a, b) => (b.duration!.p95 ?? 0) - (a.duration!.p95 ?? 0))[0];
  const biggest = perf.tools
    .filter((t) => t.result_bytes && t.result_bytes.count > 0)
    .sort((a, b) => (b.result_bytes!.p95 ?? 0) - (a.result_bytes!.p95 ?? 0))[0];

  const cacheHits = perf.cache.reduce((n, c) => n + c.hit, 0);
  const cacheAll = perf.cache.reduce((n, c) => n + c.hit + c.miss + c.shared, 0);

  return (
    <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
      <Tile
        label="Tool calls"
        value={calls.toLocaleString()}
        help={
          failed || refused
            ? `${failed.toLocaleString()} failed, ${refused.toLocaleString()} refused`
            : "none failed or refused"
        }
      />
      <Tile
        label="Slowest tool (p95)"
        value={slowest ? seconds(slowest.duration!.p95) : "—"}
        help={slowest ? slowest.tool : "nothing timed yet"}
      />
      <Tile
        label="Largest answer (p95)"
        value={biggest ? bytes(biggest.result_bytes!.p95) : "—"}
        help={biggest ? biggest.tool : "nothing measured yet"}
        tone={biggest && biggest.result_bytes!.p95 >= perf.result_budget_bytes ? "problem" : undefined}
      />
      <Tile
        label="Cache hit rate"
        value={cacheAll ? `${Math.round((cacheHits / cacheAll) * 100)}%` : "—"}
        help={cacheAll ? `${cacheAll.toLocaleString()} reads` : "no cached reads yet"}
      />
    </div>
  );
}

function Tile({ label, value, help, tone }: {
  label: string; value: string; help?: string; tone?: "problem";
}) {
  return (
    <Card>
      <CardContent className="space-y-1 py-5">
        <div className="text-xs text-muted-foreground">{label}</div>
        <div
          className={
            "font-mono text-2xl tabular-nums " +
            (tone === "problem" ? "text-problem" : "")
          }
        >
          {value}
        </div>
        {help && <div className="text-xs text-muted-foreground">{help}</div>}
      </CardContent>
    </Card>
  );
}

/* -------------------------------------------------------------------------- */

/**
 * Both distributions for one tool, picked from a list.
 *
 * One tool at a time rather than every tool at once: a histogram is only
 * readable against a single population, and stacking six tools' latencies into
 * one set of bars produces a shape that belongs to none of them.
 */
function Distributions({ perf, initial }: { perf: Perf; initial: string }) {
  const [selected, setSelected] = useState(initial);
  // The selection can go stale: a tool that has not been called since a
  // restart is not in the list any more.
  const tool = perf.tools.find((t) => key(t) === selected) ?? perf.tools[0];
  if (!tool) return null;

  return (
    <Card>
      <CardHeader className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <CardTitle className="text-base">Distribution</CardTitle>
          <p className="text-sm text-muted-foreground">
            Where a tool's calls actually land, rather than an average that no
            single call resembles.
          </p>
        </div>
        <NativeSelect
          className="sm:w-72"
          value={selected}
          onChange={(e) => setSelected(e.target.value)}
          aria-label="Which tool to chart"
        >
          {perf.tools.map((t) => (
            <option key={key(t)} value={key(t)}>
              {t.plugin} · {t.tool} ({total(t.calls).toLocaleString()})
            </option>
          ))}
        </NativeSelect>
      </CardHeader>
      <CardContent className="grid gap-6 lg:grid-cols-2">
        <Histogram
          title="How long it took"
          empty="Nothing timed yet."
          d={tool.duration}
          format={seconds}
        />
        <Histogram
          title="How large the answer was"
          empty="Nothing measured yet."
          d={tool.result_bytes}
          format={bytes}
          budget={perf.result_budget_bytes}
          caption={
            <>
              Past{" "}
              <span className="font-mono">{bytes(perf.result_budget_bytes)}</span>{" "}
              an answer is cut by the client rather than by the plugin, mid-JSON
              and without a note saying what went missing.
            </>
          }
        />
      </CardContent>
    </Card>
  );
}

/**
 * One histogram.
 *
 * A single series, so no legend — the title names it. Bars carry the count that
 * fell in each bucket, which the host converted from Prometheus's cumulative
 * form; drawing the cumulative counts would give a staircase that only ever
 * climbs and looks like a distribution without being one.
 */
function Histogram({ title, empty, d, format, budget, caption }: {
  title: string;
  empty: string;
  d?: Distribution;
  format: (n: number) => string;
  budget?: number;
  caption?: React.ReactNode;
}) {
  const config = {
    count: { label: "Calls", color: "var(--chart-1)" },
  } satisfies ChartConfig;

  const rows = useMemo(
    () => (d?.buckets ?? []).map((b) => ({
      label: bucketLabel(b, format),
      count: b.count,
      over: budget !== undefined && (b.le === null || b.le > budget),
    })),
    [d, budget, format],
  );

  if (!d || d.count === 0) {
    return (
      <div className="space-y-2">
        <h3 className="text-sm font-medium">{title}</h3>
        <p className="py-10 text-center text-sm text-muted-foreground">{empty}</p>
      </div>
    );
  }

  return (
    <div className="space-y-2">
      <div className="flex items-baseline justify-between gap-2">
        <h3 className="text-sm font-medium">{title}</h3>
        <p className="font-mono text-xs text-muted-foreground tabular-nums">
          p50 {format(d.p50)} · p95 {format(d.p95)}
        </p>
      </div>
      <ChartContainer config={config} className="h-[200px] w-full">
        <BarChart data={rows} margin={{ top: 4, right: 4, bottom: 0, left: 4 }}>
          <CartesianGrid vertical={false} strokeDasharray="3 3" />
          <XAxis
            dataKey="label"
            tickLine={false}
            axisLine={false}
            interval={0}
            tickMargin={8}
            className="text-[10px]"
          />
          <YAxis
            width={32}
            tickLine={false}
            axisLine={false}
            allowDecimals={false}
            className="text-[10px]"
          />
          <ChartTooltip content={<ChartTooltipContent hideLabel={false} />} />
          {budget !== undefined && (
            <ReferenceLine
              x={rows.findIndex((r) => r.over)}
              stroke="var(--problem)"
              strokeDasharray="4 4"
            />
          )}
          {/* radius on the data end only: the bar grows from the baseline, and
              rounding the foot detaches it from the axis it is measured from. */}
          <Bar dataKey="count" radius={[4, 4, 0, 0]}>
            {rows.map((r, i) => (
              <Cell key={i} fill={r.over ? "var(--problem)" : "var(--chart-1)"} />
            ))}
          </Bar>
        </BarChart>
      </ChartContainer>
      {caption && <p className="text-xs text-muted-foreground">{caption}</p>}
    </div>
  );
}

/* -------------------------------------------------------------------------- */

/**
 * What each read cache is doing.
 *
 * Hit, shared and miss are three outcomes rather than two: a shared fetch still
 * went upstream once, for several callers at once. Folding it into the hit rate
 * would overstate what the cache is actually holding.
 *
 * Counts are written beside each bar as well as encoded in its length. That is
 * required rather than decorative — the first series colour sits below 3:1
 * against a light surface, and a visible label is what makes it legible anyway.
 */
function CacheCard({ perf }: { perf: Perf }) {
  const config = {
    hit: { label: "Hit", color: "var(--chart-1)" },
    shared: { label: "Shared", color: "var(--chart-2)" },
    miss: { label: "Missed", color: "var(--chart-3)" },
  } satisfies ChartConfig;

  if (perf.cache.length === 0) return null;

  const rows = perf.cache.map((c) => ({
    label: `${c.plugin} · ${c.kind}`,
    hit: c.hit,
    shared: c.shared,
    miss: c.miss,
    rate: c.hit + c.miss + c.shared > 0
      ? Math.round((c.hit / (c.hit + c.miss + c.shared)) * 100)
      : 0,
  }));

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Read caches</CardTitle>
        <p className="text-sm text-muted-foreground">
          A cache with no hits is costing memory and returning nothing. A kind
          that is nearly all misses is either a read that should not be cached
          or a lifetime that is too short to catch a follow-up question.
        </p>
      </CardHeader>
      <CardContent className="space-y-4">
        <ChartContainer
          config={config}
          className="w-full"
          style={{ height: `${Math.max(120, rows.length * 44)}px` }}
        >
          <BarChart data={rows} layout="vertical" margin={{ left: 8, right: 48 }}>
            <CartesianGrid horizontal={false} strokeDasharray="3 3" />
            <XAxis type="number" hide />
            <YAxis
              type="category"
              dataKey="label"
              width={150}
              tickLine={false}
              axisLine={false}
              className="text-[11px]"
            />
            <ChartTooltip content={<ChartTooltipContent />} />
            {/* A 2px gap between segments, so two fills never touch. */}
            <Bar dataKey="hit" stackId="c" fill="var(--chart-1)" radius={2} />
            <Bar dataKey="shared" stackId="c" fill="var(--chart-2)" radius={2} />
            <Bar dataKey="miss" stackId="c" fill="var(--chart-3)" radius={[0, 4, 4, 0]} />
          </BarChart>
        </ChartContainer>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Cache</TableHead>
              <TableHead className="text-right">Hit</TableHead>
              <TableHead className="text-right">Shared</TableHead>
              <TableHead className="text-right">Missed</TableHead>
              <TableHead className="text-right">Hit rate</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((r) => (
              <TableRow key={r.label}>
                <TableCell className="font-mono text-xs">{r.label}</TableCell>
                <TableCell className="text-right font-mono tabular-nums">{r.hit.toLocaleString()}</TableCell>
                <TableCell className="text-right font-mono tabular-nums">{r.shared.toLocaleString()}</TableCell>
                <TableCell className="text-right font-mono tabular-nums">{r.miss.toLocaleString()}</TableCell>
                <TableCell className="text-right font-mono tabular-nums">{r.rate}%</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </CardContent>
    </Card>
  );
}

/* -------------------------------------------------------------------------- */

/**
 * Every tool, as numbers.
 *
 * The table is not a fallback for the charts — it is how the four call outcomes
 * are read. Denied and rate-limited call for different actions than a failure
 * does, and a stacked bar four segments deep hides exactly that difference.
 */
function ToolTable({ perf }: { perf: Perf }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Every tool</CardTitle>
        <p className="text-sm text-muted-foreground">
          Refused is not failed: a denial is a grant that was never made, and a
          rate limit is a ceiling to raise or a caller to slow down.
        </p>
      </CardHeader>
      <CardContent>
        <div className="overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Tool</TableHead>
                <TableHead className="text-right">Calls</TableHead>
                <TableHead className="text-right">Failed</TableHead>
                <TableHead className="text-right">Refused</TableHead>
                <TableHead className="text-right">p50</TableHead>
                <TableHead className="text-right">p95</TableHead>
                <TableHead className="text-right">p95 size</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {perf.tools.map((t) => {
                const refused = t.calls.denied + t.calls.rate_limited;
                const big = t.result_bytes && t.result_bytes.p95 >= perf.result_budget_bytes;
                return (
                  <TableRow key={key(t)}>
                    <TableCell className="font-mono text-xs">
                      {t.plugin} · {t.tool}
                    </TableCell>
                    <TableCell className="text-right font-mono tabular-nums">
                      {total(t.calls).toLocaleString()}
                    </TableCell>
                    <TableCell className={"text-right font-mono tabular-nums " + (t.calls.error ? "text-problem" : "text-muted-foreground")}>
                      {t.calls.error.toLocaleString()}
                    </TableCell>
                    <TableCell className={"text-right font-mono tabular-nums " + (refused ? "text-attention" : "text-muted-foreground")}>
                      {refused.toLocaleString()}
                    </TableCell>
                    <TableCell className="text-right font-mono tabular-nums">
                      {t.duration?.count ? seconds(t.duration.p50) : "—"}
                    </TableCell>
                    <TableCell className="text-right font-mono tabular-nums">
                      {t.duration?.count ? seconds(t.duration.p95) : "—"}
                    </TableCell>
                    <TableCell className={"text-right font-mono tabular-nums " + (big ? "text-problem" : "")}>
                      {t.result_bytes?.count ? bytes(t.result_bytes.p95) : "—"}
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </div>
      </CardContent>
    </Card>
  );
}

/* -------------------------------------------------------------------------- */

function key(t: ToolStats): string {
  return `${t.plugin}/${t.tool}`;
}

function total(o: { ok: number; error: number; denied: number; rate_limited: number }): number {
  return o.ok + o.error + o.denied + o.rate_limited;
}

/** The x-axis label for one bucket. Null is the overflow, which has no bound. */
function bucketLabel(b: Bucket, format: (n: number) => string): string {
  return b.le === null ? "over" : `≤${format(b.le)}`;
}

function seconds(s: number): string {
  if (s === 0) return "0";
  if (s < 1) return `${Math.round(s * 1000)}ms`;
  return `${s.toFixed(s < 10 ? 1 : 0)}s`;
}

function bytes(n: number): string {
  if (n < 1024) return `${Math.round(n)}B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(n < 10240 ? 1 : 0)}K`;
  return `${(n / (1024 * 1024)).toFixed(1)}M`;
}
