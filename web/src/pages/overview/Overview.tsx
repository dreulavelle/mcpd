import { useCallback, useMemo, useState } from "react";
import { ClipboardCheck } from "lucide-react";
import {
  api, type AuditRecord, type Endpoints, type HealthCheck, type HealthReport,
  type Operation, type Plugin, type PluginInstance, type TunnelInfo,
} from "@/lib/api";
import { describeEvent, relative, when, who } from "@/lib/format";
import { usePoll } from "@/lib/hooks";
import { Link } from "@/lib/router";
import { hasOwnName, signedInAs, useSession } from "@/lib/session";
import { Attention } from "@/components/Attention";
import {
  CodeBlock, Copyable, EmptyState, Loading, Notice, PageHeader, Section,
} from "@/components/chrome";
import { Chip, healthTone, RiskBadge, StateBadge, StatusDot } from "@/components/status";
import { Card, CardContent } from "@/components/ui/card";
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";

interface Snapshot {
  waiting: Operation[];
  plugins: Plugin[];
  instances: PluginInstance[];
  tunnels: TunnelInfo | null;
  health: HealthReport | null;
  audit: AuditRecord[];
  /** Undefined while in flight, null once it has failed. Different things. */
  endpoints: Endpoints | null | undefined;
}

/**
 * The first screen: is anything waiting on me, then the shape of the
 * deployment. Nothing here is an action -- approving from a summary is what
 * this product exists to prevent -- and every call may fail on its own.
 */
export function Overview() {
  const session = useSession();
  const [snap, setSnap] = useState<Snapshot>({
    waiting: [], plugins: [], instances: [], tunnels: null, health: null,
    audit: [], endpoints: undefined,
  });
  const [loaded, setLoaded] = useState(false);

  const load = useCallback(() => {
    const set = (patch: Partial<Snapshot>) => setSnap((s) => ({ ...s, ...patch }));
    Promise.allSettled([
      api.operations("pending_approval", 50)
        .then((r) => set({ waiting: r.operations ?? [] })),
      api.plugins().then((r) => set({ plugins: r.plugins ?? [] })),
      api.instances().then((r) => set({ instances: r.instances ?? [] })),
      api.tunnel().then((t) => set({ tunnels: t })),
      api.health().then((h) => set({ health: h })),
      api.audit(8).then((r) => set({ audit: r.records ?? [] })),
      api.endpoints()
        .then((e) => set({ endpoints: e }))
        .catch(() => set({ endpoints: null })),
    ]).finally(() => setLoaded(true));
  }, []);
  usePoll(load, 15_000);

  const waiting = useMemo(
    () => [...snap.waiting].sort(
      (a, b) => Date.parse(a.expires_at) - Date.parse(b.expires_at),
    ),
    [snap.waiting],
  );

  const unhealthy = snap.plugins.filter((p) => p.health !== "healthy");
  const notRunning = snap.instances.filter((i) => i.enabled && !i.mounted);
  const connected = (snap.tunnels?.tunnels ?? []).filter((t) => t.state === "connected");
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
        lede="What this host is doing, and what is waiting on somebody."
      />

      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <Tile
          to="/approvals"
          label="Waiting on a decision"
          value={waiting.length}
          tone={waiting.length > 0 ? "attention" : "good"}
          note={waiting.length === 0 ? "Nothing to decide." : "Read each one before deciding."}
        />
        <Tile
          to="/plugins"
          label="Plugins serving"
          value={snap.plugins.filter((p) => p.endpoint !== "").length}
          tone={unhealthy.length > 0 || notRunning.length > 0 ? "attention" : "good"}
          note={unhealthy.length > 0
            ? `${unhealthy.length} not healthy`
            : notRunning.length > 0
              ? `${notRunning.length} waiting on settings`
              : "All healthy."}
        />
        <Tile
          to="/tunnels"
          label="Connectors up"
          value={connected.length}
          tone={connected.length > 0 ? "good" : "neutral"}
          note={snap.tunnels?.tunnels.length
            ? `of ${snap.tunnels.tunnels.length} configured`
            : "None configured."}
        />
        <Tile
          label="Host"
          value={snap.health
            ? (snap.health.status === "up" ? "Up"
              : snap.health.status === "down" ? "Down" : "Degraded")
            : "Unknown"}
          tone={snap.health ? healthTone(snap.health.status) : "neutral"}
          note={snap.health
            ? `${(snap.health.checks ?? []).filter((c) => c.status === "up").length} of ${(snap.health.checks ?? []).length} checks passing`
            : "Couldn't read the health endpoint."}
        />
      </div>

      <div className="mt-8 space-y-8">
        {/* Waiting changes have their own table below; this is everything
            else, including the unhealthy plugins that were a notice here. */}
        <Attention
          plugins={snap.plugins}
          instances={snap.instances}
          tunnels={snap.tunnels?.tunnels ?? []}
        />

        <Section
          title="Waiting on a decision"
          description="A proposal expires. The soonest is first."
        >
          {waiting.length === 0 ? (
            <EmptyState mark={<ClipboardCheck />} title="Nothing waiting">
              An assistant has not proposed anything that needs deciding.
            </EmptyState>
          ) : (
            <Card className="overflow-hidden p-0">
              <div className="scroll-x">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>Change</TableHead>
                      <TableHead>Risk</TableHead>
                      <TableHead>Proposed by</TableHead>
                      <TableHead>Expires</TableHead>
                      <TableHead>State</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {waiting.slice(0, 8).map((op) => (
                      <TableRow key={op.id}>
                        <TableCell>
                          <Link
                            to={`/approvals/${encodeURIComponent(op.id)}`}
                            className="font-medium hover:underline"
                          >
                            {op.action.replace(/[._]/g, " ")}
                          </Link>
                          <div className="text-xs text-muted-foreground">{op.plugin}</div>
                        </TableCell>
                        <TableCell><RiskBadge risk={op.risk} /></TableCell>
                        <TableCell className="text-muted-foreground">{op.requested_by}</TableCell>
                        <TableCell className="whitespace-nowrap text-attention">
                          {relative(op.expires_at)}
                        </TableCell>
                        <TableCell><StateBadge state={op.state} /></TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
            </Card>
          )}
        </Section>

        <HostHealth health={snap.health} />

        <ConnectingDirectly endpoints={snap.endpoints} />

        <Section
          title="Lately"
          actions={
            <Link to="/audit" className="text-sm text-primary hover:underline">
              Full audit
            </Link>
          }
        >
          <Card>
            <CardContent>
              {snap.audit.length === 0 ? (
                <p className="text-sm text-muted-foreground">Nothing recorded yet.</p>
              ) : (
                <ul className="space-y-2">
                  {snap.audit.map((r) => (
                    <li key={r.seq} className="flex flex-wrap items-baseline gap-x-2 text-sm">
                      <span className="w-28 shrink-0 text-xs text-muted-foreground tabular-nums">
                        {when(r.at)}
                      </span>
                      <span className="flex-1">{describeEvent(r)}</span>
                      <span className="text-xs text-muted-foreground">{who(r.actor)}</span>
                    </li>
                  ))}
                </ul>
              )}
            </CardContent>
          </Card>
        </Section>
      </div>
    </>
  );
}

/**
 * How to reach this host directly: the aggregate endpoint, which belongs to the
 * deployment rather than to any one plugin. It says so when it cannot say the
 * address, because returning null looks like a host that has none.
 */
function ConnectingDirectly({ endpoints }: { endpoints: Endpoints | null | undefined }) {
  return (
    <Section title="Connecting directly">
      <Card>
        <CardContent className="space-y-3">
          <p className="text-sm text-muted-foreground">
            For clients that can reach this machine. ChatGPT uses a tunnel
            instead.
          </p>
          {endpoints === undefined ? (
            <Loading rows={1} />
          ) : endpoints === null ? (
            <Notice tone="attention">
              Couldn't read this host's address from{" "}
              <code className="font-mono">/api/endpoints</code>. The host may
              still be serving it — this card is the only thing that failed.
            </Notice>
          ) : (
            <Copyable
              value={endpoints.advertised
                ? endpoints.aggregate
                : `http://${window.location.hostname}:${endpoints.port}${endpoints.aggregate}`}
              label="address"
            />
          )}
          <CodeBlock>{"Authorization: Bearer YOUR_KEY"}</CodeBlock>
          <p className="text-xs text-muted-foreground">
            A key is issued under{" "}
            <Link to="/settings/keys" className="text-primary hover:underline">
              Settings › API Keys
            </Link>
            , and shown once. What to paste into Claude Code, Codex or an IDE
            is on{" "}
            <Link to="/clients" className="text-primary hover:underline">Clients</Link>.
          </p>
        </CardContent>
      </Card>
    </Section>
  );
}

const CHECK_LABEL: Record<HealthCheck["status"], string> = {
  up: "Passing",
  degraded: "Degraded",
  down: "Down",
};

/** What `/api/health` said, check by check, failing ones first. */
function HostHealth({ health }: { health: HealthReport | null }) {
  const checks = useMemo(() => {
    const rank: Record<HealthCheck["status"], number> = { down: 0, degraded: 1, up: 2 };
    return [...(health?.checks ?? [])].sort(
      (a, b) => rank[a.status] - rank[b.status] || a.name.localeCompare(b.name),
    );
  }, [health]);

  const failing = checks.filter((c) => c.status !== "up");

  return (
    <Section
      title="Host health"
      description="Every check this host runs on itself, and what each one last said."
    >
      {health === null ? (
        <Notice tone="problem">
          Couldn't read <code className="font-mono">/api/health</code>, so this
          says nothing either way about whether the host is well.
        </Notice>
      ) : checks.length === 0 ? (
        <EmptyState title="No checks registered">
          This build runs no health checks, so the status above is the whole
          answer.
        </EmptyState>
      ) : (
        <Card className="overflow-hidden p-0">
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
                      {/* Only said of a check that is failing. On a passing one
                          it is noise; on a failing one it is the difference
                          between "the host is down" and "one optional thing
                          is". */}
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
        </Card>
      )}

      {failing.length > 0 && (
        <p className="text-xs text-muted-foreground">
          {failing.length} of {checks.length} checks{" "}
          {failing.length === 1 ? "is" : "are"} not passing.
        </p>
      )}
    </Section>
  );
}

function Tile({ to, label, value, note, tone }: {
  to?: string;
  label: string;
  value: number | string;
  note: string;
  tone: "good" | "attention" | "problem" | "info" | "neutral";
}) {
  const body = (
    <Card className="h-full transition-colors hover:border-ring/40">
      <CardContent className="space-y-1">
        <div className="flex items-center gap-1.5 text-xs font-medium tracking-wide text-muted-foreground uppercase">
          <StatusDot tone={tone} />
          {label}
        </div>
        <div className="text-2xl font-semibold tabular-nums">{value}</div>
        <p className="text-xs text-muted-foreground">{note}</p>
      </CardContent>
    </Card>
  );
  return to ? <Link to={to} className="block">{body}</Link> : body;
}
