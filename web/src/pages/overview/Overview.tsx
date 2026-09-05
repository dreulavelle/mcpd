import { useCallback, useEffect, useMemo, useState } from "react";
import { Bar, BarChart, XAxis } from "recharts";
import {
  api, type AuditRecord, type Caller, type CallSummary, type HealthCheck,
  type HealthReport, type Operation, type Plugin, type PluginInstance,
  type Resources, type TunnelStatus,
} from "@/lib/api";
import { describeEvent, relative, when, who } from "@/lib/format";
import { usePoll } from "@/lib/hooks";
import { Link } from "@/lib/router";
import { hasOwnName, signedInAs, useCanFn, useSession } from "@/lib/session";
import { Attention, useAttention, type Item } from "@/components/Attention";
import { Loading, Notice, PageHeader, Section } from "@/components/chrome";
import { Chip, healthTone, StatusDot, type Tone } from "@/components/status";
import { Card, CardContent } from "@/components/ui/card";
import {
  ChartContainer, ChartTooltip, ChartTooltipContent, type ChartConfig,
} from "@/components/ui/chart";
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";

/**
 * Everything the first screen knows.
 *
 * Every field is optional and stays that way until its own request answers, so
 * a card whose source was refused renders nothing rather than an empty one.
 * Health is the exception and carries a third state: `null` is "asked and did
 * not answer", which is not the same as every check passing and must never be
 * drawn as if it were.
 */
interface Snapshot {
  waiting?: Operation[];
  plugins?: Plugin[];
  instances?: PluginInstance[];
  tunnels?: TunnelStatus[];
  health?: HealthReport | null;
  resources?: Resources;
  audit?: AuditRecord[];
  summary?: CallSummary;
}

/**
 * The first screen: is anything waiting on me, is everything working, what has
 * been happening -- in that order, and nothing else.
 *
 * Nothing here is an action. Approving from a summary is what this product
 * exists to prevent, so every line is a place to go and read.
 */
export function Overview() {
  const session = useSession();
  const can = useCanFn();
  const [snap, setSnap] = useState<Snapshot>({});
  const [callers, setCallers] = useState<Caller[]>();
  const [loaded, setLoaded] = useState(false);

  const load = useCallback(() => {
    const set = (patch: Partial<Snapshot>) => setSnap((s) => ({ ...s, ...patch }));
    const jobs: Promise<unknown>[] = [];
    // Each question only where the answer would be given, and each failure
    // kept to its own card. A page that asks for what it cannot see is a page
    // that logs a refusal on every visit.
    const ask = <T,>(allowed: boolean, run: () => Promise<T>, ok: (v: T) => void, failed?: () => void) => {
      if (!allowed) return;
      jobs.push(run().then(ok, () => failed?.()));
    };

    ask(can("approvals:read"), () => api.operations("pending_approval", 50),
      (r) => set({ waiting: r.operations ?? [] }));
    ask(can("plugins:read"), () => api.plugins(), (r) => set({ plugins: r.plugins ?? [] }));
    ask(can("plugins:read"), () => api.instances(), (r) => set({ instances: r.instances ?? [] }));
    ask(can("tunnels:read"), () => api.tunnel(), (t) => set({ tunnels: t.tunnels ?? [] }));
    ask(can("system:read"), () => api.health(),
      (h) => set({ health: h }), () => set({ health: null }));
    ask(can("system:read"), () => api.resources(), (r) => set({ resources: r }));
    ask(can("history:read"), () => api.audit(6), (r) => set({ audit: r.records ?? [] }));
    ask(can("history:read"), () => api.callSummary(24), (s) => set({ summary: s }));

    Promise.allSettled(jobs).finally(() => setLoaded(true));
  }, [can]);
  usePoll(load, 15_000);

  // A week does not change in fifteen seconds, so this is asked once.
  useEffect(() => {
    if (!can("history:read")) return;
    api.callers(7).then((r) => setCallers(r.callers ?? [])).catch(() => undefined);
  }, [can]);

  const plugins = useMemo(() => snap.plugins ?? [], [snap.plugins]);
  const instances = useMemo(() => snap.instances ?? [], [snap.instances]);
  const tunnels = useMemo(() => snap.tunnels ?? [], [snap.tunnels]);
  const items = useAttention({ plugins, instances, tunnels });

  const systems = useMemo(
    () => systemRows(snap.plugins, snap.instances, snap.summary),
    [snap.plugins, snap.instances, snap.summary],
  );
  const waiting = useMemo(
    () => snap.waiting && [...snap.waiting].sort(
      (a, b) => Date.parse(a.expires_at) - Date.parse(b.expires_at),
    ),
    [snap.waiting],
  );

  const sentence = verdict({
    items,
    systems: snap.plugins && snap.instances ? systems.length : undefined,
    connectors: snap.tunnels?.length,
    connected: tunnels.filter((t) => t.state === "connected").length,
    summary: snap.summary,
    healthUnread: snap.health === null,
  });

  // Only when the account has a name of its own: `name` falls back to the
  // address, and "Hello, ops@example.com" is worse than no greeting.
  const greeting = hasOwnName(session) ? signedInAs(session).split(" ")[0] : null;

  if (!loaded) {
    return (
      <>
        <PageHeader title="Overview" />
        <Loading rows={5} />
      </>
    );
  }

  return (
    <>
      <PageHeader
        title={greeting ? `Hello, ${greeting}` : "Overview"}
        actions={
          <Link to="/clients" className="text-sm text-primary hover:underline">
            Connect a client
          </Link>
        }
      />

      {/* The one line worth reading before anything else. Everything below it
          is the working out. */}
      <p className="mb-8 flex items-start gap-2.5 text-base leading-snug font-medium sm:text-lg">
        <StatusDot tone={sentence.tone} className="mt-2 sm:mt-2.5" />
        <span>{sentence.text}</span>
      </p>

      <div className="space-y-8">
        <Attention items={items} />

        <div className="grid gap-4 lg:grid-cols-5">
          <Activity summary={snap.summary} className="lg:col-span-3" />
          <Waiting operations={waiting} className="lg:col-span-2" />
        </div>

        <div className="grid gap-4 sm:grid-cols-2">
          <Systems rows={systems} counted={snap.summary !== undefined} />
          <Connectors tunnels={snap.tunnels} />
        </div>

        <Host health={snap.health} resources={snap.resources} />

        <div className="grid gap-4 sm:grid-cols-2">
          <WhoIsCalling callers={callers} />
          <Lately records={snap.audit} />
        </div>
      </div>
    </>
  );
}

/* -------------------------------------------------------------------------- */

/** Number words, for the one sentence that reads as prose. */
const NUMBERS = [
  "no", "One", "Two", "Three", "Four", "Five", "Six", "Seven", "Eight", "Nine",
];

/**
 * How many trailing hours nobody called in, or null when the window is unknown.
 *
 * The last bucket is the hour in progress and is empty for most of it, so this
 * counts it. Three is the smallest number that cannot be an ordinary lull.
 */
function quietHours(summary?: CallSummary): number | null {
  if (!summary || summary.buckets.length === 0) return null;
  let quiet = 0;
  for (let i = summary.buckets.length - 1; i >= 0; i--) {
    const b = summary.buckets[i]!;
    if (b.ok + b.error + b.denied + b.rate_limited > 0) break;
    quiet++;
  }
  return quiet;
}

/**
 * The sentence at the top of the page.
 *
 * It replaced four counters, which between them said how many of everything
 * there was and nothing about whether any of it was well. Exported because
 * every branch of it is a claim about the host and each one is worth a test.
 *
 * An undefined count means the answer was never read, and is not zero: a
 * person who may not list the plugins must not be told there are none.
 */
export function verdict(input: {
  items: Item[];
  systems?: number;
  connectors?: number;
  connected: number;
  summary?: CallSummary;
  healthUnread: boolean;
}): { text: string; tone: Tone } {
  const parts: string[] = [];
  let tone: Tone;

  if (input.systems === 0 && input.connectors === 0) {
    parts.push("Nothing is set up yet.");
    tone = "neutral";
  } else {
    const wrong = input.items.some((i) => i.tone === "problem");
    parts.push(wrong ? "Something is wrong." : "Everything is working.");

    if (input.items.length > 0) {
      const n = input.items.length;
      const count = NUMBERS[n] ?? String(n);
      parts.push(`${count} ${n === 1 ? "thing needs" : "things need"} you.`);
      tone = wrong ? "problem" : "attention";
    } else {
      tone = "good";
      const quiet = quietHours(input.summary);
      if (quiet !== null && quiet >= 3 && input.connected > 0) {
        parts.push(`Nothing has come through for ${quiet} hours.`);
      }
    }
  }

  if (input.healthUnread) parts.push("This host's health could not be read.");
  return { text: parts.join(" "), tone };
}

/* -------------------------------------------------------------------------- */

const chartConfig = {
  ok: { label: "Worked", color: "var(--chart-1)" },
  error: { label: "Failed", color: "var(--problem)" },
  refused: { label: "Refused", color: "var(--attention)" },
} satisfies ChartConfig;

const number = (n: number) => n.toLocaleString();

/**
 * A day of calls, one bar an hour.
 *
 * Stacked rather than three charts: what a reader wants from this is the shape
 * of the day and whether the failures are a streak or a scatter, and both are
 * lost when the series are apart.
 */
function Activity({ summary, className }: { summary?: CallSummary; className?: string }) {
  const rows = useMemo(
    () => (summary?.buckets ?? []).map((b) => {
      const at = new Date(b.at);
      return {
        hour: at.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" }),
        ok: b.ok,
        error: b.error,
        refused: b.denied + b.rate_limited,
      };
    }),
    [summary],
  );

  if (!summary) return null;

  // Both ends and the middle. Taken from the rows themselves, because a tick
  // that is not one of the axis's own categories falls outside the scale and
  // draws nothing.
  const ticks = rows.length > 2
    ? [rows[0]!.hour, rows[Math.floor(rows.length / 2)]!.hour, rows[rows.length - 1]!.hour]
    : rows.map((r) => r.hour);

  const label = summary.total === 0
    ? "No calls in the last 24 hours."
    : `${number(summary.total)} calls in the last 24 hours, ${number(summary.errors)} failed and ${number(summary.denied)} refused.`;

  const busiest = summary.plugins.slice(0, 3);

  return (
    <Section title="Last 24 hours" className={className}>
      <Card>
        <CardContent className="space-y-3">
          {summary.total === 0 ? (
            <p className="text-sm text-muted-foreground">No calls in the last 24 hours.</p>
          ) : (
            <p className="flex flex-wrap items-baseline gap-x-2 text-sm text-muted-foreground">
              <span className="text-xl font-semibold text-foreground tabular-nums">
                {number(summary.total)}
              </span>
              calls
              {summary.errors > 0 && (
                <span className="text-problem tabular-nums">· {number(summary.errors)} failed</span>
              )}
              {summary.denied > 0 && (
                <span className="text-attention tabular-nums">· {number(summary.denied)} refused</span>
              )}
            </p>
          )}

          <ChartContainer
            config={chartConfig}
            className="h-[120px] w-full"
            role="img"
            aria-label={label}
          >
            <BarChart data={rows} margin={{ top: 4, right: 0, bottom: 0, left: 0 }}>
              <XAxis
                dataKey="hour"
                ticks={ticks}
                tickLine={false}
                axisLine={false}
                tickMargin={6}
                className="text-[10px]"
              />
              <ChartTooltip content={<ChartTooltipContent />} />
              <Bar dataKey="ok" stackId="calls" fill="var(--color-ok)" />
              <Bar dataKey="error" stackId="calls" fill="var(--color-error)" />
              <Bar dataKey="refused" stackId="calls" fill="var(--color-refused)" />
            </BarChart>
          </ChartContainer>

          {busiest.length > 0 && (
            <p className="text-xs text-muted-foreground">
              Busiest:{" "}
              {busiest.map((p, i) => (
                <span key={p.plugin}>
                  {i > 0 && " · "}
                  {p.plugin} {number(p.calls)}
                </span>
              ))}
            </p>
          )}
        </CardContent>
      </Card>
    </Section>
  );
}

/* -------------------------------------------------------------------------- */

/** Changes an assistant has proposed, soonest to run out of time first. */
function Waiting({ operations, className }: { operations?: Operation[]; className?: string }) {
  if (!operations) return null;

  return (
    <Section title="Waiting on you" className={className}>
      <Card className="h-full">
        <CardContent>
          {operations.length === 0 ? (
            <p className="text-sm text-muted-foreground">Nothing to decide.</p>
          ) : (
            <ul className="space-y-3">
              {operations.slice(0, 5).map((op) => (
                <li key={op.id} className="text-sm">
                  <Link
                    to={`/approvals/${encodeURIComponent(op.id)}`}
                    className="font-medium hover:underline"
                  >
                    {op.action.replace(/[._]/g, " ")}
                  </Link>
                  <div className="text-xs text-muted-foreground">
                    {op.plugin} · runs out {relative(op.expires_at)}
                  </div>
                </li>
              ))}
              {operations.length > 5 && (
                <li>
                  <Link to="/approvals" className="text-sm text-primary hover:underline">
                    {operations.length - 5} more
                  </Link>
                </li>
              )}
            </ul>
          )}
        </CardContent>
      </Card>
    </Section>
  );
}

/* -------------------------------------------------------------------------- */

interface SystemRow {
  name: string;
  tone: Tone;
  state: string;
  calls?: number;
}

/**
 * One row per system, whether or not it is serving.
 *
 * A plugin switched on and waiting for a setting appears in the instance list
 * and not the plugin list, and leaving it out is how somebody spends an
 * afternoon wondering where it went.
 */
function systemRows(
  plugins?: Plugin[],
  instances?: PluginInstance[],
  summary?: CallSummary,
): SystemRow[] {
  if (!plugins && !instances) return [];
  const calls = new Map((summary?.plugins ?? []).map((p) => [p.plugin, p.calls]));
  const rows: SystemRow[] = (plugins ?? []).map((p) => ({
    name: p.name,
    tone: healthTone(p.health),
    state: p.health === "healthy" ? "working"
      : p.health === "degraded" ? "having trouble" : "not working",
    calls: calls.get(p.name),
  }));

  const listed = new Set(rows.map((r) => r.name));
  for (const i of instances ?? []) {
    if (listed.has(i.name) || !i.enabled || i.mounted || i.removed) continue;
    rows.push({ name: i.name, tone: "attention", state: "waiting on settings" });
  }

  const rank: Record<Tone, number> = { problem: 0, attention: 1, info: 2, neutral: 3, good: 4 };
  return rows.sort((a, b) => rank[a.tone] - rank[b.tone] || a.name.localeCompare(b.name));
}

function Systems({ rows, counted }: { rows: SystemRow[]; counted: boolean }) {
  if (rows.length === 0) return null;

  return (
    <Section
      title="Systems"
      description={counted ? "Calls counted over the last 24 hours." : undefined}
    >
      <Card className="h-full">
        <CardContent>
          <ul className="space-y-2.5">
            {rows.map((r) => (
              <li key={r.name} className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5 text-sm">
                <StatusDot tone={r.tone} />
                <Link
                  to={`/plugins/${encodeURIComponent(r.name)}`}
                  className="font-medium hover:underline"
                >
                  {r.name}
                </Link>
                <span className="text-muted-foreground">{r.state}</span>
                {r.calls !== undefined && (
                  <span className="ml-auto shrink-0 text-xs text-muted-foreground tabular-nums">
                    {number(r.calls)} calls
                  </span>
                )}
              </li>
            ))}
          </ul>
        </CardContent>
      </Card>
    </Section>
  );
}

/* -------------------------------------------------------------------------- */

/** What one connector is doing, in the words the Tunnels page uses. */
function connectorState(t: TunnelStatus): { tone: Tone; state: string } {
  if (t.upstream === "missing") return { tone: "problem", state: "Pointing at a tunnel it cannot use" };
  if (t.state === "failed") {
    return t.next_retry_at
      ? { tone: "attention", state: "Down, trying again" }
      : { tone: "problem", state: "Stopped" };
  }
  if (t.degraded) return { tone: "attention", state: "Nothing getting through" };
  switch (t.state) {
    case "connected": return { tone: "good", state: "Ready" };
    case "starting": return { tone: "info", state: "Connecting" };
    case "disabled": return { tone: "neutral", state: "Switched off" };
    default: return { tone: "neutral", state: "Stopped" };
  }
}

function Connectors({ tunnels }: { tunnels?: TunnelStatus[] }) {
  if (!tunnels) return null;

  return (
    <Section title="Connectors">
      <Card className="h-full">
        <CardContent>
          {tunnels.length === 0 ? (
            <p className="text-sm text-muted-foreground">
              No connector is set up. ChatGPT reaches this host through one.
            </p>
          ) : (
            <ul className="space-y-2.5">
              {tunnels.map((t) => {
                const reading = connectorState(t);
                return (
                  <li
                    key={t.tunnel_id ?? t.plugin ?? "all"}
                    className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5 text-sm"
                  >
                    <StatusDot tone={reading.tone} />
                    <Link to="/tunnels" className="font-medium hover:underline">
                      {t.plugin || "everything"}
                    </Link>
                    <span className="text-muted-foreground">{reading.state}</span>
                    {t.requests !== undefined && t.requests > 0 && (
                      <span className="ml-auto shrink-0 text-xs text-muted-foreground tabular-nums">
                        {number(t.requests)} requests
                      </span>
                    )}
                  </li>
                );
              })}
            </ul>
          )}
        </CardContent>
      </Card>
    </Section>
  );
}

/* -------------------------------------------------------------------------- */

const CHECK_LABEL: Record<HealthCheck["status"], string> = {
  up: "Passing",
  degraded: "Degraded",
  down: "Down",
};

/** How long the process has been up, in the largest unit that is honest. */
function uptime(seconds: number): string {
  const units: [string, number][] = [["day", 86_400], ["hour", 3_600], ["minute", 60]];
  for (const [unit, size] of units) {
    const n = Math.floor(seconds / size);
    if (n >= 1) return `${n} ${unit}${n === 1 ? "" : "s"}`;
  }
  return "less than a minute";
}

/**
 * One line about the host, and the checks behind it on request.
 *
 * The checks open on their own when one is failing, because the reason is the
 * only thing anybody opens this for; a person who closes it has closed it.
 */
function Host({ health, resources }: { health?: HealthReport | null; resources?: Resources }) {
  const checks = useMemo(() => {
    const rank: Record<HealthCheck["status"], number> = { down: 0, degraded: 1, up: 2 };
    return [...(health?.checks ?? [])].sort(
      (a, b) => rank[a.status] - rank[b.status] || a.name.localeCompare(b.name),
    );
  }, [health]);
  const failing = checks.filter((c) => c.status !== "up");
  const [opened, setOpened] = useState<boolean | null>(null);
  const open = opened ?? failing.length > 0;

  if (health === undefined && resources === undefined) return null;

  const sentence = health === null
    ? null
    : checks.length === 0
      ? "This build runs no checks on itself."
      : failing.length === 0
        ? `${checks.length === 1 ? "The one check is" : `All ${checks.length} checks are`} passing.`
        : `${failing.length} of ${checks.length} ${failing.length === 1 ? "checks is" : "checks are"} not passing.`;

  return (
    <Section title="Host">
      <Card>
        <CardContent className="space-y-3">
          {health === null ? (
            <Notice tone="problem">
              This host's health could not be read, so nothing here says whether
              its checks are passing.
            </Notice>
          ) : (
            <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
              <p className="text-sm">{sentence}</p>
              {checks.length > 0 && (
                <button
                  type="button"
                  onClick={() => setOpened(!open)}
                  className="text-sm text-primary hover:underline"
                >
                  {open ? "Hide checks" : "Show checks"}
                </button>
              )}
            </div>
          )}

          {resources && (
            <p className="text-xs text-muted-foreground">
              mcpd {resources.version} · running for {uptime(resources.uptime_seconds)}
            </p>
          )}

          {health !== null && open && checks.length > 0 && (
            <div className="scroll-x">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Check</TableHead>
                    <TableHead>State</TableHead>
                    <TableHead>What it said</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {checks.map((c) => (
                    <TableRow key={c.name}>
                      <TableCell className="font-medium">
                        {c.name}
                        {/* Only said of a check that is failing. On a passing
                            one it is noise; on a failing one it is the
                            difference between "the host is down" and "one
                            optional thing is". */}
                        {c.status !== "up" && !c.critical && (
                          <span className="ml-2 text-xs font-normal text-muted-foreground">
                            not critical
                          </span>
                        )}
                      </TableCell>
                      <TableCell>
                        <Chip tone={healthTone(c.status)}>
                          <StatusDot tone={healthTone(c.status)} />
                          {CHECK_LABEL[c.status]}
                        </Chip>
                      </TableCell>
                      <TableCell className="max-w-[52ch] text-muted-foreground">
                        {c.message || (c.status === "up" ? "Nothing to report." : "It gave no detail.")}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          )}
        </CardContent>
      </Card>
    </Section>
  );
}

/* -------------------------------------------------------------------------- */

/** Which credentials have been busy, for the question "should this still exist". */
function WhoIsCalling({ callers }: { callers?: Caller[] }) {
  if (!callers) return null;

  return (
    <Section title="Who has been calling" description="The last seven days.">
      <Card className="h-full">
        <CardContent>
          {callers.length === 0 ? (
            <p className="text-sm text-muted-foreground">Nobody has called yet.</p>
          ) : (
            <ul className="space-y-2.5">
              {callers.slice(0, 5).map((c) => (
                <li key={c.principal} className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5 text-sm">
                  {/* The principal as it is stored, not prettied: it is what
                      somebody types into a filter and what a key is revoked
                      by, and the Activity page names it the same way. */}
                  <Link
                    to={`/activity?principal=${encodeURIComponent(c.principal)}`}
                    className="min-w-0 truncate font-medium hover:underline"
                  >
                    {c.principal}
                  </Link>
                  <span className="ml-auto shrink-0 text-xs text-muted-foreground tabular-nums">
                    {number(c.calls)} calls
                    {c.errors > 0 && (
                      <span className="text-problem"> · {number(c.errors)} failed</span>
                    )}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>
    </Section>
  );
}

/** The audit trail's last few lines. */
function Lately({ records }: { records?: AuditRecord[] }) {
  if (!records) return null;

  return (
    <Section
      title="Lately"
      actions={
        <Link to="/audit" className="text-sm text-primary hover:underline">
          Full audit
        </Link>
      }
    >
      <Card className="h-full">
        <CardContent>
          {records.length === 0 ? (
            <p className="text-sm text-muted-foreground">Nothing recorded yet.</p>
          ) : (
            <ul className="space-y-2">
              {records.map((r) => (
                <li key={r.seq} className="flex flex-wrap items-baseline gap-x-2 text-sm">
                  <span className="w-28 shrink-0 text-xs text-muted-foreground tabular-nums">
                    {when(r.at)}
                  </span>
                  <span className="min-w-0 flex-1">{describeEvent(r)}</span>
                  <span className="text-xs text-muted-foreground">{who(r.actor)}</span>
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>
    </Section>
  );
}
