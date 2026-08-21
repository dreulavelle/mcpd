import { useCallback, useState } from "react";
import {
  api, ApiError, type Endpoints, type Plugin, type TunnelInfo, type TunnelStatus,
} from "./api";
import {
  CodeBlock, Copyable, Dot, Empty, Message, Pill, Skeleton, usePoll, useToasts,
} from "./components";

/**
 * Plugins.
 *
 * One row each. The route is on the row because it is the thing people come
 * here to copy, and a tunnel is made from the row because a tunnel serves
 * exactly one route -- deciding which plugin gets a connector is a decision
 * about a plugin, so it is made where the plugin is.
 */
export function Plugins() {
  const [plugins, setPlugins] = useState<Plugin[] | null>(null);
  const [endpoints, setEndpoints] = useState<Endpoints | null>(null);
  const [tunnels, setTunnels] = useState<TunnelInfo | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  const [error, setError] = useState("");
  const { show, view } = useToasts();

  const load = useCallback(() => {
    api.plugins()
      .then((r) => { setPlugins(r.plugins ?? []); setError(""); })
      .catch(() => setError("Couldn't load plugins."));
    api.endpoints().then(setEndpoints).catch(() => setEndpoints(null));
    api.tunnel().then(setTunnels).catch(() => setTunnels(null));
  }, []);
  usePoll(load, 15_000);

  return (
    <>
      {view}
      <h1>Plugins</h1>
      <p className="lede">What mcpd can work with.</p>

      {error && <Message tone="problem">{error}</Message>}

      {!plugins ? (
        <Skeleton rows={3} />
      ) : plugins.length === 0 ? (
        <Empty mark="○" title="No plugins yet">
          Add one to your startup file and restart.
        </Empty>
      ) : (
        <>
          <div className="stack">
            {plugins.map((p) => (
              <Row key={p.name} plugin={p} tunnels={tunnels}
                   open={open === p.name}
                   onToggle={() => setOpen(open === p.name ? null : p.name)}
                   onChange={load} show={show} />
            ))}
          </div>

          {endpoints && (
            <>
              <h2>Connecting directly</h2>
              <div className="card">
                <div className="card-body">
                  <p className="note">
                    For clients that can reach this machine. ChatGPT uses a
                    tunnel instead.
                  </p>
                  <Copyable value={endpoints.aggregate} label="address" />
                  <div className="section">
                    <CodeBlock>{`Authorization: Bearer YOUR_KEY`}</CodeBlock>
                  </div>
                </div>
              </div>
            </>
          )}
        </>
      )}
    </>
  );
}

function Row({ plugin, tunnels, open, onToggle, onChange, show }: {
  plugin: Plugin;
  tunnels: TunnelInfo | null;
  open: boolean;
  onToggle: () => void;
  onChange: () => void;
  show: (tone: "good" | "problem", text: string) => void;
}) {
  const lookups = plugin.tools.filter((t) => t.kind === "read");
  const changes = plugin.tools.filter((t) => t.kind !== "read");
  const tunnel = tunnels?.tunnels.find((t) => t.plugin === plugin.name);

  return (
    <div className={`expander${open ? " open" : ""}`}>
      <button className="expander-head" onClick={onToggle} aria-expanded={open}>
        <Dot tone={plugin.health === "healthy" ? "good"
          : plugin.health === "degraded" ? "attention" : "problem"} />
        <span className="expander-title">
          <div className="name">
            {plugin.title} <code style={{ fontSize: 12 }}>{plugin.endpoint}</code>
          </div>
          <div className="sub">{plugin.description}</div>
        </span>
        <span className="dim" style={{ fontSize: 13, whiteSpace: "nowrap" }}>
          {tunnel
            ? <Pill tone={tunnel.state === "connected" ? "good" : "attention"}>
                ChatGPT
              </Pill>
            : `${lookups.length + changes.length} tools`}
        </span>
        <span className="chevron" aria-hidden="true">›</span>
      </button>

      {open && (
        <div className="expander-body">
          {plugin.health !== "healthy" && plugin.health_message && (
            <div className="section">
              <Message tone={plugin.health === "degraded" ? "attention" : "problem"}>
                {plugin.health_message}
              </Message>
            </div>
          )}

          <div className="section split two">
            <div>
              <p className="eyebrow">Can look up</p>
              <div className="row wrap">
                {lookups.length === 0
                  ? <span className="note tight">Nothing.</span>
                  : lookups.map((t) => (
                      <Pill key={t.name}>{plain(t.name, plugin.name)}</Pill>
                    ))}
              </div>
            </div>
            <div>
              <p className="eyebrow">Can change, with approval</p>
              <div className="row wrap">
                {changes.length === 0
                  ? <span className="note tight">Nothing.</span>
                  : changes.map((t) => (
                      <Pill key={t.name} tone="attention">
                        {plain(t.name, plugin.name)}
                      </Pill>
                    ))}
              </div>
            </div>
          </div>

          <div className="section">
            <p className="eyebrow">Address</p>
            <Copyable value={plugin.connect_url} label="address" />
          </div>

          <div className="section">
            <p className="eyebrow">ChatGPT</p>
            <TunnelControl plugin={plugin} tunnels={tunnels} tunnel={tunnel}
                           onChange={onChange} show={show} />
          </div>
        </div>
      )}
    </div>
  );
}

/** Make or remove the connector that serves this one plugin. */
function TunnelControl({ plugin, tunnels, tunnel, onChange, show }: {
  plugin: Plugin;
  tunnels: TunnelInfo | null;
  tunnel?: TunnelStatus;
  onChange: () => void;
  show: (tone: "good" | "problem", text: string) => void;
}) {
  const [busy, setBusy] = useState(false);

  async function create() {
    setBusy(true);
    try {
      await api.createTunnel(plugin.title, plugin.name);
      show("good", "Tunnel made.");
    } catch (e) {
      show("problem", e instanceof ApiError ? e.detail : "Couldn't make it.");
    } finally {
      setBusy(false);
      onChange();
    }
  }

  async function remove() {
    if (!confirm(`Delete the ${plugin.name} tunnel? Its connector stops working.`)) return;
    setBusy(true);
    try {
      await api.deleteTunnel(tunnel!.tunnel_id!);
      show("good", "Deleted.");
    } catch (e) {
      show("problem", e instanceof ApiError ? e.detail : "Couldn't delete it.");
    } finally {
      setBusy(false);
      onChange();
    }
  }

  if (tunnel) {
    return (
      <div className="row">
        <Dot tone={tunnel.state === "connected" ? "good"
          : tunnel.state === "failed" ? "problem" : "busy"} />
        <span className="note tight" style={{ flex: 1 }}>
          Its own connector, {describe(tunnel.state)}.
        </span>
        <button className="btn sm danger" disabled={busy} onClick={remove}>
          {busy ? "Working…" : "Remove"}
        </button>
      </div>
    );
  }

  if (!tunnels?.can_manage) {
    return (
      <p className="note tight">
        Add {tunnels?.missing ?? "an OpenAI admin key"} in Settings to give this
        plugin its own connector.
      </p>
    );
  }

  return (
    <div className="row">
      <span className="note tight" style={{ flex: 1 }}>
        Reachable through any connector that covers everything.
      </span>
      <button className="btn sm primary" disabled={busy} onClick={create}>
        {busy ? "Making…" : "Give it its own connector"}
      </button>
    </div>
  );
}

function describe(state: TunnelStatus["state"]): string {
  switch (state) {
    case "connected": return "ready";
    case "starting": return "connecting";
    case "failed": return "not connecting";
    case "stopped": return "switched off";
    default: return "not set up";
  }
}

/** cnmaestro_list_devices → "list devices" */
function plain(tool: string, plugin: string): string {
  return tool.replace(new RegExp(`^${plugin}_`), "").replace(/_/g, " ");
}
