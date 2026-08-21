import { useCallback, useState } from "react";
import { api, ApiError, type OpenAITunnel, type TunnelInfo, type TunnelStatus } from "./api";
import { Copyable, Dot, Message, Out, Skeleton, useIsAdmin, usePoll, useToasts, type Notify } from "./components";

const OPENAI_TUNNELS = "https://platform.openai.com/settings/organization/tunnels";
const CHATGPT_CONNECTORS = "https://chatgpt.com/#settings/Connectors";

/**
 * Tunnels.
 *
 * A tunnel carries exactly one address, so it is one connector in ChatGPT:
 * one for everything, or one per system to keep them apart. The page is a list
 * of them because that is all a tunnel is -- a name, what it reaches, and
 * whether it is up.
 */
export function Tunnels() {
  const [info, setInfo] = useState<TunnelInfo | null>(null);
  const [error, setError] = useState("");
  const { show, view } = useToasts();
  const admin = useIsAdmin();

  const load = useCallback(() => {
    api.tunnel()
      .then((t) => { setInfo(t); setError(""); })
      .catch(() => setError("Couldn't load tunnels."));
  }, []);
  usePoll(load, 8_000);

  const running = new Map((info?.tunnels ?? []).map((t) => [t.tunnel_id ?? "", t]));
  const rows: Row[] = !info ? [] : info.can_manage
    ? (info.available ?? []).map((t) => ({ ...t, status: running.get(t.id) }))
    : info.tunnels.map((t) => ({
        id: t.tunnel_id ?? "", name: t.plugin || "Everything", status: t,
      }));

  return (
    <>
      {view}
      <h1>Tunnels</h1>
      <p className="lede">
        One tunnel is one connector in ChatGPT. Copy a tunnel's ID, then add it
        under <Out href={CHATGPT_CONNECTORS}>Connectors</Out> — ChatGPT has no
        API for that last step, so it is the one part mcpd cannot do for you.
      </p>

      {error && <Message tone="problem">{error}</Message>}
      {info?.problem && <Message tone="problem">{info.problem}</Message>}

      {info?.missing && (
        <Message tone="info">
          <span>
            Add {info.missing} in Settings to make tunnels from here, or make
            them on <Out href={OPENAI_TUNNELS}>OpenAI's site</Out>.
          </span>
        </Message>
      )}

      {info?.can_manage && admin && (
        <Add plugins={info.plugins} workspaces={info.workspaces ?? []}
             onDone={load} show={show} />
      )}

      {!info ? <Skeleton rows={4} /> : (
      <div className="card" style={{ marginTop: "var(--s4)" }}>
        {rows.length === 0 ? (
          <div className="card-body">
            <p className="note tight">No tunnels yet.</p>
          </div>
        ) : (
          <div className="tablewrap">
            <table>
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Reaches</th>
                  <th>State</th>
                  <th style={{ width: "1%" }}></th>
                </tr>
              </thead>
              <tbody>
                {rows.map((row) => (
                  <TunnelRow key={row.id} row={row} info={info} onDone={load} show={show} />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
      )}
    </>
  );
}

interface Row extends OpenAITunnel {
  status?: TunnelStatus;
}

function TunnelRow({ row, info, onDone, show }: {
  row: Row;
  info: TunnelInfo;
  onDone: () => void;
  show: Notify;
}) {
  const state = row.status?.state;
  const admin = useIsAdmin();

  async function assign(to: string) {
    try {
      await api.assignTunnel(row.id, to === "*" ? "" : to);
    } catch (e) {
      show("problem", e instanceof ApiError ? e.detail : "Couldn't change that.");
    } finally {
      onDone();
    }
  }

  async function remove() {
    if (!confirm(`Delete "${row.name}"? Any connector using it stops working.`)) return;
    try {
      await api.deleteTunnel(row.id);
      show("good", "Deleted.");
    } catch (e) {
      show("problem", e instanceof ApiError ? e.detail : "Couldn't delete it.");
    } finally {
      onDone();
    }
  }

  return (
    <tr>
      <td>
        <div className="tunnel-name">{row.name}</div>
        {/* Whole, and copyable: ChatGPT will accept a tunnel ID typed in, and
            an ID shown with the middle missing cannot be typed anywhere. */}
        <Copyable value={row.id} label="tunnel ID" inline />
      </td>
      <td>
        {info.can_manage && admin ? (
          <select value={row.status ? (row.status.plugin || "*") : ""}
                  onChange={(e) => assign(e.target.value)}>
            <option value="">Not used</option>
            <option value="*">Everything</option>
            {info.plugins.map((p) => <option key={p} value={p}>{p}</option>)}
          </select>
        ) : (
          <code>{row.status?.plugin || "Everything"}</code>
        )}
      </td>
      <td>
        <span className="row">
          <Dot tone={tone(state)} />
          <span className="note tight">{describe(state)}</span>
        </span>
        {state === "failed" && row.status?.message && (
          <p className="note tight" style={{ marginTop: 4 }}>{row.status.message}</p>
        )}
      </td>
      <td>
        {info.can_manage && admin && (
          <button className="btn sm danger" onClick={remove}>Remove</button>
        )}
      </td>
    </tr>
  );
}

function Add({ plugins, workspaces, onDone, show }: {
  plugins: string[];
  workspaces: string[];
  onDone: () => void;
  show: Notify;
}) {
  const [name, setName] = useState("");
  const [plugin, setPlugin] = useState("");
  // Defaulted to the first known workspace. A tunnel scoped only to the
  // Platform organisation does not appear in an Enterprise or Edu workspace,
  // which is the failure this exists to prevent -- and it is silent, so the
  // safe default is the one that has worked before.
  const [workspace, setWorkspace] = useState(workspaces[0] ?? "");
  const [busy, setBusy] = useState(false);

  async function add() {
    setBusy(true);
    try {
      await api.createTunnel(name.trim(), plugin, workspace.trim());
      // OpenAI's own CLI prints the same caution after creating one: a tunnel
      // is not active for the first half minute. Saying "made" and stopping
      // sends someone to ChatGPT to look for a connector that is not there
      // yet, and conclude it failed.
      show("good", "Made. Give it about 30 seconds to become active in ChatGPT.");
      setName("");
    } catch (e) {
      show("problem", e instanceof ApiError ? e.detail : "Couldn't make it.");
    } finally {
      setBusy(false);
      onDone();
    }
  }

  return (
    <div className="row wrap" style={{ alignItems: "flex-end" }}>
      <div className="field" style={{ marginBottom: 0, flex: "0 1 12rem" }}>
        <label htmlFor="tplug">Reaches</label>
        <select id="tplug" value={plugin} onChange={(e) => setPlugin(e.target.value)}>
          <option value="">Everything</option>
          {plugins.map((p) => <option key={p} value={p}>{p}</option>)}
        </select>
      </div>
      {workspaces.length > 0 ? (
        <div className="field" style={{ marginBottom: 0, flex: "0 1 14rem" }}>
          <label htmlFor="tws">Workspace</label>
          <select id="tws" value={workspace} onChange={(e) => setWorkspace(e.target.value)}>
            {workspaces.map((w) => <option key={w} value={w}>{w}</option>)}
            <option value="">None — organisation only</option>
          </select>
        </div>
      ) : (
        <div className="field" style={{ marginBottom: 0, flex: "0 1 14rem" }}>
          <label htmlFor="tws">Workspace (optional)</label>
          <input id="tws" type="text" value={workspace} placeholder="ws_..."
                 onChange={(e) => setWorkspace(e.target.value)} />
        </div>
      )}
      <div className="field" style={{ marginBottom: 0, flex: "1 1 10rem" }}>
        <label htmlFor="tname">Name (optional)</label>
        <input id="tname" type="text" value={name}
               placeholder={plugin ? `mcpd: ${plugin}` : "mcpd"}
               onChange={(e) => setName(e.target.value)} />
      </div>
      <button className="btn primary" disabled={busy} onClick={add}>
        {busy ? "Adding…" : "Add"}
      </button>
    </div>
  );
}

function tone(state?: TunnelStatus["state"]) {
  switch (state) {
    case "connected": return "good" as const;
    case "starting": return "busy" as const;
    case "failed": return "problem" as const;
    default: return "" as const;
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

