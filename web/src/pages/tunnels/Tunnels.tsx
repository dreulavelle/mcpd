import { useCallback, useEffect, useMemo, useState } from "react";
import { ArrowRight, Check, ChevronRight, Copy, Plus, RotateCw, Search, Waypoints } from "lucide-react";
import {
  isOpenAIReason, OpenAIPermissionDialog, type OpenAIReason,
} from "@/components/openai-permission";
import {
  api, ApiError,
  type ChatGPTAccount, type OpenAITunnel, type ToolCall, type TunnelInfo, type TunnelStatus,
} from "@/lib/api";
import { relative, when } from "@/lib/format";
import { usePoll } from "@/lib/hooks";
import { Link, useQueryParam } from "@/lib/router";
import { useCan } from "@/lib/session";
import { cn } from "@/lib/utils";
import {
  Copyable, EmptyState, Loading, Notice, Out, PageHeader,
} from "@/components/chrome";
import { Chip, StatusDot, type Tone } from "@/components/status";
import { useNotify, type Notify } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
  Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { Sheet, SheetContent, SheetDescription, SheetTitle } from "@/components/ui/sheet";
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";
import { useConfirm } from "@/components/confirm";

const OPENAI_TUNNELS = "https://platform.openai.com/settings/organization/tunnels";
const CHATGPT_CONNECTORS = "https://chatgpt.com/#settings/Connectors";

/**
 * Tunnels made from this page and not yet attached in ChatGPT, by id.
 *
 * Kept in this browser, because it is a fact about what this person has
 * done and not finished. Nothing on the host can tell an attached connector
 * that has been idle from one nobody attached -- both are "connected, nothing
 * sent" -- so the page only says "waiting for ChatGPT" of a tunnel it made
 * itself, and stops saying it on the first request through.
 */
const AWAITING = "mcpd.tunnels.awaiting";

function readAwaiting(): string[] {
  try {
    const raw = localStorage.getItem(AWAITING);
    const list = raw ? JSON.parse(raw) : [];
    return Array.isArray(list) ? list.filter((v) => typeof v === "string") : [];
  } catch {
    return [];
  }
}

function writeAwaiting(ids: string[]) {
  try {
    if (ids.length === 0) localStorage.removeItem(AWAITING);
    else localStorage.setItem(AWAITING, JSON.stringify(ids));
  } catch {
    // Private mode: remembered for this page and no longer.
  }
}

interface Row extends OpenAITunnel {
  status?: TunnelStatus;
  /** Which ChatGPT account it connects with, undefined when it has none. */
  account?: string;
  /** What it is pointed at in the configuration: a plugin name, "" for
   *  everything, or undefined when it is not assigned at all. */
  assigned?: string;
}

/**
 * What a tunnel is doing, reduced to one word and a rank.
 *
 * The rank is what the list is sorted by, worst first: a page somebody
 * opens because something is wrong should put the wrong thing at the top.
 * Every other place a state is shown reads the same table, so the row, the
 * inspector and the filter chips cannot disagree about what a tunnel is.
 */
export interface Reading {
  kind: "gone" | "stopped" | "retrying" | "degraded" | "unassigned" | "waiting" | "attach" | "connecting" | "ready" | "off" | "unused";
  label: string;
  tone: Tone;
  rank: number;
  /** The filter chip it counts under. */
  bucket: "needs" | "waiting" | "ready" | "off";
  detail: string;
}

export function reading(row: Row, plugins: string[], accounts: ChatGPTAccount[], awaiting: Set<string> = new Set()): Reading {
  const s = row.status;
  const waitingOn = row.assigned && !s && !plugins.includes(row.assigned) ? row.assigned : "";
  if (!row.account && accounts.length > 1) {
    return { kind: "unassigned", label: "No account", tone: "attention", rank: 3, bucket: "needs",
      detail: "This host has more than one ChatGPT account, so a tunnel has to say which one it connects with. Until it does, it is not started." };
  }
  if (waitingOn) {
    return { kind: "waiting", label: "Waiting", tone: "attention", rank: 4, bucket: "waiting",
      detail: `${waitingOn} is not running, so this tunnel is not started. It connects on its own once that plugin has what it needs.` };
  }
  if (!s) {
    return { kind: "unused", label: "Not used", tone: "neutral", rank: 8, bucket: "off",
      detail: "Point it at a system to start it." };
  }
  if (s.upstream === "missing") {
    return { kind: "gone", label: "Gone from OpenAI", tone: "problem", rank: 0, bucket: "needs",
      detail: "OpenAI no longer has this tunnel, so no connector can reach it. Forget it here, and make a new one if its system still needs a connector of its own." };
  }
  switch (s.state) {
    case "failed":
      return s.next_retry_at
        ? { kind: "retrying", label: `Retrying · attempt ${s.attempts ?? 1}`, tone: "attention", rank: 1, bucket: "needs",
            detail: `Next try ${relative(s.next_retry_at)}. ${s.message ?? ""}` }
        : { kind: "stopped", label: "Stopped", tone: "problem", rank: 0, bucket: "needs",
            detail: `It will not restart on its own. ${s.message ?? ""}` };
    case "starting":
      return { kind: "connecting", label: "Connecting", tone: "info", rank: 5, bucket: "waiting",
        detail: "Waiting for the first poll to complete." };
    case "connected":
      if (s.degraded) {
        return { kind: "degraded", label: "Degraded", tone: "attention", rank: 2, bucket: "needs",
          detail: "Connected, but the client has been reporting errors with nothing served since. mcpd restarts it if this goes on." };
      }
      if ((s.requests ?? 0) === 0 && !s.last_request_at && awaiting.has(row.id)) {
        return { kind: "attach", label: "Waiting for ChatGPT", tone: "info", rank: 4, bucket: "waiting",
          detail: "mcpd is connected and nothing has come through yet. Attach the tunnel in ChatGPT; this clears itself on the first request." };
      }
      if ((s.requests ?? 0) === 0 && !s.last_request_at) {
        // A restart of mcpd starts the count again; an attached connector
        // that nobody has used since is not one that was never attached.
        return { kind: "ready", label: "Ready", tone: "good", rank: 6, bucket: "ready",
          detail: `Connected${s.connected_at ? ` ${relative(s.connected_at)}` : ""}; nothing has come through since mcpd started. A connector ChatGPT already has reconnects on its own.` };
      }
      return { kind: "ready", label: "Ready", tone: "good", rank: 6, bucket: "ready",
        detail: `${s.requests ?? 0} request${s.requests === 1 ? "" : "s"} since it connected${s.connected_at ? ` ${relative(s.connected_at)}` : ""}.` };
    case "stopped":
      return { kind: "off", label: "Off", tone: "neutral", rank: 7, bucket: "off", detail: "Switched off." };
    default:
      return { kind: "unused", label: "Not used", tone: "neutral", rank: 8, bucket: "off", detail: "" };
  }
}

const BUCKETS: { id: Reading["bucket"]; label: string; tone: Tone }[] = [
  { id: "needs", label: "Needs you", tone: "attention" },
  { id: "waiting", label: "Waiting", tone: "info" },
  { id: "ready", label: "Ready", tone: "good" },
  { id: "off", label: "Off", tone: "neutral" },
];

/** A tunnel carries one address, so it is one connector in ChatGPT. */
export function Tunnels() {
  const [info, setInfo] = useState<TunnelInfo | null>(null);
  const [error, setError] = useState("");
  // A refusal from OpenAI is several paragraphs of instruction, which a toast
  // cannot carry: newlines collapse and the numbered steps run together. It
  // gets a dialog; everything else stays a toast.
  const [refused, setRefused] = useState<{ reason: OpenAIReason; detail: string } | null>(null);
  const [selectedParam, setSelected] = useQueryParam("tunnel");
  // "metrics" opens the detail on its chart; clicking the bars sets it.
  const [view, setView] = useQueryParam("view");
  const [bucket, setBucket] = useQueryParam("show");
  const [query, setQuery] = useState("");
  const [making, setMaking] = useState(false);
  const [awaiting, setAwaiting] = useState<string[]>(readAwaiting);

  const notify = useNotify();
  const admin = useCan("admin");

  const load = useCallback(() => {
    api.tunnel()
      .then((t) => { setInfo(t); setError(""); })
      .catch(() => setError("Couldn't load tunnels."));
  }, []);
  usePoll(load, 8_000);

  // An older build sends null rather than [] for an empty list.
  const plugins = useMemo(() => info?.plugins ?? [], [info]);
  const accounts = useMemo(() => info?.accounts ?? [], [info]);
  const rows: Row[] = useMemo(() => {
    if (!info) return [];
    const running = new Map((info.tunnels ?? []).map((t) => [t.tunnel_id ?? "", t]));
    const accountOf = info.account_assignments ?? {};
    const assignments = info.assignments ?? {};
    const base: Row[] = info.can_manage
      ? (info.available ?? []).map((t) => ({ ...t, status: running.get(t.id) }))
      : (info.tunnels ?? []).map((t) => ({ id: t.tunnel_id ?? "", name: t.plugin || "Everything", status: t }));
    return base.map((r) => ({
      ...r,
      account: accountOf[r.id] ?? r.account_id,
      assigned: assignments[r.id],
    }));
  }, [info]);

  const awaitingSet = useMemo(() => new Set(awaiting), [awaiting]);
  const read = useCallback((r: Row) => reading(r, plugins, accounts, awaitingSet), [plugins, accounts, awaitingSet]);

  // The first request through a tunnel is ChatGPT saying it is attached.
  useEffect(() => {
    if (awaiting.length === 0) return;
    const attached = awaiting.filter((id) => {
      const s = rows.find((r) => r.id === id)?.status;
      return Boolean(s?.last_request_at) || (s?.requests ?? 0) > 0;
    });
    if (attached.length === 0) return;
    const rest = awaiting.filter((id) => !attached.includes(id));
    setAwaiting(rest);
    writeAwaiting(rest);
    for (const id of attached) {
      notify("good", `ChatGPT is connected to ${rows.find((r) => r.id === id)?.name ?? id}.`);
    }
  }, [awaiting, rows, notify]);

  // Worst first, then by name: a page opened because something is wrong
  // should put the wrong thing at the top.
  const ordered = useMemo(
    () => [...rows].sort((a, b) => read(a).rank - read(b).rank || a.name.localeCompare(b.name)),
    [rows, read],
  );
  const counts = useMemo(() => {
    const c: Record<Reading["bucket"], number> = { needs: 0, waiting: 0, ready: 0, off: 0 };
    for (const r of rows) c[read(r).bucket]++;
    return c;
  }, [rows, read]);
  const shown = useMemo(() => {
    const q = query.trim().toLowerCase();
    return ordered.filter((r) =>
      (!bucket || read(r).bucket === bucket) &&
      (!q || [r.name, r.id, r.account ?? "", accountName(accounts, r.account), r.status?.plugin ?? r.assigned ?? ""]
        .join(" ").toLowerCase().includes(q)));
  }, [ordered, bucket, query, read, accounts]);

  // The selection is in the address, so a link can open the page on one
  // tunnel with its detail already open. Nothing is selected by default:
  // the list is the page, and the detail is a sheet over it.
  const selected = rows.find((r) => r.id === selectedParam) ?? null;

  return (
    <>
      {refused && (
        <OpenAIPermissionDialog
          reason={refused.reason}
          detail={refused.detail}
          onClose={() => setRefused(null)}
        />
      )}
      <PageHeader
        title="Tunnels"
        lede="Every connector this host serves, and what each is doing right now. One tunnel is one connector in ChatGPT."
        actions={
          <div className="flex flex-wrap items-center gap-2">
            {rows.length > 6 && (
              <div className="relative">
                <Search className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" aria-hidden="true" />
                <Input
                  aria-label="Find a tunnel"
                  className="w-56 pl-8"
                  placeholder="Find a tunnel…"
                  value={query}
                  onChange={(e) => setQuery(e.target.value)}
                />
              </div>
            )}
            {info?.can_manage && admin && (
              <Button onClick={() => setMaking(true)}>
                <Plus className="size-4" aria-hidden="true" />
                Make a tunnel
              </Button>
            )}
          </div>
        }
      />

      {error && <Notice tone="problem">{error}</Notice>}
      {info?.problem && <Notice tone="problem">{info.problem}</Notice>}
      {info?.missing && (
        <Notice tone="info">
          Add {info.missing} in Settings to make tunnels from here, or make them
          on <Out href={OPENAI_TUNNELS}>OpenAI's site</Out>.
        </Notice>
      )}

      {making && info && (
        <MakeTunnel
          plugins={plugins} fallbackWorkspaces={info.workspaces ?? []} accounts={accounts}
          notify={notify} onRefused={setRefused}
          onClose={() => setMaking(false)}
          onMade={(id) => {
            setMaking(false);
            setBucket("");
            setSelected(id);
            if (id) {
              const next = [...awaiting.filter((v) => v !== id), id];
              setAwaiting(next);
              writeAwaiting(next);
            }
            load();
          }}
        />
      )}

      {!info ? <Loading rows={5} /> : rows.length === 0 ? (
        <EmptyState mark={<Waypoints />} title="No tunnels yet">
          {info.can_manage
            ? "Make one. One tunnel is one connector in ChatGPT, and a connector can cover everything on this host or a single system."
            : "A tunnel is made in the OpenAI dashboard and pasted in here, or made from here once an admin key is set under Settings › ChatGPT."}
        </EmptyState>
      ) : (
        <>
          <Card className="mt-4 overflow-hidden p-0">
            {/* Only when there is something to filter between. Eight tunnels
                all ready is one state, and a row of chips saying so twice
                was a header with nothing to say. */}
            {BUCKETS.filter((b) => counts[b.id] > 0).length > 1 && (
              <div className="flex flex-wrap items-center gap-2 border-b px-4 py-3">
                <button type="button" onClick={() => setBucket("")} aria-pressed={bucket === ""}>
                  <Chip tone={bucket === "" ? "info" : "neutral"}>All {rows.length}</Chip>
                </button>
                {BUCKETS.filter((b) => counts[b.id] > 0).map((b) => (
                  <button key={b.id} type="button" onClick={() => setBucket(bucket === b.id ? "" : b.id)} aria-pressed={bucket === b.id}>
                    <Chip tone={bucket === b.id ? b.tone : "neutral"} className={bucket === b.id ? "" : "hover:border-ring/50"}>
                      {b.label} {counts[b.id]}
                    </Chip>
                  </button>
                ))}
              </div>
            )}
            <div className="scroll-x">
              <Table aria-label="Tunnels">
                <TableHeader>
                  <TableRow>
                    <TableHead className="w-8" />
                    <TableHead>Tunnel</TableHead>
                    {accounts.length > 1 && <TableHead>Account</TableHead>}
                    <TableHead>Reaches</TableHead>
                    <TableHead>Requests, last 12 h</TableHead>
                    <TableHead>Activity</TableHead>
                    <TableHead className="w-px" />
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {shown.length === 0 ? (
                    <TableRow>
                      <TableCell colSpan={accounts.length > 1 ? 7 : 6} className="py-8 text-center text-muted-foreground">
                        No tunnel matches that.
                      </TableCell>
                    </TableRow>
                  ) : shown.map((row) => (
                    <TunnelRow
                      key={row.id} row={row} reading={read(row)} accounts={accounts}
                      selected={selected?.id === row.id}
                      onSelect={(v) => { setView(v ?? ""); setSelected(row.id); }}
                      onDone={load} notify={notify} onRefused={setRefused}
                    />
                  ))}
                </TableBody>
              </Table>
            </div>
          </Card>

          <Sheet open={selected !== null} onOpenChange={(open) => { if (!open) { setView(""); setSelected(""); } }}>
            {selected && (
              <SheetContent aria-describedby={undefined}>
                <Inspector
                  key={selected.id}
                  row={selected} reading={read(selected)} info={info}
                  plugins={plugins} accounts={accounts} metricsFirst={view === "metrics"}
                  onDone={load} notify={notify} onRefused={setRefused}
                />
              </SheetContent>
            )}
          </Sheet>
        </>
      )}
    </>
  );
}

function accountName(accounts: ChatGPTAccount[], id?: string): string {
  return accounts.find((a) => a.id === id)?.name ?? "";
}

/**
 * One row: what the tunnel is, and enough to know whether to open it. The
 * whole row opens the detail; the two buttons at its end are the actions
 * somebody reaches for without wanting to read anything first.
 */
function TunnelRow({ row, reading: r, accounts, selected, onSelect, onDone, notify, onRefused }: {
  row: Row;
  reading: Reading;
  accounts: ChatGPTAccount[];
  selected: boolean;
  onSelect: (view?: "metrics") => void;
  onDone: () => void;
  notify: Notify;
  onRefused: (r: { reason: OpenAIReason; detail: string }) => void;
}) {
  const admin = useCan("admin");
  const [busy, setBusy] = useState(false);
  const s = row.status;
  const reaches = s ? (s.plugin || "Everything") : row.assigned === undefined ? "—" : (row.assigned || "Everything");

  async function restart(e: React.MouseEvent) {
    e.stopPropagation();
    setBusy(true);
    try {
      await api.restartTunnel(row.id);
      notify("good", `Restarting ${row.name}. Give it a moment to reconnect.`);
    } catch (err) {
      showFailure(err, "Couldn't restart it.", notify, onRefused);
    } finally {
      setBusy(false);
      onDone();
    }
  }

  return (
    <TableRow
      onClick={() => onSelect()}
      aria-selected={selected}
      className={cn("cursor-pointer", selected && "bg-accent")}
    >
      <TableCell><StatusDot tone={r.tone} /></TableCell>
      <TableCell className="min-w-[14rem]">
        <span className="block truncate font-medium">{row.name}</span>
        <span className={cn("block text-xs", toneText(r.tone))}>{r.label}</span>
      </TableCell>
      {accounts.length > 1 && (
        <TableCell className="text-muted-foreground">{accountName(accounts, row.account) || "—"}</TableCell>
      )}
      <TableCell className="font-mono text-xs">{reaches}</TableCell>
      <TableCell>
        {/* The bars open the metrics view: a chart worth looking at is
            worth looking at larger, with the errors beside it. */}
        <button
          type="button"
          className="rounded-sm hover:bg-accent/60"
          title="Requests and errors over the last twelve hours"
          aria-label={`Metrics for ${row.name}`}
          onClick={(e) => { e.stopPropagation(); onSelect("metrics"); }}
        >
          <Bars values={s?.activity} errors={s?.errors} tone={r.tone} />
        </button>
      </TableCell>
      <TableCell className="text-xs whitespace-nowrap text-muted-foreground">
        {s?.last_request_at ? relative(s.last_request_at) : s?.trouble_at ? `error ${relative(s.trouble_at)}` : "—"}
      </TableCell>
      <TableCell className="whitespace-nowrap">
        <span className="flex items-center justify-end gap-1">
          {admin && s && s.state !== "disabled" && (
            <Button variant="ghost" size="sm" onClick={restart} disabled={busy} title="Restart">
              <RotateCw className={busy ? "size-3.5 animate-spin" : "size-3.5"} aria-hidden="true" />
              <span className="sr-only">Restart {row.name}</span>
            </Button>
          )}
          <Button variant="ghost" size="sm" onClick={(e) => { e.stopPropagation(); onSelect(); }} aria-label={`Details for ${row.name}`}>
            Details
            <ChevronRight className="size-3.5" aria-hidden="true" />
          </Button>
        </span>
      </TableCell>
    </TableRow>
  );
}

function toneText(tone: Tone): string {
  switch (tone) {
    case "good": return "text-good";
    case "attention": return "text-attention";
    case "problem": return "text-problem";
    case "info": return "text-info";
    default: return "text-muted-foreground";
  }
}

/**
 * Twelve hours of requests as bars. Height is relative to the busiest hour
 * of this tunnel, so a quiet connector still shows its shape; an hour with
 * nothing is a faint stub rather than a gap, so twelve of them read as
 * "nothing for twelve hours" and not as a chart that failed to draw.
 */
function Bars({ values, errors, tone }: { values?: number[]; errors?: number[]; tone: Tone }) {
  const series = values && values.length > 0 ? values : Array.from({ length: 12 }, () => 0);
  const bad = errors && errors.length === series.length ? errors : series.map(() => 0);
  const max = Math.max(1, ...series);
  const fill = tone === "good" ? "bg-good" : tone === "attention" ? "bg-attention" : tone === "problem" ? "bg-problem" : tone === "info" ? "bg-info" : "bg-faint";
  const total = series.reduce((a, b) => a + b, 0);
  const totalErrors = bad.reduce((a, b) => a + b, 0);
  return (
    <span
      className="flex h-6 items-end gap-0.5"
      role="img"
      aria-label={`${total} requests and ${totalErrors} errors in the last ${series.length} hours`}
    >
      {series.map((v, i) => (
        <span key={i} className="relative flex w-1.5 items-end" style={{ height: 24 }}>
          <span
            className={cn("w-full rounded-sm", fill, v === 0 && "opacity-30")}
            style={{ height: v === 0 ? 2 : Math.max(3, Math.round((v / max) * 24)) }}
          />
          {/* An hour with errors gets a mark under its bar, so a connector
              that went quiet at the hour its errors began reads as one. */}
          {bad[i]! > 0 && (
            <span className="absolute -bottom-1 left-0 h-0.5 w-full rounded-sm bg-problem" />
          )}
        </span>
      ))}
    </span>
  );
}

/** The last twelve hours, large enough to read: an hour per column. */
function Chart({ values, errors }: { values: number[]; errors: number[] }) {
  const max = Math.max(1, ...values, ...errors);
  const now = new Date();
  const hourOf = (i: number) => {
    const d = new Date(now.getTime() - (values.length - 1 - i) * 3_600_000);
    return d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
  };
  return (
    <div>
      <div className="grid gap-1" style={{ gridTemplateColumns: `repeat(${values.length}, minmax(0, 1fr))` }}>
        {values.map((v, i) => (
          <div key={i} className="flex h-28 flex-col justify-end gap-0.5" title={`${hourOf(i)}: ${v} request${v === 1 ? "" : "s"}, ${errors[i] ?? 0} error${errors[i] === 1 ? "" : "s"}`}>
            {(errors[i] ?? 0) > 0 && (
              <div className="rounded-sm bg-problem" style={{ height: Math.max(3, Math.round(((errors[i] ?? 0) / max) * 100)) }} />
            )}
            <div className={cn("rounded-sm bg-good", v === 0 && "opacity-30")} style={{ height: v === 0 ? 2 : Math.max(3, Math.round((v / max) * 100)) }} />
          </div>
        ))}
      </div>
      <div className="mt-1 flex justify-between text-[10px] text-muted-foreground tabular-nums">
        <span>{hourOf(0)}</span><span>{hourOf(values.length - 1)}</span>
      </div>
      <div className="mt-1 flex gap-3 text-[11px] text-muted-foreground">
        <span className="inline-flex items-center gap-1"><span className="inline-block size-2 rounded-sm bg-good" />requests</span>
        <span className="inline-flex items-center gap-1"><span className="inline-block size-2 rounded-sm bg-problem" />client errors</span>
      </div>
    </div>
  );
}

/**
 * What ChatGPT actually did through this tunnel, from the call ledger. The
 * ledger records the account's identity rather than the tunnel, so for a
 * tunnel serving one system the calls are filtered to that system and are
 * exactly its own; for one serving everything they are the account's.
 */
function RecentCalls({ principal, plugin }: { principal?: string; plugin: string }) {
  const [calls, setCalls] = useState<ToolCall[] | null>(null);
  const [failed, setFailed] = useState(false);
  const mayRead = useCan("admin");
  useEffect(() => {
    if (!mayRead || !principal) return;
    let live = true;
    api.calls({ principal, plugin: plugin || undefined, hours: 12, limit: 8 })
      .then((r) => { if (live) setCalls(r.calls ?? []); })
      .catch(() => { if (live) setFailed(true); });
    return () => { live = false; };
  }, [mayRead, principal, plugin]);
  if (!mayRead || !principal) return null;
  const href = `/activity?principal=${encodeURIComponent(principal)}${plugin ? `&plugin=${encodeURIComponent(plugin)}` : ""}&hours=12`;
  return (
    <div className="space-y-2 border-t pt-4">
      <h3 className="text-[11px] font-semibold tracking-wider text-muted-foreground uppercase">Recent calls</h3>
      {failed ? (
        <p className="text-xs text-muted-foreground">The call record could not be read.</p>
      ) : calls === null ? (
        <p className="text-xs text-muted-foreground">Reading…</p>
      ) : calls.length === 0 ? (
        <p className="text-xs text-muted-foreground">Nothing in the last twelve hours.</p>
      ) : (
        <ul className="space-y-1">
          {calls.map((c) => (
            <li key={c.id} className="flex items-center gap-2 text-xs">
              <StatusDot tone={c.outcome === "ok" ? "good" : c.outcome === "denied" ? "attention" : c.outcome === "error" ? "problem" : "neutral"} />
              <span className="min-w-0 flex-1 truncate font-mono">{plugin ? c.tool : `${c.plugin}_${c.tool}`}</span>
              <span className="text-muted-foreground">{c.outcome === "ok" ? "" : c.outcome.replace("_", " ")}</span>
              <span className="whitespace-nowrap text-muted-foreground">{relative(c.at)}</span>
            </li>
          ))}
        </ul>
      )}
      <Link to={href} className="inline-flex items-center gap-1 text-xs text-primary hover:underline">
        All its calls, on Activity <ArrowRight className="size-3" aria-hidden="true" />
      </Link>
    </div>
  );
}

/** Everything the page knows about a tunnel, as text, for a ticket. */
function CopyDiagnostics({ row, reading: r }: { row: Row; reading: Reading }) {
  const [copied, setCopied] = useState(false);
  async function copy() {
    const s = row.status;
    const lines = [
      `tunnel: ${row.id}`, `name: ${row.name}`, `state: ${r.label}`, `detail: ${r.detail}`,
      s?.message ? `message: ${s.message}` : "", s?.connected_at ? `connected_at: ${s.connected_at}` : "",
      `requests_since_connected: ${s?.requests ?? 0}`, s?.last_request_at ? `last_request_at: ${s.last_request_at}` : "",
      s?.activity ? `requests_by_hour: ${s.activity.join(",")}` : "", s?.errors ? `errors_by_hour: ${s.errors.join(",")}` : "",
      s?.attempts ? `restart_attempts: ${s.attempts}` : "", s?.next_retry_at ? `next_retry_at: ${s.next_retry_at}` : "",
      s?.upstream ? `upstream: ${s.upstream} (checked ${s.upstream_checked_at ?? "?"})` : "",
      s?.trouble ? `last_client_error: ${s.trouble_at ?? ""} ${s.trouble}` : "",
    ].filter(Boolean);
    try {
      await navigator.clipboard.writeText(lines.join("\n"));
      setCopied(true);
      setTimeout(() => setCopied(false), 1600);
    } catch {
      // Refused outside a secure context, which a plain-http LAN address is.
    }
  }
  return (
    <Button variant="ghost" size="sm" onClick={copy}>
      {copied ? <Check className="size-3.5 text-good" aria-hidden="true" /> : <Copy className="size-3.5" aria-hidden="true" />}
      {copied ? "Copied" : "Copy diagnostics"}
    </Button>
  );
}

// showFailure decides where a failure is shown.
//
// A refusal from OpenAI carries instructions -- which permission, granted
// where, by whom -- and a toast flattens them into one line with the numbered
// steps run together. Those get a dialog. Everything else is a toast, because
// everything else is one sentence.
export function showFailure(
  e: unknown,
  fallback: string,
  notify: Notify,
  onRefused: (r: { reason: OpenAIReason; detail: string }) => void,
) {
  if (e instanceof ApiError && isOpenAIReason(e.code)) {
    onRefused({ reason: e.code, detail: e.detail });
    return;
  }
  notify("problem", e instanceof ApiError ? e.detail : fallback);
}

/**
 * One tunnel's whole story, beside the list: what it is, what is wrong,
 * what to do about it, and where it is in being set up. The actions live
 * here and only here, so a row stays a row.
 */
function Inspector({ row, reading: r, info, plugins, accounts, metricsFirst, onDone, notify, onRefused }: {
  row: Row;
  reading: Reading;
  info: TunnelInfo;
  plugins: string[];
  accounts: ChatGPTAccount[];
  /** Opened from the bars: the chart is what was asked for, so it leads. */
  metricsFirst?: boolean;
  onDone: () => void;
  notify: Notify;
  onRefused: (r: { reason: OpenAIReason; detail: string }) => void;
}) {
  const confirm = useConfirm();
  const admin = useCan("admin");
  const manages = info.can_manage && admin;
  const [busy, setBusy] = useState<"restart" | "remove" | "assign" | null>(null);
  const s = row.status;
  const account = accounts.find((a) => a.id === row.account);
  const activity = s?.activity ?? [];
  const errors = s?.errors && s.errors.length === activity.length ? s.errors : activity.map(() => 0);
  const metrics = s && activity.length > 0 && (
    <div className="space-y-3 border-t pt-4">
      <h3 className="text-[11px] font-semibold tracking-wider text-muted-foreground uppercase">Last twelve hours</h3>
      <Chart values={activity} errors={errors} />
      <dl className="grid grid-cols-2 gap-x-4 gap-y-2 text-xs">
        <div><dt className="text-muted-foreground">Requests</dt><dd className="font-mono">{activity.reduce((a, b) => a + b, 0)}</dd></div>
        <div><dt className="text-muted-foreground">Client errors</dt><dd className="font-mono">{errors.reduce((a, b) => a + b, 0)}</dd></div>
        <div><dt className="text-muted-foreground">Last request</dt><dd>{s.last_request_at ? relative(s.last_request_at) : "none since mcpd started"}</dd></div>
        <div><dt className="text-muted-foreground">Connected</dt><dd>{s.connected_at ? relative(s.connected_at) : "—"}</dd></div>
        <div><dt className="text-muted-foreground">Restarts since it worked</dt><dd className="font-mono">{s.attempts ?? 0}</dd></div>
        <div><dt className="text-muted-foreground">At OpenAI</dt><dd>{s.upstream === "missing" ? "gone" : s.upstream === "present" ? `present, checked ${s.upstream_checked_at ? relative(s.upstream_checked_at) : ""}` : "not checked"}</dd></div>
      </dl>
    </div>
  );

  // The account travels with every assignment: pointing a tunnel at a system
  // and saying whose credential it uses are one decision, and applying them
  // separately leaves a moment where the tunnel has a system and no account,
  // which is exactly the state that refuses to start.
  async function assign(to: string, withAccount = row.account) {
    setBusy("assign");
    try {
      await api.assignTunnel(row.id, to === "*" ? "" : to, withAccount);
    } catch (e) {
      showFailure(e, "Couldn't change that.", notify, onRefused);
    } finally {
      setBusy(null);
      onDone();
    }
  }

  async function restart() {
    setBusy("restart");
    try {
      await api.restartTunnel(row.id);
      notify("good", `Restarting ${row.name}. Give it a moment to reconnect.`);
    } catch (e) {
      showFailure(e, "Couldn't restart it.", notify, onRefused);
    } finally {
      setBusy(null);
      onDone();
    }
  }

  async function remove() {
    const question = r.kind === "gone"
      ? { title: `Forget "${row.name}"?`, description: "OpenAI no longer has it, so there is nothing there to delete. This clears its assignment here, which is what keeps mcpd reporting it.", action: "Forget" }
      : `Delete "${row.name}"? Any connector using it stops working.`;
    if (!(await confirm(question))) return;
    setBusy("remove");
    try {
      // Deleted from the organisation it actually lives in. Two accounts are
      // two organisations, and deleting from the wrong one cannot be undone.
      await api.deleteTunnel(row.id, row.account ?? row.account_id);
      notify("good", r.kind === "gone" ? "Forgotten." : "Deleted.");
    } catch (e) {
      showFailure(e, "Couldn't delete it.", notify, onRefused);
    } finally {
      setBusy(null);
      onDone();
    }
  }

  const reachesValue = s ? (s.plugin || "*") : row.assigned === undefined ? "" : (row.assigned || "*");
  const attached = Boolean(s?.last_request_at) || (s?.requests ?? 0) > 0;

  return (
    <div className="flex flex-col gap-5">
      <div className="space-y-2 pr-8">
        <div className="flex flex-wrap items-center gap-2">
          <SheetTitle>{row.name}</SheetTitle>
          <Chip tone={r.tone}><StatusDot tone={r.tone} />{r.label}</Chip>
        </div>
        {/* Copyable: ChatGPT accepts a tunnel ID typed in, and an ID shown
            with the middle missing cannot be typed anywhere. */}
        <Copyable value={row.id} label="tunnel ID" />
        <SheetDescription className="text-xs">
          {account ? <>{account.name} · connects as <code className="font-mono">{account.principal}</code></> : "No account"}
          {account?.organization_id && <> · <code className="font-mono">{account.organization_id}</code></>}
        </SheetDescription>
      </div>

      {metricsFirst && metrics}

      {r.detail && (
        <Notice tone={r.tone === "good" ? "neutral" : r.tone}>
          {r.detail}
          {s?.trouble && r.kind !== "ready" && (
            <span className="mt-1 block font-mono text-[11px] break-all opacity-80">{s.trouble}</span>
          )}
        </Notice>
      )}

      {(admin || manages) && (
        <div className="flex flex-wrap gap-2">
          {admin && s && s.state !== "disabled" && (
            <Button variant="outline" size="sm" onClick={restart} disabled={busy !== null}
                    title="Stop it and start it again, against the plugins as they are now">
              <RotateCw className={busy === "restart" ? "size-3.5 animate-spin" : "size-3.5"} aria-hidden="true" />
              Restart
            </Button>
          )}
          {manages && (
            <Button variant="ghost" size="sm" onClick={remove} disabled={busy !== null}
                    className="text-destructive hover:text-destructive">
              {r.kind === "gone" ? "Forget" : "Remove"}
            </Button>
          )}
          <CopyDiagnostics row={row} reading={r} />
        </div>
      )}

      {manages && (
        <div className="grid gap-3 border-t pt-4 sm:grid-cols-2">
          <div className="space-y-1.5">
            <Label htmlFor="insp-reaches">Reaches</Label>
            <NativeSelect id="insp-reaches" value={reachesValue} disabled={busy !== null}
                          onChange={(e) => assign(e.target.value)}>
              <option value="">Not used</option>
              <option value="*">Everything</option>
              {optionsFor(plugins, row.assigned && !plugins.includes(row.assigned) ? row.assigned : "").map((p) => (
                <option key={p} value={p}>{p}</option>
              ))}
            </NativeSelect>
          </div>
          {accounts.length > 1 && (
            <div className="space-y-1.5">
              <Label htmlFor="insp-account">Account</Label>
              <NativeSelect id="insp-account" value={row.account ?? ""} disabled={busy !== null}
                            onChange={(e) => assign(reachesValue, e.target.value)}>
                <option value="">No account</option>
                {accounts.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
              </NativeSelect>
            </div>
          )}
        </div>
      )}

      <div className="space-y-3 border-t pt-4">
        <h3 className="text-[11px] font-semibold tracking-wider text-muted-foreground uppercase">Setup</h3>
        <ol className="space-y-2.5">
          <Step n={1} done={Boolean(row.account) || accounts.length <= 1} current={!row.account && accounts.length > 1}>
            Account{account ? `: ${account.name}` : ""}
          </Step>
          <Step n={2} done>
            Tunnel made{row.status?.connected_at ? "" : ""}
          </Step>
          <Step n={3} done={attached || (r.kind !== "attach" && s?.state === "connected")} current={r.kind === "attach"}>
            Attached in ChatGPT
            {!attached && r.kind === "attach" && (
              <span className="mt-1 block text-xs font-normal text-muted-foreground">
                Open <Out href={CHATGPT_CONNECTORS}>Settings › Connectors</Out> in ChatGPT,
                choose Create, pick <em>Tunnel</em>, and select this tunnel or paste
                its id from above. This step ticks itself on the first request.
              </span>
            )}
            {attached && s?.last_request_at && (
              <span className="mt-0.5 block text-xs font-normal text-muted-foreground">
                Last request {when(s.last_request_at)}
              </span>
            )}
            {!attached && r.kind !== "attach" && s?.state === "connected" && (
              <span className="mt-0.5 block text-xs font-normal text-muted-foreground">
                Nothing has come through since mcpd started, so this cannot be
                confirmed from here. A connector ChatGPT already has needs nothing.
              </span>
            )}
          </Step>
          <Step n={4} done={r.kind === "ready"} current={r.kind !== "ready" && s?.state === "connected"}>
            Serving
          </Step>
        </ol>
      </div>

      {!metricsFirst && metrics}

      <RecentCalls principal={account?.principal} plugin={s?.plugin ?? row.assigned ?? ""} />

      {s?.trouble && (
        <div className="space-y-1.5 border-t pt-4">
          <h3 className="text-[11px] font-semibold tracking-wider text-muted-foreground uppercase">Last from the client</h3>
          <p className="rounded-md border bg-muted/50 p-2 font-mono text-[11px] break-all text-muted-foreground">
            {s.trouble_at ? `${when(s.trouble_at)} ` : ""}{s.trouble}
          </p>
          {admin && (
            <Link to="/logs" className="inline-flex items-center gap-1 text-xs text-primary hover:underline">
              Every line from this host, on Logs <ArrowRight className="size-3" aria-hidden="true" />
            </Link>
          )}
        </div>
      )}
    </div>
  );
}

function Step({ n, done, current, children }: {
  n: number;
  done: boolean;
  current?: boolean;
  children: React.ReactNode;
}) {
  return (
    <li className="flex items-start gap-2.5">
      <span
        aria-hidden="true"
        className={cn(
          "mt-0.5 inline-flex size-5 shrink-0 items-center justify-center rounded-full border text-[11px]",
          done ? "border-good bg-good text-primary-foreground" : current ? "border-info text-info" : "border-border text-muted-foreground",
        )}
      >
        {done ? "✓" : n}
      </span>
      <span className={cn("min-w-0 text-sm", done || current ? "text-foreground" : "text-muted-foreground", current && "font-medium")}>
        {children}
      </span>
    </li>
  );
}

function MakeTunnel({ plugins, fallbackWorkspaces, accounts, notify, onRefused, onClose, onMade }: {
  plugins: string[];
  /** Used only by an account that reports none of its own. */
  fallbackWorkspaces: string[];
  accounts: ChatGPTAccount[];
  notify: Notify;
  onRefused: (r: { reason: OpenAIReason; detail: string }) => void;
  onClose: () => void;
  /** Told the new tunnel's id, so the page can select it and walk through the rest. */
  onMade: (id: string) => void;
}) {
  const [name, setName] = useState("");
  const [plugin, setPlugin] = useState("");
  // Only accounts that can actually make a tunnel: one without an admin key
  // and organisation cannot, and offering it would produce a refusal at the
  // point somebody presses Make rather than at the point they chose.
  const canMake = accounts.filter((a) => a.can_manage);
  const [account, setAccount] = useState(canMake[0]?.id ?? "");
  // A tunnel scoped only to the Platform organisation silently does not appear
  // in an Enterprise or Edu workspace, so the default is one that has worked.
  const [workspace, setWorkspace] = useState("");
  const [busy, setBusy] = useState(false);

  // The selected account's own workspaces. An account is an organisation, so
  // offering the union across every account would let a tunnel be created in
  // one the selected account cannot reach. The host-wide list is the fallback
  // only for an account with no tunnels yet.
  const chosen = canMake.find((a) => a.id === account);
  const workspaces = chosen?.workspaces?.length ? chosen.workspaces : fallbackWorkspaces;

  useEffect(() => {
    setWorkspace(workspaces[0] ?? "");
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [account]);

  async function add() {
    setBusy(true);
    try {
      const made = await api.createTunnel(name.trim(), plugin, workspace.trim(), account);
      // A tunnel is not active for the first half minute; OpenAI's own CLI
      // says the same after creating one.
      notify("good", "Made. Give it about 30 seconds to become active in ChatGPT.");
      onMade(made?.id ?? "");
    } catch (e) {
      showFailure(e, "Couldn't make it.", notify, onRefused);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open onOpenChange={(o) => { if (!o) onClose(); }}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Make a tunnel</DialogTitle>
          <DialogDescription>
            It is made in the account's organisation, pointed at a system here,
            and connected straight away. Attaching it in ChatGPT is the one step
            left afterwards, and the page walks you through it.
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-4">
          {canMake.length > 1 && (
            <div className="space-y-1.5">
              <Label htmlFor="tacct">Account</Label>
              <NativeSelect id="tacct" value={account} onChange={(e) => setAccount(e.target.value)}>
                {canMake.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
              </NativeSelect>
            </div>
          )}
          <div className="space-y-1.5">
            <Label htmlFor="tplug">Reaches</Label>
            <NativeSelect id="tplug" value={plugin} onChange={(e) => setPlugin(e.target.value)}>
              <option value="">Everything</option>
              {plugins.map((p) => <option key={p} value={p}>{p}</option>)}
            </NativeSelect>
          </div>
          {workspaces.length > 0 ? (
            <div className="space-y-1.5">
              <Label htmlFor="tws">Workspace</Label>
              <NativeSelect id="tws" value={workspace} onChange={(e) => setWorkspace(e.target.value)}>
                {workspaces.map((w) => <option key={w} value={w}>{w}</option>)}
                <option value="">None — organisation only</option>
              </NativeSelect>
            </div>
          ) : (
            <div className="space-y-1.5">
              <Label htmlFor="tws">Workspace (optional)</Label>
              <Input id="tws" type="text" value={workspace} placeholder="ws_..."
                     onChange={(e) => setWorkspace(e.target.value)} />
            </div>
          )}
          <div className="space-y-1.5">
            <Label htmlFor="tname">Name (optional)</Label>
            <Input id="tname" type="text" value={name}
                   placeholder={plugin ? `mcpd: ${plugin}` : "mcpd"}
                   onChange={(e) => setName(e.target.value)} />
          </div>
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={onClose}>Cancel</Button>
            <Button disabled={busy} onClick={add}>{busy ? "Making…" : "Make"}</Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}

/** The plugins offered, plus one a tunnel is already waiting on so its own
 *  assignment is not missing from the list that shows it. */
function optionsFor(plugins: string[], waitingOn: string): string[] {
  if (!waitingOn || plugins.includes(waitingOn)) return plugins;
  return [...plugins, waitingOn].sort();
}
