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

/** How long a window is, in the words a sentence needs. */
function windowWords(hours: number): string {
  return hours === 1 ? "hour" : `${hours} hours`;
}

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

/** How many hours of calls the chart draws. */
const WINDOW_HOURS = 24;

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

  const set = useCallback(
    (patch: Partial<Snapshot>) => setSnap((s) => ({ ...s, ...patch })),
    [],
  );

  const load = useCallback(() => {
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

    Promise.allSettled(jobs).finally(() => setLoaded(true));
  }, [can, set]);
  usePoll(load, 15_000);

  // A day of hourly buckets does not change meaningfully in fifteen seconds,
  // and it is the most expensive thing on the page: two aggregates over the
  // busiest table this host writes.
  const loadSummary = useCallback(() => {
    if (!can("history:read")) return;
    api.callSummary(WINDOW_HOURS).then((s) => set({ summary: s })).catch(() => undefined);
  }, [can, set]);
  usePoll(loadSummary, 60_000);

  // A week does not change while somebody reads a screen, so this is asked once.
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
    waiting: snap.waiting?.length,
    systems: snap.plugins && snap.instances ? systems.length : undefined,
    connectors: snap.tunnels?.length,
    connected: tunnels.filter((t) => t.state === "connected").length,
    summary: snap.summary,
    health: snap.health,
  });

  // Whether any card below drew anything. A custom role can hold none of the
  // read permissions, and a header over an empty page reads as a dashboard
  // that is broken rather than one this account may not see.
  const anything = items.length > 0 || snap.summary !== undefined
    || snap.waiting !== undefined || systems.length > 0
    || snap.tunnels !== undefined || snap.health !== undefined
    || snap.resources !== undefined || callers !== undefined
    || snap.audit !== undefined;

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
        actions={can("plugins:read") && (
          <Link to="/clients" className="text-sm text-primary hover:underline">
            Connect a client
          </Link>
        )}
      />

      {/* The one line worth reading before anything else; everything below it
          is the working out. Size carries it rather than weight, which at this
          length would read as shouting. */}
      {sentence && (
        <p className="mb-8 flex items-start gap-3 text-xl leading-snug text-balance sm:text-2xl">
          <StatusDot tone={sentence.tone} className="mt-2.5 sm:mt-3" />
          <span>{sentence.text}</span>
        </p>
      )}

      {!anything && (
        <p className="text-sm text-muted-foreground">
          Nothing on this host is visible to your account. Ask an administrator
          for the access you need.
        </p>
      )}

      <div className="space-y-8">
        <Attention items={items} />

        <div className="grid gap-4 lg:grid-cols-5">
          <Activity summary={snap.summary} className="lg:col-span-3" />
          <Waiting operations={waiting} className="lg:col-span-2" />
        </div>

        <div className="grid gap-x-10 gap-y-6 sm:grid-cols-2">
          <Systems rows={systems} hours={snap.summary?.hours} />
          <Connectors tunnels={snap.tunnels} />
        </div>

        <Host health={snap.health} resources={snap.resources} />

        <div className="grid gap-x-10 gap-y-6 sm:grid-cols-2">
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

/** All the calls in one bucket, however they ended. */
const busy = (b: { ok: number; error: number; denied: number; rate_limited: number }) =>
  b.ok + b.error + b.denied + b.rate_limited;

/**
 * When the last call came in, if the run of empty hours since is long enough
 * to be worth saying. Null when the window is unknown, still busy, only
 * briefly idle, or empty end to end -- three hours is the smallest gap that
 * cannot be a lull, and a window with no calls at all has no "since".
 *
 * The moment the host reported, not the hour its bucket opened: a call made at
 * 09:59 reported as 09:00 is wrong by an hour in the sentence somebody acts on.
 */
function quietSince(summary?: CallSummary): string | null {
  if (!summary?.last_at || summary.buckets.length === 0) return null;
  let empty = 0;
  for (let i = summary.buckets.length - 1; i >= 0; i--) {
    if (busy(summary.buckets[i]!) > 0) break;
    empty++;
  }
  return empty >= 3 ? summary.last_at : null;
}

/** A moment in the last half day is a time; older than that needs the date. */
function moment(iso: string, now: number = Date.now()): string {
  const at = new Date(iso);
  if (Number.isNaN(at.getTime())) return iso;
  if (now - at.getTime() > 12 * 3_600_000) return when(iso);
  return at.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

/**
 * The sentence at the top of the page.
 *
 * It replaced four counters, which between them said how many of everything
 * there was and nothing about whether any of it was well. Exported because
 * every branch of it is a claim about the host and each one is worth a test.
 *
 * Two rules hold it honest. An undefined count means the answer was never
 * read, and is not zero: a person who may not list the systems must not be
 * told there are none. And the lead -- "Everything is working." either way --
 * is only said when something actually answered, because a host that could not
 * be reached at all is not a host that is well. Null means there was nothing
 * to say, which is better than a claim about a host nobody could see.
 */
export function verdict(input: {
  items: Item[];
  /** Changes waiting on a decision. One of those is a thing that needs the reader. */
  waiting?: number;
  systems?: number;
  connectors?: number;
  connected: number;
  summary?: CallSummary;
  /** Undefined when it was never asked; null when it was asked and did not answer. */
  health?: HealthReport | null;
}): { text: string; tone: Tone } | null {
  const down = input.health?.status === "down";
  const degraded = input.health?.status === "degraded";
  const unread = input.health === null;

  // Something answered that speaks to the host's state. A refused or failed
  // read is an absence of an answer, not one: with the server unreachable the
  // page led with a green "Everything is working." over nothing at all.
  const known = input.systems !== undefined || input.connectors !== undefined
    || (input.health !== undefined && !unread);
  const empty = input.systems === 0 && input.connectors === 0;

  // A line saying a new version exists is worth reading and is not a thing
  // anybody has to do today, so it does not turn the headline amber.
  const needing = input.items.filter((i) => i.tone !== "info").length + (input.waiting ?? 0);
  const wrong = down || input.items.some((i) => i.tone === "problem");

  const parts: string[] = [];
  let well = false;
  if (wrong) {
    // A critical check that is down makes the host wrong whatever the system
    // list says. "Everything is working" over a Host card reading "1 of 3
    // checks is not passing" is the sentence somebody believes instead of the
    // card.
    parts.push("Something is wrong.");
  } else if (degraded) {
    // Said instead of "Everything is working.", never after it: a sentence
    // should not be contradicted by the one that follows it.
    parts.push("A check on this host is not passing.");
  } else if (empty) {
    parts.push("Nothing is set up yet.");
  } else if (known) {
    parts.push("Everything is working.");
    well = true;
  }

  if (needing > 0) {
    const count = NUMBERS[needing] ?? String(needing);
    parts.push(`${count} ${needing === 1 ? "thing needs" : "things need"} you.`);
  }

  // Only when nothing else has been said. A host with something waiting on it
  // has a more useful next sentence than how long it has been quiet.
  if (well && needing === 0 && input.connected > 0) {
    const since = quietSince(input.summary);
    if (since) parts.push(`Nothing has come through since ${moment(since)}.`);
  }

  if (unread) parts.push("This host's health could not be read.");

  if (parts.length === 0) return null;
  const tone: Tone = wrong ? "problem"
    : (degraded || needing > 0 || unread) ? "attention"
      : empty ? "neutral" : "good";
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
        // A call turned away by the gate and one turned away by a rate limit
        // are the same answer to the person reading this: it did not run and
        // nobody's system was touched.
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

  // Refused is counted from the buckets rather than taken from `denied`, which
  // is the gate's refusals alone. The bars stack both, and a total under the
  // chart that disagreed with it would be the one somebody quotes.
  const refused = summary.buckets.reduce((t, b) => t + b.denied + b.rate_limited, 0);
  const span = windowWords(summary.hours);
  const calls = summary.total === 1 ? "call" : "calls";
  const label = summary.total === 0
    ? `No calls in the last ${span}.`
    : `${number(summary.total)} ${calls} in the last ${span}, ${number(summary.errors)} failed and ${number(refused)} refused.`;

  return (
    <Section title={`Last ${span}`} className={className}>
      <Card>
        <CardContent className="space-y-3">
          {summary.total === 0 ? (
            <p className="text-sm text-muted-foreground">No calls in the last {span}.</p>
          ) : (
            <p className="flex flex-wrap items-baseline gap-x-2 text-sm text-muted-foreground">
              <span className="text-xl font-semibold text-foreground tabular-nums">
                {number(summary.total)}
              </span>
              {calls}
              {summary.errors > 0 && (
                <span className="text-problem tabular-nums">· {number(summary.errors)} failed</span>
              )}
              {refused > 0 && (
                <span className="text-attention tabular-nums">· {number(refused)} refused</span>
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
              {/* Animation off: the page polls, and bars that grow from zero on
                  every answer read as a day that just started. */}
              <Bar dataKey="ok" stackId="calls" fill="var(--color-ok)" isAnimationActive={false} />
              <Bar dataKey="error" stackId="calls" fill="var(--color-error)" isAnimationActive={false} />
              <Bar dataKey="refused" stackId="calls" fill="var(--color-refused)" isAnimationActive={false} />
            </BarChart>
          </ChartContainer>
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
 * A system that is switched on and waiting for a setting, or switched off
 * outright, is in the instance list and not the serving one. Leaving either
 * out is how somebody spends an afternoon wondering where it went -- and how a
 * host whose systems are all switched off came to read "Nothing is set up
 * yet." A removal waiting to be restored is the one thing left out; it is
 * gone, and the Plugins page is where it comes back.
 */
function systemRows(
  serving?: Plugin[],
  instances?: PluginInstance[],
  summary?: CallSummary,
): SystemRow[] {
  if (!serving && !instances) return [];
  const calls = new Map((summary?.plugins ?? []).map((p) => [p.plugin, p.calls]));
  const rows: SystemRow[] = (serving ?? []).map((p) => ({
    name: p.name,
    tone: healthTone(p.health),
    state: p.health === "healthy" ? "working"
      : p.health === "degraded" ? "having trouble" : "not working",
    calls: calls.get(p.name),
  }));

  const listed = new Set(rows.map((r) => r.name));
  for (const i of instances ?? []) {
    if (listed.has(i.name) || i.mounted || i.removed) continue;
    rows.push(i.enabled
      ? { name: i.name, tone: "attention", state: "waiting on settings", calls: calls.get(i.name) }
      : { name: i.name, tone: "neutral", state: "disabled", calls: calls.get(i.name) });
  }

  const rank: Record<Tone, number> = { problem: 0, attention: 1, info: 2, neutral: 3, good: 4 };
  return rows.sort((a, b) => rank[a.tone] - rank[b.tone] || a.name.localeCompare(b.name));
}

function Systems({ rows, hours }: { rows: SystemRow[]; hours?: number }) {
  if (rows.length === 0) return null;

  return (
    <Section
      title="Systems"
      description={hours ? `Calls counted over the last ${windowWords(hours)}.` : undefined}
    >
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
    </Section>
  );
}

/* -------------------------------------------------------------------------- */

/**
 * What one connector is doing, in the same lower-case shorthand the systems
 * beside it use. These are labels in a list rather than sentences; the whole
 * reading, with what to do about it, is on the Tunnels page.
 */
function connectorState(t: TunnelStatus): { tone: Tone; state: string } {
  if (t.upstream === "missing") return { tone: "problem", state: "not in this account" };
  if (t.state === "failed") {
    return t.next_retry_at
      ? { tone: "attention", state: "down, trying again" }
      : { tone: "problem", state: "stopped" };
  }
  if (t.degraded) return { tone: "attention", state: "nothing getting through" };
  switch (t.state) {
    case "connected": return { tone: "good", state: "ready" };
    case "starting": return { tone: "info", state: "connecting" };
    case "disabled": return { tone: "neutral", state: "disabled" };
    default: return { tone: "neutral", state: "stopped" };
  }
}

function Connectors({ tunnels }: { tunnels?: TunnelStatus[] }) {
  if (!tunnels) return null;

  return (
    <Section title="Connectors">
      {tunnels.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          No connector is set up yet. Set one up on{" "}
          <Link to="/tunnels" className="text-primary hover:underline">Tunnels</Link>.
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
      {callers.length === 0 ? (
        <p className="text-sm text-muted-foreground">Nobody has called yet.</p>
      ) : (
        <ul className="space-y-2.5">
          {callers.slice(0, 5).map((c) => (
            <li key={c.principal} className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5 text-sm">
              {/* The principal as it is stored, not prettied: it is what
                  somebody types into a filter and what a key is revoked by,
                  and the Activity page names it the same way. */}
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
    </Section>
  );
}
