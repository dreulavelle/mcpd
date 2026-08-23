import { useCallback, useMemo, useState } from "react";
import { ClipboardCheck } from "lucide-react";
import {
  api, type AuditRecord, type HealthReport, type Operation, type Plugin,
  type PluginInstance, type TunnelInfo,
} from "@/lib/api";
import { describeEvent, relative, when, who } from "@/lib/format";
import { usePoll } from "@/lib/hooks";
import { Link } from "@/lib/router";
import { useSession } from "@/lib/session";
import { EmptyState, Loading, Notice, PageHeader, Section } from "@/components/chrome";
import { healthTone, RiskBadge, StateBadge, StatusDot } from "@/components/status";
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
}

/**
 * The first screen.
 *
 * It answers one question -- is anything waiting on me -- and then gives the
 * shape of the deployment underneath it. Nothing here is an action: everything
 * is a link to the page where the action belongs, because a dashboard that
 * lets you approve a change without reading it is the thing this product
 * exists to prevent.
 *
 * Every call is allowed to fail on its own. A tunnel endpoint that is down
 * should cost the tunnel tile, not the pending-approvals list beside it.
 */
export function Overview() {
  const session = useSession();
  const [snap, setSnap] = useState<Snapshot>({
    waiting: [], plugins: [], instances: [], tunnels: null, health: null, audit: [],
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
  const greeting = session?.display_name?.split(" ")[0] || null;

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
        {unhealthy.length > 0 && (
          <Notice tone="attention">
            <div className="space-y-0.5">
              {unhealthy.map((p) => (
                <p key={p.name}>
                  <Link to={`/plugins/${encodeURIComponent(p.name)}`} className="font-medium underline underline-offset-4">
                    {p.name}
                  </Link>{" "}
                  is {p.health}
                  {p.health_message ? ` — ${p.health_message}` : "."}
                </p>
              ))}
            </div>
          </Notice>
        )}

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
