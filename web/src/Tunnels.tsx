import { useCallback, useState } from "react";
import {
  api, ApiError,
  type Meta, type OpenAITunnel, type SettingGroup, type SettingsPayload,
  type TunnelInfo, type TunnelStatus,
} from "./api";
import { Dot, Message, Out, Skeleton, useToasts, usePoll } from "./components";
import { SettingsForm } from "./SettingsForm";

const OPENAI_TUNNELS = "https://platform.openai.com/settings/organization/tunnels";
const OPENAI_API_KEYS = "https://platform.openai.com/settings/organization/api-keys";
const OPENAI_ADMIN_KEYS = "https://platform.openai.com/settings/organization/admin-keys";

/**
 * Tunnels.
 *
 * A tunnel carries exactly one address, so it maps one-to-one onto a connector
 * in ChatGPT: one for everything a person is allowed to use, or one per system
 * to keep them apart. That constraint is the whole shape of this page — every
 * row here is a connector over there.
 */
export function Tunnels() {
  const [info, setInfo] = useState<TunnelInfo | null>(null);
  const [settings, setSettings] = useState<SettingsPayload | null>(null);
  const [meta, setMeta] = useState<Meta | null>(null);
  const [error, setError] = useState("");
  const { show, view } = useToasts();

  const load = useCallback(() => {
    api.tunnel()
      .then((t) => { setInfo(t); setError(""); })
      .catch(() => setError("Couldn't load your tunnels. Is mcpd still running?"));
    api.settings().then(setSettings).catch(() => setSettings(null));
    api.meta().then(setMeta).catch(() => setMeta(null));
  }, []);
  usePoll(load, 8_000);

  const group = settings?.groups.find((g) => g.section === "tunnels");

  return (
    <>
      {view}
      <h1>ChatGPT</h1>
      <p className="lede">
        How ChatGPT reaches mcpd, without opening anything to the internet. The
        connection is made outward from here, so nothing has to reach in.
      </p>

      {error && <Message tone="problem">{error}</Message>}

      {!info ? <Skeleton rows={4} /> : (
        <>
          <Running info={info} onChange={load} show={show} />
          {info.can_manage
            ? <Manage info={info} onChange={load} show={show} />
            : <NoAdminKey />}
          {group && settings && (
            <Credentials
              group={relevant(group, info.can_manage, meta?.auth_mode ?? "")}
              settings={settings} onSaved={load} show={show} />
          )}
          <Guide connected={info.tunnels.some((t) => t.state === "connected")} />
        </>
      )}
    </>
  );
}

/* ── what's running ─────────────────────────────────────────────────────── */

function Running({ info, onChange, show }: {
  info: TunnelInfo;
  onChange: () => void;
  show: (tone: "good" | "problem", text: string) => void;
}) {
  const [busy, setBusy] = useState(false);
  const { tunnels } = info;
  const anyUp = tunnels.some((t) => t.state === "connected");

  async function act(action: "start" | "stop") {
    setBusy(true);
    try {
      await (action === "start" ? api.tunnelStart() : api.tunnelStop());
      show("good", action === "start" ? "Connecting." : "Disconnected.");
    } catch (e) {
      show("problem", e instanceof ApiError ? e.detail : "Couldn't reach mcpd.");
    } finally {
      setBusy(false);
      onChange();
    }
  }

  if (tunnels.length === 0) {
    return (
      <div className="card">
        <div className="card-body">
          <h3>Nothing connected yet</h3>
          <p className="note">
            Make a tunnel below, or on{" "}
            <Out href={OPENAI_TUNNELS}>OpenAI's tunnels page</Out>, and ChatGPT
            can reach mcpd. Follow the steps at the bottom of this page if this
            is your first one.
          </p>
        </div>
      </div>
    );
  }

  return (
    <div className="card">
      <div className="card-head">
        <h3 style={{ flex: 1 }}>Running now</h3>
        <button className={`btn sm ${anyUp ? "" : "primary"}`} disabled={busy}
                onClick={() => act(anyUp ? "stop" : "start")}>
          {busy ? "Working…" : anyUp ? "Disconnect all" : "Connect"}
        </button>
      </div>
      <div className="card-body stack">
        {tunnels.map((t) => <RunningRow key={t.plugin || "*"} status={t} />)}
      </div>
    </div>
  );
}

function RunningRow({ status }: { status: TunnelStatus }) {
  return (
    <div>
      <div className="row">
        <Dot tone={tone(status.state)} />
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ fontWeight: 600, fontSize: 14 }}>
            {status.plugin || "Everything you're allowed"}
          </div>
          <p className="note tight">{describe(status.state)}</p>
        </div>
        <code style={{ fontSize: 12 }}>{shortID(status.tunnel_id)}</code>
      </div>
      {status.state === "failed" && status.message && (
        <div style={{ marginTop: "var(--s2)" }}>
          <Message tone="problem">{status.message}</Message>
        </div>
      )}
    </div>
  );
}

/* ── managing tunnels at OpenAI ─────────────────────────────────────────── */

function Manage({ info, onChange, show }: {
  info: TunnelInfo;
  onChange: () => void;
  show: (tone: "good" | "problem", text: string) => void;
}) {
  const [name, setName] = useState("");
  const [plugin, setPlugin] = useState("");
  const [busy, setBusy] = useState(false);

  const used = new Map(info.tunnels.map((t) => [t.tunnel_id ?? "", t.plugin ?? ""]));

  async function create() {
    setBusy(true);
    try {
      await api.createTunnel(name.trim(), plugin);
      show("good", "Tunnel made, and mcpd is connecting to it.");
      setName("");
    } catch (e) {
      show("problem", e instanceof ApiError ? e.detail : "Couldn't make the tunnel.");
    } finally {
      setBusy(false);
      onChange();
    }
  }

  async function assign(id: string, to: string) {
    try {
      await api.assignTunnel(id, to);
      show("good", "Done — mcpd is reconnecting.");
    } catch (e) {
      show("problem", e instanceof ApiError ? e.detail : "Couldn't change that.");
    } finally {
      onChange();
    }
  }

  async function remove(t: OpenAITunnel) {
    if (!confirm(
      `Delete "${t.name}" from your OpenAI account?\n\n` +
      `Any connector using it stops working, wherever it is. This can't be undone.`
    )) return;
    try {
      await api.deleteTunnel(t.id);
      show("good", "Deleted.");
    } catch (e) {
      show("problem", e instanceof ApiError ? e.detail : "Couldn't delete it.");
    } finally {
      onChange();
    }
  }

  return (
    <>
      <h2>Make a connector</h2>
      <div className="card">
        <div className="card-body">
          <p className="note">
            Each one is a separate connector in ChatGPT. A tunnel carries a
            single address, so a system that needs a connector of its own needs
            a tunnel of its own.
          </p>

          <div className="row wrap" style={{ marginTop: "var(--s4)", alignItems: "flex-end" }}>
            <div className="field" style={{ marginBottom: 0, flex: "1 1 14rem" }}>
              <label htmlFor="tname">Call it</label>
              <input id="tname" type="text" value={name} placeholder="ChatGPT"
                     onChange={(e) => setName(e.target.value)} />
            </div>
            <div className="field" style={{ marginBottom: 0, flex: "0 1 14rem" }}>
              <label htmlFor="tplug">Let it reach</label>
              <select id="tplug" value={plugin} onChange={(e) => setPlugin(e.target.value)}>
                <option value="">Everything you're allowed</option>
                {info.plugins.map((p) => <option key={p} value={p}>{p} only</option>)}
              </select>
            </div>
            <button className="btn primary" disabled={busy || !name.trim()} onClick={create}>
              {busy ? "Making…" : "Make it"}
            </button>
          </div>
        </div>
      </div>

      {info.problem && <Message tone="attention">{info.problem}</Message>}

      {info.available && info.available.length > 0 && (
        <>
          <h2>In your OpenAI account</h2>
          <div className="card">
            <div className="tablewrap">
              <table>
                <thead>
                  <tr>
                    <th>Name</th>
                    <th>Reaches</th>
                    <th style={{ width: "1%" }}></th>
                  </tr>
                </thead>
                <tbody>
                  {info.available.map((t) => (
                    <tr key={t.id}>
                      <td>
                        <div style={{ fontWeight: 550 }}>{t.name}</div>
                        <code style={{ fontSize: 12 }}>{shortID(t.id)}</code>
                      </td>
                      <td>
                        <select value={used.has(t.id) ? (used.get(t.id) || "*") : ""}
                                onChange={(e) => assign(t.id, e.target.value === "*" ? "" : e.target.value)}>
                          <option value="">Not used here</option>
                          <option value="*">Everything you're allowed</option>
                          {info.plugins.map((p) => (
                            <option key={p} value={p}>{p} only</option>
                          ))}
                        </select>
                      </td>
                      <td>
                        <button className="btn sm danger" onClick={() => remove(t)}>
                          Delete
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
          <p className="note" style={{ marginTop: "var(--s2)" }}>
            Deleting removes it from your OpenAI account for good, and any
            connector using it stops working.
          </p>
        </>
      )}
    </>
  );
}

function NoAdminKey() {
  return (
    <Message tone="info">
      <span>
        <strong>You can make tunnels from here.</strong> Add an admin key below
        — it's a different key from the one that runs the connection, made on{" "}
        <Out href={OPENAI_ADMIN_KEYS}>the admin keys page</Out>. Without it
        you'll make tunnels on{" "}
        <Out href={OPENAI_TUNNELS}>OpenAI's site</Out> and paste the ID here.
      </span>
    </Message>
  );
}

/* ── credentials ────────────────────────────────────────────────────────── */

/**
 * Hides fields this deployment cannot act on.
 *
 * A form that offers settings which do nothing is worse than one that omits
 * them: it invites someone to fill them in and then quietly ignores the value.
 * Two things decide it here. With an admin key, tunnel IDs are chosen above
 * rather than typed. And under OAuth the connector's own sign-in decides who
 * is asking, so a configured identity is never consulted.
 */
function relevant(group: SettingGroup, canManage: boolean, authMode: string): SettingGroup {
  const byID = ["tunnel.tunnel_id"];
  const identity = ["tunnel.principal", "tunnel.role", "tunnel.plugins"];
  const oauth = authMode === "oauth" || authMode === "mixed";

  return {
    ...group,
    fields: group.fields.filter((f) => {
      if (canManage && (byID.includes(f.key) || f.key.startsWith("tunnel.plugin."))) {
        return false;
      }
      if (oauth && identity.includes(f.key)) return false;
      return true;
    }),
  };
}

function Credentials({ group, settings, onSaved, show }: {
  group: SettingGroup;
  settings: SettingsPayload;
  onSaved: () => void;
  show: (tone: "good" | "problem", text: string) => void;
}) {
  return (
    <>
      <h2>Keys and identity</h2>
      <SettingsForm
        groups={[group]}
        settings={settings}
        links={{
          "tunnel.tunnel_id": { href: OPENAI_TUNNELS, label: "Find it in your OpenAI account" },
          "tunnel.api_key": { href: OPENAI_API_KEYS, label: "Create one on the API keys page" },
          "tunnel.admin_key": { href: OPENAI_ADMIN_KEYS, label: "Create one on the admin keys page" },
        }}
        onSaved={onSaved}
        show={show}
      />
    </>
  );
}

/* ── first-time guide ───────────────────────────────────────────────────── */

function Guide({ connected }: { connected: boolean }) {
  const [open, setOpen] = useState(!connected);

  return (
    <>
      <h2>
        <button className="btn quiet sm" onClick={() => setOpen(!open)}
                style={{ marginLeft: -11 }}>
          {open ? "▾" : "▸"} Setting this up for the first time
        </button>
      </h2>

      {open && (
        <div className="card">
          <div className="card-body">
            <Step n={1} title="Get your keys">
              <p>
                A <strong>runtime key</strong> from{" "}
                <Out href={OPENAI_API_KEYS}>API keys</Out>, with the{" "}
                <strong>Tunnels: Read and Use</strong> permission — that one
                runs the connection. Optionally an{" "}
                <strong>admin key</strong> from{" "}
                <Out href={OPENAI_ADMIN_KEYS}>admin keys</Out>, which lets you
                make tunnels without leaving this page.
              </p>
              <p className="note tight">
                They look alike and sit on adjacent pages. Keys from anywhere
                else in your OpenAI settings won't work for either job.
              </p>
            </Step>

            <Step n={2} title="Paste them in below">
              <p>Save, and mcpd connects straight away.</p>
            </Step>

            <Step n={3} title="Make a connector">
              <p>
                Name it, choose what it may reach, and press Make it. mcpd
                creates the tunnel and starts serving it.
              </p>
            </Step>

            <Step n={4} title="Add it in ChatGPT">
              <p>
                In ChatGPT: <strong>Plugins</strong>, create a developer-mode
                app, and choose <strong>Tunnel</strong>. Yours will be in the
                list.
              </p>
              <Message tone="attention">
                <strong>Don't paste an address.</strong> If ChatGPT is asking
                for an MCP server URL, you're on the wrong option — pick the
                tunnel from the list. mcpd is on your own network, so ChatGPT
                can't reach it directly whatever you type. Reaching it anyway
                is what the tunnel is for.
              </Message>
            </Step>

            <Step n={5} title="Sign in when it asks">
              <p>
                ChatGPT sends you here to sign in and confirm what it may do.
                That's how mcpd knows who's asking — everyone using the
                connector gets their own sign-in rather than sharing one.
              </p>
            </Step>
          </div>
        </div>
      )}
    </>
  );
}

function Step({ n, title, children }: {
  n: number; title: string; children: React.ReactNode;
}) {
  return (
    <div className="step">
      <div className="step-n">{n}</div>
      <div className="step-body">
        <h3>{title}</h3>
        {children}
      </div>
    </div>
  );
}

/* ── helpers ────────────────────────────────────────────────────────────── */

function tone(state: TunnelStatus["state"]) {
  switch (state) {
    case "connected": return "good" as const;
    case "starting": return "busy" as const;
    case "failed": return "problem" as const;
    default: return "" as const;
  }
}

function describe(state: TunnelStatus["state"]): string {
  switch (state) {
    case "connected": return "Ready — ChatGPT can reach mcpd.";
    case "starting": return "Connecting…";
    case "failed": return "Couldn't connect.";
    case "stopped": return "Set up, but switched off.";
    default: return "Not set up yet.";
  }
}

/** tunnel_6a87ab02… — enough to tell two apart without a wall of hex. */
function shortID(id?: string): string {
  if (!id) return "";
  return id.length > 20 ? `${id.slice(0, 16)}…` : id;
}
