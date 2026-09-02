import { useCallback, useEffect, useMemo, useState } from "react";
import { ArrowRight, Plus, RotateCw, Search, Waypoints } from "lucide-react";
import {
  isOpenAIReason, OpenAIPermissionDialog, type OpenAIReason,
} from "@/components/openai-permission";
import {
  api, ApiError,
  type ChatGPTAccount, type OpenAITunnel, type TunnelInfo, type TunnelStatus,
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
import { useConfirm } from "@/components/confirm";

const OPENAI_TUNNELS = "https://platform.openai.com/settings/organization/tunnels";
const CHATGPT_CONNECTORS = "https://chatgpt.com/#settings/Connectors";

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

export function reading(row: Row, plugins: string[], accounts: ChatGPTAccount[]): Reading {
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
      detail: "OpenAI no longer has this tunnel, so no connector can reach it and the client here is polling for something that does not exist. Remove it and make a new one." };
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
      if ((s.requests ?? 0) === 0 && !s.last_request_at) {
        return { kind: "attach", label: "Waiting for ChatGPT", tone: "info", rank: 4, bucket: "waiting",
          detail: "mcpd is connected and nothing has come through yet. Attach the tunnel in ChatGPT, or wait: this clears itself on the first request." };
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
  const [bucket, setBucket] = useQueryParam("show");
  const [query, setQuery] = useState("");
  const [making, setMaking] = useState(false);

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

  const read = useCallback((r: Row) => reading(r, plugins, accounts), [plugins, accounts]);

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
  // tunnel. Absent, the worst one is selected: it is what somebody came for.
  const selected = shown.find((r) => r.id === selectedParam) ?? shown[0] ?? null;

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
          onMade={(id) => { setMaking(false); setBucket(""); setSelected(id); load(); }}
        />
      )}

      {!info ? <Loading rows={5} /> : rows.length === 0 ? (
        <EmptyState mark={<Waypoints />} title="No tunnels yet">
          {info.can_manage
            ? "Make one. One tunnel is one connector in ChatGPT, and a connector can cover everything on this host or a single system."
            : "A tunnel is made in the OpenAI dashboard and pasted in here, or made from here once an admin key is set under Settings › ChatGPT."}
        </EmptyState>
      ) : (
        <div className="mt-4 grid items-start gap-5 lg:grid-cols-[minmax(0,1fr)_22.5rem]">
          <Card className="overflow-hidden p-0">
            <div className="flex flex-wrap items-center gap-2 px-4 py-3">
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
              <span className="flex-1" />
              <span className="text-xs text-muted-foreground">worst first · last 12 hours</span>
            </div>
            <div
              role="list"
              aria-label="Tunnels"
              className="grid grid-cols-[1.25rem_minmax(0,1.5fr)_minmax(0,0.9fr)_minmax(0,1fr)_minmax(0,1.1fr)_auto] items-center gap-x-3 border-t px-4 py-1.5 text-[11px] font-semibold tracking-wider text-muted-foreground uppercase"
            >
              <span /><span>Tunnel</span><span>Account</span><span>Reaches</span><span>Requests</span><span className="text-right">Activity</span>
            </div>
            {shown.length === 0 ? (
              <p className="border-t px-4 py-8 text-center text-sm text-muted-foreground">
                No tunnel matches that.
              </p>
            ) : shown.map((row) => (
              <TunnelRow
                key={row.id} row={row} reading={read(row)} accounts={accounts}
                selected={selected?.id === row.id}
                onSelect={() => setSelected(row.id)}
              />
            ))}
          </Card>

          {selected && (
            <Inspector
              key={selected.id}
              row={selected} reading={read(selected)} info={info}
              plugins={plugins} accounts={accounts}
              onDone={load} notify={notify} onRefused={setRefused}
            />
          )}
        </div>
      )}
    </>
  );
}

function accountName(accounts: ChatGPTAccount[], id?: string): string {
  return accounts.find((a) => a.id === id)?.name ?? "";
}

function TunnelRow({ row, reading: r, accounts, selected, onSelect }: {
  row: Row;
  reading: Reading;
  accounts: ChatGPTAccount[];
  selected: boolean;
  onSelect: () => void;
}) {
  const s = row.status;
  const reaches = s ? (s.plugin || "Everything") : row.assigned === undefined ? "—" : (row.assigned || "Everything");
  return (
    <button
      type="button"
      role="listitem"
      aria-current={selected ? "true" : undefined}
      onClick={onSelect}
      className={cn(
        "grid w-full grid-cols-[1.25rem_minmax(0,1.5fr)_minmax(0,0.9fr)_minmax(0,1fr)_minmax(0,1.1fr)_auto] items-center gap-x-3 border-t px-4 py-3 text-left transition-colors",
        selected ? "bg-accent" : "hover:bg-accent/50",
      )}
    >
      <StatusDot tone={r.tone} />
      <span className="min-w-0">
        <span className="block truncate font-medium">{row.name}</span>
        <span className={cn("block text-xs", toneText(r.tone))}>{r.label}</span>
      </span>
      <span className="truncate text-sm text-muted-foreground">
        {accountName(accounts, row.account) || (accounts.length > 1 ? "—" : "")}
      </span>
      <span className="truncate font-mono text-xs">{reaches}</span>
      <Bars values={s?.activity} tone={r.tone} />
      <span className="text-right text-xs whitespace-nowrap text-muted-foreground">
        {s?.last_request_at ? relative(s.last_request_at) : s?.trouble_at ? `error ${relative(s.trouble_at)}` : "—"}
      </span>
    </button>
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
function Bars({ values, tone }: { values?: number[]; tone: Tone }) {
  const series = values && values.length > 0 ? values : Array.from({ length: 12 }, () => 0);
  const max = Math.max(1, ...series);
  const fill = tone === "good" ? "bg-good" : tone === "attention" ? "bg-attention" : tone === "problem" ? "bg-problem" : tone === "info" ? "bg-info" : "bg-faint";
  const total = series.reduce((a, b) => a + b, 0);
  return (
    <span
      className="flex h-6 items-end gap-0.5"
      role="img"
      aria-label={`${total} requests in the last ${series.length} hours`}
      title={`${total} in the last ${series.length} hours`}
    >
      {series.map((v, i) => (
        <span
          key={i}
          className={cn("w-1.5 rounded-sm", fill, v === 0 && "opacity-30")}
          style={{ height: v === 0 ? 2 : Math.max(3, Math.round((v / max) * 24)) }}
        />
      ))}
    </span>
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
function Inspector({ row, reading: r, info, plugins, accounts, onDone, notify, onRefused }: {
  row: Row;
  reading: Reading;
  info: TunnelInfo;
  plugins: string[];
  accounts: ChatGPTAccount[];
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
    if (!(await confirm(`Delete "${row.name}"? Any connector using it stops working.`))) return;
    setBusy("remove");
    try {
      // Deleted from the organisation it actually lives in. Two accounts are
      // two organisations, and deleting from the wrong one cannot be undone.
      await api.deleteTunnel(row.id, row.account ?? row.account_id);
      notify("good", "Deleted.");
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
    <Card className="gap-5 px-5 py-5 lg:sticky lg:top-6">
      <div className="space-y-2">
        <div className="flex flex-wrap items-center gap-2">
          <h2 className="text-base font-semibold">{row.name}</h2>
          <Chip tone={r.tone}><StatusDot tone={r.tone} />{r.label}</Chip>
        </div>
        {/* Copyable: ChatGPT accepts a tunnel ID typed in, and an ID shown
            with the middle missing cannot be typed anywhere. */}
        <Copyable value={row.id} label="tunnel ID" />
        <p className="text-xs text-muted-foreground">
          {account ? <>{account.name} · connects as <code className="font-mono">{account.principal}</code></> : "No account"}
          {account?.organization_id && <> · <code className="font-mono">{account.organization_id}</code></>}
        </p>
      </div>

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
              Remove
            </Button>
          )}
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
          <Step n={3} done={attached} current={!attached && r.kind === "attach"}>
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
          </Step>
          <Step n={4} done={r.kind === "ready"} current={attached && r.kind !== "ready"}>
            Serving
          </Step>
        </ol>
      </div>

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
    </Card>
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
