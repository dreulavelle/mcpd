import { useCallback, useEffect, useMemo, useState } from "react";
import { RotateCw, Waypoints } from "lucide-react";
import {
  isOpenAIReason, OpenAIPermissionDialog, type OpenAIReason,
} from "@/components/openai-permission";
import {
  api, ApiError,
  type ChatGPTAccount, type OpenAITunnel, type TunnelInfo, type TunnelStatus,
} from "@/lib/api";
import { relative } from "@/lib/format";
import { usePoll } from "@/lib/hooks";
import { Link } from "@/lib/router";
import { useCan } from "@/lib/session";
import {
  Copyable, EmptyState, Loading, Notice, Out, PageHeader, Section,
} from "@/components/chrome";
import { Chip, StatusDot, type Tone } from "@/components/status";
import { useNotify, type Notify } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
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
 * done and not finished: mcpd made the tunnel, ChatGPT has no API for the
 * last step, and until the first request comes through nothing on the host
 * can tell an attached connector from one somebody forgot about. The first
 * request is what clears it.
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
    // Private mode: the panel lasts for this page and no longer.
  }
}

/** A tunnel carries one address, so it is one connector in ChatGPT. */
export function Tunnels() {
  const [info, setInfo] = useState<TunnelInfo | null>(null);
  const [error, setError] = useState("");
  // A refusal from OpenAI is several paragraphs of instruction, which a toast
  // cannot carry: newlines collapse and the numbered steps run together. It
  // gets a dialog; everything else stays a toast.
  const [refused, setRefused] = useState<{ reason: OpenAIReason; detail: string } | null>(null);
  const [awaiting, setAwaiting] = useState<string[]>(readAwaiting);

  const notify = useNotify();
  const admin = useCan("admin");

  const load = useCallback(() => {
    api.tunnel()
      .then((t) => { setInfo(t); setError(""); })
      .catch(() => setError("Couldn't load tunnels."));
  }, []);
  usePoll(load, 8_000);

  const running = useMemo(
    () => new Map((info?.tunnels ?? []).map((t) => [t.tunnel_id ?? "", t])),
    [info],
  );
  // An older build sends null rather than [] for an empty list.
  const plugins = info?.plugins ?? [];
  const assignments = info?.assignments ?? {};
  const accountOf = info?.account_assignments ?? {};
  const accounts = info?.accounts ?? [];
  const rows: Row[] = useMemo(() => !info ? [] : info.can_manage
    ? (info.available ?? []).map((t) => ({ ...t, status: running.get(t.id) }))
    : (info.tunnels ?? []).map((t) => ({
        id: t.tunnel_id ?? "", name: t.plugin || "Everything", status: t,
        account_id: accountOf[t.tunnel_id ?? ""],
      })), [info, running, accountOf]);

  // The first request through a tunnel is ChatGPT saying it is attached.
  useEffect(() => {
    if (awaiting.length === 0) return;
    const attached = awaiting.filter((id) => (running.get(id)?.requests ?? 0) > 0);
    if (attached.length === 0) return;
    const rest = awaiting.filter((id) => !attached.includes(id));
    setAwaiting(rest);
    writeAwaiting(rest);
    for (const id of attached) {
      const row = rows.find((r) => r.id === id);
      notify("good", `ChatGPT is connected to ${row?.name ?? id}.`);
    }
  }, [awaiting, running, rows, notify]);

  function forget(id: string) {
    const rest = awaiting.filter((v) => v !== id);
    setAwaiting(rest);
    writeAwaiting(rest);
  }

  // One section per account, because an account is an organisation and a
  // tunnel lives in exactly one. Two tunnels serving the same plugin on
  // different accounts used to sit in one table telling them apart by a
  // column; here the heading tells them apart.
  const byAccount = useMemo(() => {
    const groups = accounts.map((a) => ({
      account: a as ChatGPTAccount | null,
      rows: rows.filter((r) => (accountOf[r.id] ?? r.account_id) === a.id),
    }));
    const orphans = rows.filter((r) => {
      const id = accountOf[r.id] ?? r.account_id;
      return !id || !accounts.some((a) => a.id === id);
    });
    if (orphans.length > 0) groups.push({ account: null, rows: orphans });
    return groups.filter((g) => g.rows.length > 0 || g.account !== null);
  }, [accounts, rows, accountOf]);

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
        lede={
          <>
            One tunnel is one connector in ChatGPT. mcpd makes the tunnel and
            keeps it connected; adding it under{" "}
            <Out href={CHATGPT_CONNECTORS}>Connectors</Out> in ChatGPT is the
            one step it cannot do for you, because there is no API for it.
          </>
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

      <div className="mt-4 space-y-8">
        {awaiting.length > 0 && info && (
          <FinishInChatGPT
            ids={awaiting}
            rows={rows}
            onForget={forget}
          />
        )}

        {info?.can_manage && admin && (
          <Section title="Make a tunnel" description="It is made in the account's organisation, pointed at a system here, and connected straight away.">
            <Add plugins={plugins} fallbackWorkspaces={info.workspaces ?? []}
                 accounts={accounts} onDone={load} notify={notify}
                 onRefused={setRefused}
                 onMade={(id) => {
                   const next = [...awaiting.filter((v) => v !== id), id];
                   setAwaiting(next);
                   writeAwaiting(next);
                 }} />
          </Section>
        )}

        {!info ? <Loading rows={4} /> : rows.length === 0 ? (
          <EmptyState mark={<Waypoints />} title="No tunnels yet">
            {info?.can_manage
              ? "Make one above. One tunnel is one connector in ChatGPT, and a connector can cover everything on this host or a single system."
              : "A tunnel is made in the OpenAI dashboard and pasted in here, or made from here once an admin key is set under Settings › ChatGPT."}
          </EmptyState>
        ) : byAccount.map(({ account, rows: own }) => (
          <Section
            key={account?.id ?? "none"}
            title={account ? account.name : "Not assigned to an account"}
            description={account ? <AccountLine account={account} /> : (
              "These tunnels have no ChatGPT account, so nothing connects them. Give each one an account below."
            )}
          >
            {own.length === 0 ? (
              <p className="text-sm text-muted-foreground">
                No tunnels in this account yet.
              </p>
            ) : (
              <Card className="overflow-hidden p-0">
                <div className="scroll-x">
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>Tunnel</TableHead>
                        <TableHead>Reaches</TableHead>
                        <TableHead>State</TableHead>
                        <TableHead className="w-px" />
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {own.map((row) => (
                        <TunnelRow key={row.id} row={row} info={info} plugins={plugins}
                                   assigned={assignments[row.id]}
                                   account={accountOf[row.id] ?? row.account_id}
                                   accounts={accounts}
                                   onDone={load} notify={notify} onRefused={setRefused} />
                      ))}
                    </TableBody>
                  </Table>
                </div>
              </Card>
            )}
          </Section>
        ))}
      </div>
    </>
  );
}

/** One line under an account's heading: whose identity, which organisation. */
function AccountLine({ account }: { account: ChatGPTAccount }) {
  return (
    <span className="flex flex-wrap items-center gap-x-3 gap-y-1">
      <span>Connects as <code className="font-mono text-xs">{account.principal}</code></span>
      {account.organization_id && (
        <span>in <code className="font-mono text-xs">{account.organization_id}</code></span>
      )}
      {!account.enabled && <Chip tone="attention">switched off</Chip>}
      {account.problem
        ? <Chip tone="problem">{account.problem}</Chip>
        : !account.can_manage && account.missing
          ? <span>Needs {account.missing} to make tunnels.</span>
          : null}
    </span>
  );
}

/**
 * The handoff. mcpd has done its half; this is the other half, spelled out,
 * with the one value ChatGPT asks for ready to copy. It goes away on its own
 * when the first request comes through, which is the moment the connector
 * is real.
 */
function FinishInChatGPT({ ids, rows, onForget }: {
  ids: string[];
  rows: Row[];
  onForget: (id: string) => void;
}) {
  return (
    <Notice tone="info">
      <div className="space-y-3">
        <p>
          <strong>Finish in ChatGPT.</strong> Open{" "}
          <Out href={CHATGPT_CONNECTORS}>Settings › Connectors</Out>, choose
          Create, pick <em>Tunnel</em>, and select the tunnel below or paste its
          id. This notice clears itself the moment ChatGPT sends its first
          request through it.
        </p>
        <ul className="space-y-2">
          {ids.map((id) => {
            const row = rows.find((r) => r.id === id);
            const state = row?.status?.state;
            return (
              <li key={id} className="flex flex-wrap items-center gap-2">
                <span className="font-medium">{row?.name ?? id}</span>
                <Copyable value={id} label="tunnel ID" className="max-w-[24rem]" />
                <span className="text-xs text-muted-foreground">
                  {state === "connected"
                    ? "mcpd is connected and waiting for ChatGPT."
                    : state === "starting"
                      ? "mcpd is still connecting."
                      : "mcpd has not connected this yet."}
                </span>
                <Button variant="ghost" size="xs" onClick={() => onForget(id)}>
                  Dismiss
                </Button>
              </li>
            );
          })}
        </ul>
      </div>
    </Notice>
  );
}

interface Row extends OpenAITunnel {
  status?: TunnelStatus;
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

/** No account chosen, and more than one to choose from. The tunnel will not
 *  start, and saying so here is the difference between a fixable mistake and
 *  a connector that silently does nothing. */
function needsAccount(account: string | undefined, accounts: ChatGPTAccount[]): boolean {
  return !account && accounts.length > 1;
}

function TunnelRow({ row, info, plugins, assigned, account, accounts, onDone, notify, onRefused }: {
  row: Row;
  info: TunnelInfo;
  plugins: string[];
  /** What this tunnel is pointed at in the configuration: a plugin name, ""
   *  for everything, or undefined when it is not assigned at all. */
  assigned?: string;
  /** Which ChatGPT account it connects with, undefined when it has none. */
  account?: string;
  accounts: ChatGPTAccount[];
  onDone: () => void;
  notify: Notify;
  onRefused: (r: { reason: OpenAIReason; detail: string }) => void;
}) {
  const confirm = useConfirm();
  const admin = useCan("admin");
  const [busy, setBusy] = useState<"restart" | "remove" | null>(null);
  // Assigned to a plugin that is not mounted, so the tunnel is not started.
  // Until this said so, the assignment looked like it had not taken.
  const waitingOn = assigned && !row.status && !plugins.includes(assigned)
    ? assigned : "";
  const unassigned = needsAccount(account, accounts);
  const manages = info.can_manage && admin;

  // The account travels with every assignment: pointing a tunnel at a system
  // and saying whose credential it uses are one decision, and applying them
  // separately leaves a moment where the tunnel has a system and no account,
  // which is exactly the state that refuses to start.
  async function assign(to: string, withAccount = account) {
    try {
      await api.assignTunnel(row.id, to === "*" ? "" : to, withAccount);
    } catch (e) {
      showFailure(e, "Couldn't change that.", notify, onRefused);
    } finally {
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
      await api.deleteTunnel(row.id, account ?? row.account_id);
      notify("good", "Deleted.");
    } catch (e) {
      showFailure(e, "Couldn't delete it.", notify, onRefused);
    } finally {
      setBusy(null);
      onDone();
    }
  }

  return (
    <TableRow>
      <TableCell className="align-top">
        <div className="font-medium">{row.name}</div>
        {/* Copyable: ChatGPT accepts a tunnel ID typed in, and
            an ID shown with the middle missing cannot be typed anywhere. */}
        <Copyable value={row.id} label="tunnel ID" className="mt-1 max-w-[24rem]" />
        {manages && accounts.length > 1 && (
          <div className="mt-2 w-44">
            <NativeSelect
              aria-label="Account" value={account ?? ""}
              onChange={(e) => assign(selected(row, assigned), e.target.value)}
            >
              <option value="">No account</option>
              {accounts.map((a) => (
                <option key={a.id} value={a.id}>{a.name}</option>
              ))}
            </NativeSelect>
          </div>
        )}
      </TableCell>
      <TableCell className="align-top">
        {manages ? (
          // The stored assignment, not the running one: a tunnel waiting on a
          // plugin is still pointed at it.
          <div className="w-44">
            <NativeSelect aria-label="Reaches" value={selected(row, assigned)}
                          onChange={(e) => assign(e.target.value)}>
              <option value="">Not used</option>
              <option value="*">Everything</option>
              {optionsFor(plugins, waitingOn).map((p) => (
                <option key={p} value={p}>{p}</option>
              ))}
            </NativeSelect>
          </div>
        ) : (
          <code className="font-mono text-xs">{row.status?.plugin || "Everything"}</code>
        )}
      </TableCell>
      <TableCell className="align-top">
        <Liveness status={row.status} unassigned={unassigned} waitingOn={waitingOn} />
      </TableCell>
      <TableCell className="align-top whitespace-nowrap">
        {admin && row.status && row.status.state !== "disabled" && (
          <Button
            variant="ghost" size="sm" onClick={restart} disabled={busy !== null}
            title="Stop it and start it again, against the plugins as they are now"
          >
            <RotateCw className={busy === "restart" ? "size-3.5 animate-spin" : "size-3.5"} aria-hidden="true" />
            Restart
          </Button>
        )}
        {manages && (
          <Button
            variant="ghost" size="sm" onClick={remove} disabled={busy !== null}
            className="text-destructive hover:text-destructive"
          >
            Remove
          </Button>
        )}
      </TableCell>
    </TableRow>
  );
}

/**
 * What the tunnel is actually doing, and not only what it last said.
 *
 * "Connected" is decided once, on the first poll, so on its own it is a
 * claim about the past. Beside it goes what has come through since, what
 * the client has been complaining about, whether mcpd is retrying, and
 * whether OpenAI still has the tunnel at all -- each of which is a different
 * thing to do something about.
 */
export function Liveness({ status, unassigned, waitingOn }: {
  status?: TunnelStatus;
  unassigned: boolean;
  waitingOn: string;
}) {
  if (unassigned) {
    return (
      <Line tone="attention" label="No account">
        This host has more than one ChatGPT account, so a tunnel has to say
        which one it connects with. Until it does, it is not started.
      </Line>
    );
  }
  if (waitingOn) {
    return (
      <Line tone="attention" label="Waiting">
        {waitingOn} is not running, so this tunnel is not started. It connects
        on its own once that plugin has what it needs.
      </Line>
    );
  }
  if (!status) return <Line tone="neutral" label="Not used" />;

  if (status.upstream === "missing") {
    return (
      <Line tone="problem" label="Gone from OpenAI">
        OpenAI no longer has this tunnel, so no connector can reach it and the
        client here is polling for something that does not exist. Remove it
        and make a new one.
      </Line>
    );
  }
  switch (status.state) {
    case "failed":
      return status.next_retry_at ? (
        <Line tone="attention" label={`Retrying (attempt ${status.attempts ?? 1})`}>
          Next try {relative(status.next_retry_at)}. {status.message}
        </Line>
      ) : (
        <Line tone="problem" label="Stopped">
          It will not restart on its own. {status.message}
        </Line>
      );
    case "starting":
      return <Line tone="info" label="Connecting">Waiting for the first poll to complete.</Line>;
    case "connected":
      if (status.degraded) {
        return (
          <Line tone="attention" label="Degraded">
            Connected, but the client has been reporting errors with nothing
            served since. mcpd restarts it if this goes on.
            {status.trouble && <span className="mt-1 block font-mono text-[11px] break-all">{status.trouble}</span>}
          </Line>
        );
      }
      return (
        <Line tone="good" label="Ready">
          {(status.requests ?? 0) > 0
            ? `${status.requests} request${status.requests === 1 ? "" : "s"} since it connected, the last ${status.last_request_at ? relative(status.last_request_at) : "just now"}.`
            : `Connected ${status.connected_at ? relative(status.connected_at) : ""}; ChatGPT has not sent anything through it yet.`}
        </Line>
      );
    case "stopped":
      return <Line tone="neutral" label="Off" />;
    default:
      return <Line tone="neutral" label="Not used" />;
  }
}

function Line({ tone, label, children }: {
  tone: Tone;
  label: string;
  children?: React.ReactNode;
}) {
  return (
    <div>
      <span className="flex items-center gap-2">
        <StatusDot tone={tone} />
        <span className="text-sm font-medium">{label}</span>
      </span>
      {children && (
        <p className="mt-1 max-w-[40ch] text-xs text-muted-foreground">{children}</p>
      )}
    </div>
  );
}

function Add({ plugins, fallbackWorkspaces, accounts, onDone, notify, onRefused, onMade }: {
  plugins: string[];
  /** Used only by an account that reports none of its own. */
  fallbackWorkspaces: string[];
  accounts: ChatGPTAccount[];
  onDone: () => void;
  notify: Notify;
  onRefused: (r: { reason: OpenAIReason; detail: string }) => void;
  /** Told the new tunnel's id, so the page can walk through the rest. */
  onMade: (id: string) => void;
}) {
  const [name, setName] = useState("");
  const [plugin, setPlugin] = useState("");
  // Only accounts that can actually make a tunnel: one without an admin key
  // and organisation cannot, and offering it would produce a refusal at the
  // point somebody presses Add rather than at the point they chose.
  const canMake = accounts.filter((a) => a.can_manage);
  const [account, setAccount] = useState(canMake[0]?.id ?? "");
  // A tunnel scoped only to the Platform organisation silently does not appear
  // in an Enterprise or Edu workspace, so the default is one that has worked.
  const [workspace, setWorkspace] = useState("");
  const [busy, setBusy] = useState(false);

  // The selected account's own workspaces. An account is an organisation, so
  // offering the union across every account would let a tunnel be created in
  // one the selected account cannot reach -- and the refusal arrives after
  // the tunnel is made, not while it is being chosen. The host-wide list is
  // the fallback only for an account with no tunnels yet, which has no
  // workspaces of its own to report.
  const chosen = canMake.find((a) => a.id === account);
  const workspaces = chosen?.workspaces?.length ? chosen.workspaces : fallbackWorkspaces;

  // Reset when the account changes, or a workspace belonging to the previous
  // account stays selected and is submitted against this one.
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
      setName("");
      if (made?.id) onMade(made.id);
    } catch (e) {
      showFailure(e, "Couldn't make it.", notify, onRefused);
    } finally {
      setBusy(false);
      onDone();
    }
  }

  return (
    <Card>
      <CardContent className="flex flex-wrap items-end gap-3">
        {canMake.length > 0 && (
          <div className="w-44 space-y-1.5">
            <Label htmlFor="tacct">Account</Label>
            <NativeSelect id="tacct" value={account}
                          onChange={(e) => setAccount(e.target.value)}>
              {canMake.map((a) => (
                <option key={a.id} value={a.id}>{a.name}</option>
              ))}
            </NativeSelect>
          </div>
        )}
        <div className="w-48 space-y-1.5">
          <Label htmlFor="tplug">Reaches</Label>
          <NativeSelect id="tplug" value={plugin}
                        onChange={(e) => setPlugin(e.target.value)}>
            <option value="">Everything</option>
            {plugins.map((p) => <option key={p} value={p}>{p}</option>)}
          </NativeSelect>
        </div>
        {workspaces.length > 0 ? (
          <div className="w-56 space-y-1.5">
            <Label htmlFor="tws">Workspace</Label>
            <NativeSelect id="tws" value={workspace}
                          onChange={(e) => setWorkspace(e.target.value)}>
              {workspaces.map((w) => <option key={w} value={w}>{w}</option>)}
              <option value="">None — organisation only</option>
            </NativeSelect>
          </div>
        ) : (
          <div className="w-56 space-y-1.5">
            <Label htmlFor="tws">Workspace (optional)</Label>
            <Input id="tws" type="text" value={workspace} placeholder="ws_..."
                   onChange={(e) => setWorkspace(e.target.value)} />
          </div>
        )}
        <div className="min-w-40 flex-1 space-y-1.5">
          <Label htmlFor="tname">Name (optional)</Label>
          <Input id="tname" type="text" value={name}
                 placeholder={plugin ? `mcpd: ${plugin}` : "mcpd"}
                 onChange={(e) => setName(e.target.value)} />
        </div>
        <Button disabled={busy} onClick={add}>
          {busy ? "Making…" : "Make"}
        </Button>
        <p className="w-full text-xs text-muted-foreground">
          Made it already on OpenAI's site? It appears under its account below
          once mcpd's admin key can see it; point it at a system there.{" "}
          <Link to="/settings/chatgpt" className="text-primary hover:underline">Accounts</Link>{" "}
          are managed in Settings.
        </p>
      </CardContent>
    </Card>
  );
}

/** What the "Reaches" select shows: the running tunnel, else the stored
 *  assignment, else nothing. */
function selected(row: Row, assigned?: string): string {
  if (row.status) return row.status.plugin || "*";
  if (assigned === undefined) return "";
  return assigned || "*";
}

/** The plugins offered, plus one a tunnel is already waiting on so its own
 *  assignment is not missing from the list that shows it. */
function optionsFor(plugins: string[], waitingOn: string): string[] {
  if (!waitingOn || plugins.includes(waitingOn)) return plugins;
  return [...plugins, waitingOn].sort();
}
