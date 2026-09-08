import { useEffect, useMemo, useState } from "react";
import { api, type Statistics as Stats, type ToolTotals } from "@/lib/api";
import { EmptyState, Loading, Notice, PageHeader, Section } from "@/components/chrome";
import { Segmented } from "@/components/Segmented";
import { Chip } from "@/components/status";
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";
import { relative } from "@/lib/format";
import { useQueryParam } from "@/lib/router";
import { cn } from "@/lib/utils";
import {
  WINDOWS, againstBudget, bytes, condense, count, headline, latency,
  strideLabel, successRate, tokensPerCall,
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
  const series = useMemo(() => condense(stats.series), [stats.series]);

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
        />
        <Tile label="Typical call" value={latency(totals.meanUS)}
          help={`averaged over the ${count(totals.timed)} that ran`} />
        <Tile
          label="Context spent"
          value={`~${count(totals.tokens)} tokens`}
          help={`${bytes(totals.contextBytes)} on the wire, estimated`}
        />
      </div>

      {series.length > 1 && <Traffic series={series} stride={strideLabel(stats.stride_seconds)} />}

      <Section
        title="Every tool"
        description="What each one costs to call, and how often it answers. Sorted by how much it is used."
      >
        <ToolTable stats={stats} />
      </Section>

      {stats.plugins.length > 1 && (
        <Section title="By plugin" description="The same calls, summed per integration.">
          <div className="space-y-2">
            {stats.plugins.map((p) => {
              const rate = successRate(p);
              return (
                <div key={p.plugin} className="flex items-center gap-3 rounded-md border bg-card px-3 py-2">
                  <span className="min-w-0 flex-1 truncate text-sm font-medium">{p.plugin}</span>
                  <span className="hidden w-24 text-xs text-muted-foreground tabular-nums sm:block">
                    {p.tools} {p.tools === 1 ? "tool" : "tools"}
                  </span>
                  <span className="w-20 text-right text-sm tabular-nums">{count(p.calls)}</span>
                  <span className="w-16 text-right text-sm text-muted-foreground tabular-nums">
                    {rate === null ? "—" : `${Math.round(rate * 100)}%`}
                  </span>
                  <span className="w-20 text-right text-sm text-muted-foreground tabular-nums">
                    {latency(p.mean_us)}
                  </span>
                </div>
              );
            })}
          </div>
        </Section>
      )}

      <p className="text-xs text-muted-foreground">
        Token figures are an estimate. This host is the server and never sees
        what a model was actually charged, so they are the bytes it sent —
        counted twice, because the protocol carries a result as structured
        content and again as text — divided by {stats.bytes_per_token}.
      </p>
    </div>
  );
}

function Tile({ label, value, help, tone }: {
  label: string;
  value: string;
  help?: string;
  tone?: "problem";
}) {
  return (
    <div className="rounded-lg border bg-card p-4">
      <p className="text-xs text-muted-foreground">{label}</p>
      <p className={cn("mt-1 text-2xl font-semibold tabular-nums",
        tone === "problem" && "text-problem")}>
        {value}
      </p>
      {help && <p className="mt-1 text-xs text-muted-foreground">{help}</p>}
    </div>
  );
}

/**
 * Calls over the span, successes and everything else stacked.
 *
 * Bars rather than a line: the series is a count per bucket, and a line
 * between two counts implies a value at every moment between them.
 *
 * The upper band is deliberately not called failure. A refused call is in it,
 * and a refusal is a working host doing its job -- painting a burst of them
 * red and labelling it "failures" sends somebody hunting a broken integration
 * that does not exist.
 */
function Traffic({ series, stride }: {
  series: import("@/lib/api").StatsPoint[];
  stride: string;
}) {
  const peak = Math.max(...series.map((p) => p.calls), 1);

  return (
    <Section
      title="When it was used"
      description={`Calls per ${stride}. The band on top is calls that did not succeed, refusals included.`}
    >
      <div className="rounded-lg border bg-card p-4">
        <div className="flex h-32 items-end gap-px" role="img"
          aria-label={`Calls over time, peaking at ${peak} in one bucket`}>
          {series.map((p) => (
            <div
              key={p.at}
              className="flex min-w-0 flex-1 flex-col justify-end"
              title={`${new Date(p.at).toLocaleString()}: ${p.calls} calls, ${p.not_ok} did not succeed`}
            >
              {p.not_ok > 0 && (
                <div className="w-full rounded-t-[1px] bg-attention"
                  style={{ height: `${(p.not_ok / peak) * 100}%` }} />
              )}
              <div
                className={cn("w-full bg-[var(--chart-1)]", p.not_ok === 0 && "rounded-t-[1px]")}
                style={{ height: `${(p.ok / peak) * 100}%` }}
              />
            </div>
          ))}
        </div>
        <div className="mt-2 flex justify-between text-xs text-muted-foreground">
          <span>{relative(series[0]!.at)}</span>
          <span>{relative(series[series.length - 1]!.at)}</span>
        </div>
      </div>
    </Section>
  );
}

function ToolTable({ stats }: { stats: Stats }) {
  return (
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
          {stats.tools.map((t) => (
            <ToolRow key={`${t.plugin}/${t.tool}`} t={t} stats={stats} />
          ))}
        </TableBody>
      </Table>
    </div>
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
  const share = againstBudget(t, stats.result_budget_bytes);

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
        {share !== null && share >= 0.8 && (
          <Chip tone="attention" className="ml-2">
            {share >= 1 ? "truncated" : "near the cap"}
          </Chip>
        )}
      </TableCell>
      <TableCell className="text-right tabular-nums text-muted-foreground">
        {tokens === null ? "—" : `~${count(tokens)}`}
      </TableCell>
    </TableRow>
  );
}
