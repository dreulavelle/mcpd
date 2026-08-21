import { useCallback, useState } from "react";
import {
  api, ApiError, type Endpoints, type Plugin, type SettingsPayload,
  type TunnelInfo, type TunnelStatus,
} from "./api";
import {
  CodeBlock, Copyable, Dot, Empty, Message, Pill, Skeleton, useIsAdmin, usePoll,
  useToasts, type Notify,
} from "./components";
import { SettingsForm } from "./SettingsForm";

/**
 * Plugins.
 *
 * One card per configured instance, grouped by what it is an instance of. The
 * grouping only shows when there is more than one of something, because a
 * heading over a single card is a heading that says nothing.
 *
 * Everything an instance needs is on its own card: what it can do, where to
 * reach it, whether ChatGPT can, and the settings it runs on. Sending someone
 * to a general settings page to configure one integration is the arrangement
 * this replaces.
 */
export function Plugins() {
  const [plugins, setPlugins] = useState<Plugin[] | null>(null);
  const [endpoints, setEndpoints] = useState<Endpoints | null>(null);
  const [tunnels, setTunnels] = useState<TunnelInfo | null>(null);
  const [settings, setSettings] = useState<SettingsPayload | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  const [error, setError] = useState("");
  const { show, view } = useToasts();

  const load = useCallback(() => {
    api.plugins()
      .then((r) => { setPlugins(r.plugins ?? []); setError(""); })
      .catch(() => setError("Couldn't load plugins."));
    api.endpoints().then(setEndpoints).catch(() => setEndpoints(null));
    api.tunnel().then(setTunnels).catch(() => setTunnels(null));
    api.settings().then(setSettings).catch(() => setSettings(null));
  }, []);
  usePoll(load, 15_000);

  return (
    <>
      {view}
      <h1>Plugins</h1>
      <p className="lede">What mcpd can work with, and what each one is set up to reach.</p>

      {error && <Message tone="problem">{error}</Message>}

      {!plugins ? (
        <Skeleton rows={3} />
      ) : plugins.length === 0 ? (
        <Empty mark="○" title="No plugins yet">
          Enable one in your startup file and restart.
        </Empty>
      ) : (
        <>
          {groupByType(plugins).map(({ type, instances }) => (
            <section key={type} className="plugin-group">
              {instances.length > 1 && (
                <h2 className="type-heading">
                  {instances[0]!.title}
                  <span className="note tight">{instances.length} instances</span>
                </h2>
              )}
              <div className="stack">
                {instances.map((p) => (
                  <PluginCard
                    key={p.name} plugin={p} tunnels={tunnels} settings={settings}
                    open={open === p.name}
                    onToggle={() => setOpen(open === p.name ? null : p.name)}
                    onChange={load} show={show}
                  />
                ))}
              </div>
            </section>
          ))}

          {endpoints && (
            <section className="plugin-group">
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
            </section>
          )}
        </>
      )}
    </>
  );
}

/** Instances of one integration, kept together and in a stable order. */
function groupByType(plugins: Plugin[]): { type: string; instances: Plugin[] }[] {
  const byType = new Map<string, Plugin[]>();
  for (const p of plugins) {
    const list = byType.get(p.type) ?? [];
    list.push(p);
    byType.set(p.type, list);
  }
  return [...byType.entries()]
    .map(([type, instances]) => ({
      type,
      instances: [...instances].sort((a, b) => a.name.localeCompare(b.name)),
    }))
    .sort((a, b) => a.type.localeCompare(b.type));
}

function PluginCard({ plugin, tunnels, settings, open, onToggle, onChange, show }: {
  plugin: Plugin;
  tunnels: TunnelInfo | null;
  settings: SettingsPayload | null;
  open: boolean;
  onToggle: () => void;
  onChange: () => void;
  show: Notify;
}) {
  const reads = plugin.tools.filter((t) => t.kind === "read");
  const writes = plugin.tools.filter((t) => t.kind !== "read");
  const tunnel = tunnels?.tunnels.find((t) => t.plugin === plugin.name);
  const group = settings?.groups.find((g) => g.name === plugin.settings_group);

  const tone = plugin.health === "healthy" ? "good"
    : plugin.health === "degraded" ? "attention" : "problem";

  return (
    <div className={`expander${open ? " open" : ""}`}>
      <button className="expander-head" onClick={onToggle} aria-expanded={open}>
        <Dot tone={tone} />
        <span className="expander-title">
          <div className="name">
            {/* The instance name, not the title: with two of something the
                title is identical on both and the name is the difference. */}
            {plugin.name}
            {plugin.name !== plugin.type && (
              <span className="note tight type-tag">{plugin.title}</span>
            )}
          </div>
          <div className="sub">{plugin.description}</div>
        </span>

        {/* Counts and connector state together. Showing one or the other meant
            a plugin with a connector stopped saying what it could do. */}
        <span className="plugin-facts">
          <span className="note tight">{reads.length} read</span>
          {writes.length > 0 && (
            <span className="note tight">{writes.length} write</span>
          )}
          {tunnel && (
            <Pill tone={tunnel.state === "connected" ? "good" : "attention"}>
              ChatGPT
            </Pill>
          )}
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

          {/* Settings first. It is why someone opens the card of a plugin that
              is not working, and everything below it is reference. */}
          {group && settings && (
            <div className="section">
              <p className="eyebrow">Settings</p>
              <SettingsForm
                groups={[group]} settings={settings}
                onSaved={onChange} show={show}
              />
            </div>
          )}

          <div className="section split two">
            <div>
              <p className="eyebrow">Read</p>
              <ToolList tools={reads.map((t) => plain(t.name, plugin.name))} />
            </div>
            <div>
              <p className="eyebrow">Write (Approval Required)</p>
              <ToolList tone="attention"
                        tools={writes.map((t) => plain(t.name, plugin.name))} />
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

function ToolList({ tools, tone }: { tools: string[]; tone?: "attention" }) {
  if (tools.length === 0) return <span className="note tight">Nothing.</span>;
  return (
    <div className="row wrap">
      {tools.map((t) => <Pill key={t} tone={tone}>{t}</Pill>)}
    </div>
  );
}

/** Make or remove the connector that serves this one plugin. */
function TunnelControl({ plugin, tunnels, tunnel, onChange, show }: {
  plugin: Plugin;
  tunnels: TunnelInfo | null;
  tunnel?: TunnelStatus;
  onChange: () => void;
  show: Notify;
}) {
  const [busy, setBusy] = useState(false);
  const admin = useIsAdmin();

  async function create() {
    setBusy(true);
    try {
      // Same default as the Tunnels page: a tunnel scoped only to the
      // organisation is invisible in an Enterprise or Edu workspace.
      await api.createTunnel(plugin.title, plugin.name, tunnels?.workspaces?.[0]);
      show("good", "Made. Give it about 30 seconds to become active in ChatGPT.");
    } catch (e) {
      show("problem", e instanceof ApiError ? e.detail : "Couldn't make it.");
    } finally {
      setBusy(false);
      onChange();
    }
  }

  async function remove() {
    if (!confirm("Remove this connector? Anything using it stops working.")) return;
    setBusy(true);
    try {
      await api.deleteTunnel(tunnel!.tunnel_id!);
      show("good", "Removed.");
    } catch (e) {
      show("problem", e instanceof ApiError ? e.detail : "Couldn't remove it.");
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
        <span className="note tight grow">
          Its own connector, {describe(tunnel.state)}.
        </span>
        {admin && (
          <button className="btn sm danger" disabled={busy} onClick={remove}>
            {busy ? "Working…" : "Remove"}
          </button>
        )}
      </div>
    );
  }

  // A user sees how the plugin is reached and cannot change it. Offering the
  // button and refusing the call would be a worse way to say the same thing.
  if (!admin) {
    return (
      <p className="note tight">
        Reachable through any connector that covers everything.
      </p>
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
      <span className="note tight grow">
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
    default: return String(state);
  }
}

/** Tool names carry their plugin prefix; the card already says which plugin. */
function plain(tool: string, plugin: string): string {
  return tool.startsWith(plugin + "_") ? tool.slice(plugin.length + 1) : tool;
}
