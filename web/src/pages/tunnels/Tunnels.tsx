import { useCallback, useState } from "react";
import { Waypoints } from "lucide-react";
import {
  isOpenAIReason, OpenAIPermissionDialog, type OpenAIReason,
} from "@/components/openai-permission";
import {
  api, ApiError,
  type ChatGPTAccount, type OpenAITunnel, type TunnelInfo, type TunnelStatus,
} from "@/lib/api";
import { usePoll } from "@/lib/hooks";
import { useCan } from "@/lib/session";
import {
  Copyable, EmptyState, Loading, Notice, Out, PageHeader,
} from "@/components/chrome";
import { StatusDot, type Tone } from "@/components/status";
import { useNotify, type Notify } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";

const OPENAI_TUNNELS = "https://platform.openai.com/settings/organization/tunnels";
const CHATGPT_CONNECTORS = "https://chatgpt.com/#settings/Connectors";

/** A tunnel carries one address, so it is one connector in ChatGPT. */
export function Tunnels() {
  const [info, setInfo] = useState<TunnelInfo | null>(null);
  const [error, setError] = useState("");
  // A refusal from OpenAI is several paragraphs of instruction, which a toast
  // cannot carry: newlines collapse and the numbered steps run together. It
  // gets a dialog; everything else stays a toast.
  const [refused, setRefused] = useState<{ reason: OpenAIReason; detail: string } | null>(null);

  const notify = useNotify();
  const admin = useCan("admin");

  const load = useCallback(() => {
    api.tunnel()
      .then((t) => { setInfo(t); setError(""); })
      .catch(() => setError("Couldn't load tunnels."));
  }, []);
  usePoll(load, 8_000);

  const running = new Map((info?.tunnels ?? []).map((t) => [t.tunnel_id ?? "", t]));
  // An older build sends null rather than [] for an empty list.
  const plugins = info?.plugins ?? [];
  const assignments = info?.assignments ?? {};
  const accountOf = info?.account_assignments ?? {};
  const accounts = info?.accounts ?? [];
  const rows: Row[] = !info ? [] : info.can_manage
    ? (info.available ?? []).map((t) => ({ ...t, status: running.get(t.id) }))
    : (info.tunnels ?? []).map((t) => ({
        id: t.tunnel_id ?? "", name: t.plugin || "Everything", status: t,
        account_id: accountOf[t.tunnel_id ?? ""],
      }));

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
            One tunnel is one connector in ChatGPT. Copy a tunnel's ID, then add
            it under <Out href={CHATGPT_CONNECTORS}>Connectors</Out> — ChatGPT
            has no API for that last step, so it is the one part mcpd cannot do
            for you.
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

      {info?.can_manage && admin && (
        <Add plugins={plugins} workspaces={info.workspaces ?? []}
             accounts={accounts} onDone={load} notify={notify}
             onRefused={setRefused} />
      )}

      {!info ? <Loading rows={4} /> : rows.length === 0 ? (
        <EmptyState mark={<Waypoints />} title="No tunnels yet" />
      ) : (
        <Card className="mt-4 overflow-hidden p-0">
          <div className="scroll-x">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  {/* Always, not only when there are several. Two tunnels can
                      serve the same plugin name on different accounts, and
                      then the plugin name identifies neither of them -- so the
                      column that disambiguates them cannot be the one that
                      appears only once the confusion has already started. */}
                  <TableHead>Account</TableHead>
                  <TableHead>Reaches</TableHead>
                  <TableHead>State</TableHead>
                  <TableHead className="w-px" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((row) => (
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
    </>
  );
}

interface Row extends OpenAITunnel {
  status?: TunnelStatus;
}

/** No account chosen, and more than one to choose from. The tunnel will not
 *  start, and saying so here is the difference between a fixable mistake and
 *  a connector that silently does nothing. */
// showFailure decides where a failure is shown.
//
// A refusal from OpenAI carries instructions -- which permission, granted
// where, by whom -- and a toast flattens them into one line with the numbered
// steps run together. Those get a dialog. Everything else is a toast, because
// everything else is one sentence.
function showFailure(
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
  const state = row.status?.state;
  const admin = useCan("admin");
  // Assigned to a plugin that is not mounted, so the tunnel is not started.
  // Until this said so, the assignment looked like it had not taken.
  const waitingOn = assigned && !row.status && !plugins.includes(assigned)
    ? assigned : "";
  const unassigned = needsAccount(account, accounts);

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

  async function remove() {
    if (!confirm(`Delete "${row.name}"? Any connector using it stops working.`)) return;
    try {
      // Deleted from the organisation it actually lives in. Two accounts are
      // two organisations, and deleting from the wrong one cannot be undone.
      await api.deleteTunnel(row.id, account ?? row.account_id);
      notify("good", "Deleted.");
    } catch (e) {
      showFailure(e, "Couldn't delete it.", notify, onRefused);
    } finally {
      onDone();
    }
  }

  return (
    <TableRow>
      <TableCell>
        <div className="font-medium">{row.name}</div>
        {/* Copyable: ChatGPT accepts a tunnel ID typed in, and
            an ID shown with the middle missing cannot be typed anywhere. */}
        <Copyable value={row.id} label="tunnel ID" className="mt-1 max-w-[24rem]" />
      </TableCell>
      <TableCell>
        {info.can_manage && admin ? (
          <div className="w-40">
            <NativeSelect
              aria-label="Account" value={account ?? ""}
              onChange={(e) => assign(selected(row, assigned), e.target.value)}
            >
              <option value="">Not set</option>
              {accounts.map((a) => (
                <option key={a.id} value={a.id}>{a.name}</option>
              ))}
            </NativeSelect>
          </div>
        ) : (
          <code className="font-mono text-xs">
            {accounts.find((a) => a.id === account)?.name ?? "—"}
          </code>
        )}
      </TableCell>
      <TableCell>
        {info.can_manage && admin ? (
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
      <TableCell>
        <span className="flex items-center gap-2">
          <StatusDot
            tone={waitingOn || unassigned ? "attention" : tone(state)}
          />
          <span className="text-xs text-muted-foreground">
            {unassigned ? "No account" : waitingOn ? "Waiting" : describe(state)}
          </span>
        </span>
        {unassigned && (
          <p className="mt-1 text-xs text-muted-foreground">
            This host has more than one ChatGPT account, so a tunnel has to say
            which one it connects with. Until it does, it is not started.
          </p>
        )}
        {waitingOn && (
          <p className="mt-1 text-xs text-muted-foreground">
            {waitingOn} is not running, so this tunnel is not started. It
            connects on its own once that plugin has what it needs.
          </p>
        )}
        {state === "failed" && row.status?.message && (
          <p className="mt-1 text-xs text-muted-foreground">{row.status.message}</p>
        )}
      </TableCell>
      <TableCell>
        {info.can_manage && admin && (
          <Button
            variant="outline" size="sm" onClick={remove}
            className="text-destructive hover:text-destructive"
          >
            Remove
          </Button>
        )}
      </TableCell>
    </TableRow>
  );
}

function Add({ plugins, workspaces, accounts, onDone, notify, onRefused }: {
  plugins: string[];
  workspaces: string[];
  accounts: ChatGPTAccount[];
  onDone: () => void;
  notify: Notify;
  onRefused: (r: { reason: OpenAIReason; detail: string }) => void;
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
  const [workspace, setWorkspace] = useState(workspaces[0] ?? "");
  const [busy, setBusy] = useState(false);

  async function add() {
    setBusy(true);
    try {
      await api.createTunnel(name.trim(), plugin, workspace.trim(), account);
      // A tunnel is not active for the first half minute; OpenAI's own CLI
      // says the same after creating one.
      notify("good", "Made. Give it about 30 seconds to become active in ChatGPT.");
      setName("");
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
          {busy ? "Adding…" : "Add"}
        </Button>
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

function tone(state?: TunnelStatus["state"]): Tone {
  switch (state) {
    case "connected": return "good";
    case "starting": return "info";
    case "failed": return "problem";
    default: return "neutral";
  }
}

function describe(state?: TunnelStatus["state"]): string {
  switch (state) {
    case "connected": return "Ready";
    case "starting": return "Connecting";
    case "failed": return "Failed";
    case "stopped": return "Off";
    default: return "Not used";
  }
}
