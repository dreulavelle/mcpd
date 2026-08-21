import { useCallback, useState } from "react";
import { api, ApiError, type Endpoints, type Plugin, type TunnelInfo } from "./api";
import {
  Copyable, Dot, Empty, Message, Out, Pill, Skeleton, usePoll, useToasts,
} from "./components";

const OPENAI_TUNNELS = "https://platform.openai.com/settings/organization/tunnels";

/**
 * Connections.
 *
 * Each system collapses to one row showing the two things worth knowing at a
 * glance: whether it's working, and what it is. Everything else opens on
 * demand — it's what you need while setting one up, and clutter afterwards.
 */
export function Connections() {
  const [plugins, setPlugins] = useState<Plugin[] | null>(null);
  const [endpoints, setEndpoints] = useState<Endpoints | null>(null);
  const [error, setError] = useState("");
  const [open, setOpen] = useState<string | null>(null);

  const load = useCallback(() => {
    api.plugins()
      .then((r) => { setPlugins(r.plugins ?? []); setError(""); })
      .catch((e) => { setPlugins([]); setError(e instanceof Error ? e.message : "Couldn't load connections."); });
    api.endpoints().then(setEndpoints).catch(() => setEndpoints(null));
  }, []);
  usePoll(load, 20_000);

  return (
    <>
      <h1>Connections</h1>
      <p className="lede">
        The systems mcpd can work with. Each has its own address, so you can
        give an assistant one without giving it the others.
      </p>

      {error && <Message tone="problem">{error}</Message>}

      <TunnelPanel />

      {plugins === null ? <Skeleton rows={3} /> : plugins.length === 0 ? (
        <Empty mark="+" title="No systems connected yet">
          Head to Setup to turn one on. There's a test connection you can use to
          check everything works first.
        </Empty>
      ) : (
        <>
          {endpoints && (
            <div className="card" style={{ marginBottom: "var(--s3)" }}>
              <div className="card-body">
                <h3>One address for everything</h3>
                <p className="note">
                  Gives an assistant everything its key is allowed to reach. Use
                  this when whatever you're connecting can only point at a
                  single address.
                </p>
                <Copyable value={endpoints.aggregate} label="address" />
              </div>
            </div>
          )}

          <div className="stack">
            {plugins.map((p) => (
              <Row key={p.name} plugin={p}
                   open={open === p.name}
                   onToggle={() => setOpen(open === p.name ? null : p.name)} />
            ))}
          </div>
        </>
      )}
    </>
  );
}

function Row({ plugin, open, onToggle }: { plugin: Plugin; open: boolean; onToggle: () => void }) {
  const lookups = plugin.tools.filter((t) => t.kind === "read");
  const changes = plugin.tools.filter((t) => t.kind === "propose");

  return (
    <div className={`expander${open ? " open" : ""}`}>
      <button className="expander-head" onClick={onToggle} aria-expanded={open}>
        <Dot tone={plugin.health === "healthy" ? "good" : plugin.health === "degraded" ? "attention" : "problem"} />
        <span className="expander-title">
          <div className="name">{plugin.title}</div>
          <div className="sub">{plugin.description}</div>
        </span>
        <span className="dim" style={{ fontSize: 13, whiteSpace: "nowrap" }}>
          {lookups.length} {lookups.length === 1 ? "lookup" : "lookups"}
          {changes.length > 0 && ` · ${changes.length} ${changes.length === 1 ? "change" : "changes"}`}
        </span>
        <span className="chevron" aria-hidden="true">›</span>
      </button>

      {open && (
        <div className="expander-body">
          {plugin.health !== "healthy" && (
            <div style={{ marginTop: "var(--s4)" }}>
              <Message tone={plugin.health === "degraded" ? "attention" : "problem"}>
                {plugin.health_message || "This connection isn't reaching the system it manages."}
              </Message>
            </div>
          )}

          <div className="section">
            <p className="eyebrow">Address</p>
            <p className="note">
              For connecting something directly. It only works with a key that's
              been given access to this system.
            </p>
            <Copyable value={plugin.connect_url} label="address" />
          </div>

          <div className="section split two">
            <div>
              <p className="eyebrow">Can look up</p>
              <p className="note">Happens straight away. Nothing changes.</p>
              <ul style={{ margin: 0, paddingLeft: 18, fontSize: 14 }}>
                {lookups.length === 0 && <li className="dim">Nothing</li>}
                {lookups.map((t) => <li key={t.name}>{plain(t.name, plugin.name)}</li>)}
              </ul>
            </div>
            <div>
              <p className="eyebrow">Can suggest</p>
              <p className="note">Waits for your approval. Nothing happens until you say so.</p>
              <ul style={{ margin: 0, paddingLeft: 18, fontSize: 14 }}>
                {changes.length === 0 && <li className="dim">Nothing</li>}
                {changes.map((t) => <li key={t.name}>{plain(t.name, plugin.name)}</li>)}
              </ul>
            </div>
          </div>

          {plugin.settings.length > 0 && (
            <div className="section">
              <p className="eyebrow">Settings</p>
              <p className="note">
                From your startup file. Passwords and keys aren't shown — you'll
                see where they're read from instead.
              </p>
              <dl className="kv">
                {plugin.settings.map((s) => (
                  <div key={s.key}>
                    <dt>{s.key.replace(/_/g, " ")}</dt>
                    <dd>
                      <code>{s.value}</code>
                      {s.secret && <Pill>hidden</Pill>}
                    </dd>
                  </div>
                ))}
              </dl>
            </div>
          )}

          <div className="section">
            <p className="eyebrow">About</p>
            <dl className="kv">
              <div><dt>Version</dt><dd><code>{plugin.version}</code></dd></div>
              {plugin.required && (
                <div>
                  <dt>Required</dt>
                  <dd>Yes <span className="dim">— mcpd won't start without it</span></dd>
                </div>
              )}
            </dl>
          </div>
        </div>
      )}
    </div>
  );
}

/** cnmaestro_list_devices → "list devices" */
function plain(tool: string, plugin: string): string {
  return tool.replace(new RegExp(`^${plugin}_`), "").replace(/_/g, " ");
}

/* ── tunnel ─────────────────────────────────────────────────────────────── */

function TunnelPanel() {
  const [info, setInfo] = useState<TunnelInfo | null>(null);
  const [busy, setBusy] = useState(false);
  const { show, view } = useToasts();

  const load = useCallback(() => {
    api.tunnel().then(setInfo).catch(() => setInfo(null));
  }, []);
  usePoll(load, 8_000);

  async function act(action: "start" | "stop") {
    setBusy(true);
    try {
      await (action === "start" ? api.tunnelStart() : api.tunnelStop());
      show("good", action === "start" ? "Connected to ChatGPT." : "Disconnected.");
    } catch (e) {
      show("problem", e instanceof ApiError ? e.detail : "Couldn't reach mcpd.");
    } finally {
      setBusy(false);
      load();
    }
  }

  if (!info) return null;
  const { status, version } = info;
  const off = status.state === "disabled";

  return (
    <>
      {view}
      <div className="card" style={{ marginBottom: "var(--s3)" }}>
        <div className="card-body">
          <div className="row" style={{ marginBottom: off ? 0 : "var(--s4)" }}>
            <Dot tone={tone(status.state)} />
            <div style={{ flex: 1 }}>
              <h3 style={{ marginBottom: 2 }}>ChatGPT</h3>
              <p className="note tight">{describe(status.state)}</p>
            </div>
            {!off && (
              <button className={`btn ${status.state === "connected" ? "" : "primary"}`}
                      disabled={busy} onClick={() => act(status.state === "connected" ? "stop" : "start")}>
                {busy ? "Working…" : status.state === "connected" ? "Disconnect" : "Connect"}
              </button>
            )}
          </div>

          {status.state === "failed" && status.message && (
            <Message tone="problem">{status.message}</Message>
          )}

          {off ? (
            <p className="note" style={{ marginTop: "var(--s2)" }}>
              Set this up in Settings and ChatGPT can reach mcpd without you
              opening anything to the internet. You'll need a tunnel from{" "}
              <Out href={OPENAI_TUNNELS}>your OpenAI account</Out>.
            </p>
          ) : (
            <dl className="kv">
              {status.mcp_url ? (
                <div>
                  <dt>Who's asking</dt>
                  <dd>
                    <Pill tone="good">Each person signs in</Pill>
                    <span className="note tight">
                      ChatGPT asks whoever uses it to sign in, so what they can
                      do here is their own.
                    </span>
                  </dd>
                </div>
              ) : (
                <>
                  <div>
                    <dt>Who's asking</dt>
                    <dd><code>{status.principal}</code><Pill>{roleLabel(status.role)}</Pill></dd>
                  </div>
                  <div>
                    <dt>Can reach</dt>
                    <dd><code>{status.plugins?.includes("*") ? "everything" : status.plugins?.join(", ")}</code></dd>
                  </div>
                </>
              )}
            </dl>
          )}

          {version?.update_available && (
            <div style={{ marginTop: "var(--s4)" }}>
              <Message tone="attention">
                A newer version is out ({version.latest}). It's built into mcpd,
                so picking it up means rebuilding — nothing updates itself.
              </Message>
            </div>
          )}
        </div>
      </div>
    </>
  );
}

function tone(state: TunnelInfo["status"]["state"]) {
  switch (state) {
    case "connected": return "good" as const;
    case "starting": return "busy" as const;
    case "failed": return "problem" as const;
    default: return "" as const;
  }
}

function describe(state: TunnelInfo["status"]["state"]): string {
  switch (state) {
    case "connected": return "Connected — ChatGPT can reach mcpd.";
    case "starting": return "Connecting…";
    case "failed": return "Couldn't connect.";
    case "stopped": return "Set up, but switched off.";
    default: return "Not set up yet.";
  }
}

function roleLabel(role?: string): string {
  switch (role) {
    case "viewer": return "can look, not touch";
    case "operator": return "can suggest changes";
    case "approver": return "can approve its own changes";
    case "admin": return "full access";
    default: return role ?? "";
  }
}
