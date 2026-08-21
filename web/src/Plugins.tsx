import { useCallback, useState } from "react";
import {
  api, ApiError, type Endpoints, type Plugin, type PluginInstance,
  type PluginType, type SettingsPayload, type TunnelInfo, type TunnelStatus,
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
  const [types, setTypes] = useState<PluginType[]>([]);
  const [instances, setInstances] = useState<PluginInstance[]>([]);
  const [open, setOpen] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);
  const [error, setError] = useState("");
  const { show, view } = useToasts();
  const admin = useIsAdmin();

  const load = useCallback(() => {
    api.plugins()
      .then((r) => { setPlugins(r.plugins ?? []); setError(""); })
      .catch(() => setError("Couldn't load plugins."));
    api.endpoints().then(setEndpoints).catch(() => setEndpoints(null));
    api.tunnel().then(setTunnels).catch(() => setTunnels(null));
    api.settings().then(setSettings).catch(() => setSettings(null));
    api.pluginTypes().then((r) => setTypes(r.types ?? [])).catch(() => setTypes([]));
    api.instances().then((r) => setInstances(r.instances ?? [])).catch(() => setInstances([]));
  }, []);
  usePoll(load, 15_000);

  return (
    <>
      {view}
      <div className="row">
        <h1 className="grow">Plugins</h1>
        {admin && plugins && (
          <button className="btn primary" onClick={() => setAdding(true)}>Add plugin</button>
        )}
      </div>
      <p className="lede">What mcpd can work with, and what each one is set up to reach.</p>

      {error && <Message tone="problem">{error}</Message>}

      {/* An instance added since the last start is configured and not serving.
          Saying so is the difference between "waiting" and "broken". */}
      <PendingNotice instances={instances} />

      {adding && (
        <AddPlugin
          types={types} onClose={() => setAdding(false)}
          onAdded={(name) => {
            setAdding(false);
            load();
            setOpen(name);
            show("good", `Added ${name}. Fill in its settings and it starts.`);
          }}
        />
      )}

      {!plugins ? (
        <Skeleton rows={3} />
      ) : plugins.length === 0 ? (
        <Empty mark="○" title="No plugins yet">
          Enable one in your startup file and restart.
        </Empty>
      ) : (
        <>
          {groupByType(withUnmounted(plugins, instances, types)).map(({ type, members }) => (
            <section key={type} className="plugin-group">
              {members.length > 1 && (
                <h2 className="type-heading">
                  {members[0]!.title}
                  <span className="note tight">{members.length} instances</span>
                </h2>
              )}
              <div className="stack">
                {members.map((p) => (
                  <PluginCard
                    key={p.name} plugin={p} tunnels={tunnels} settings={settings}
                    instances={instances}
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

/**
 * Every configured instance, mounted or not.
 *
 * A plugin that has not been configured is not mounted, so it is absent from
 * the plugins endpoint -- and a card is exactly where its settings form lives.
 * Without this, adding an integration produced a notice saying what it needed
 * and nowhere to type it.
 *
 * The stand-in carries what is knowable before a plugin exists: its type's
 * title and description, and the name of its settings group. Everything else
 * is empty, which is the truth about something that is not running.
 */
function withUnmounted(
  plugins: Plugin[], instances: PluginInstance[], types: PluginType[],
): Plugin[] {
  const mounted = new Set(plugins.map((p) => p.name));
  const pending = instances
    .filter((i) => !mounted.has(i.name))
    .map((i): Plugin => {
      const t = types.find((candidate) => candidate.name === i.type);
      return {
        name: i.name,
        type: i.type,
        version: "",
        title: t?.title ?? i.type,
        description: t?.description ?? "",
        endpoint: "",
        connect_url: "",
        health: "unhealthy",
        health_message: i.missing?.length
          ? `Waiting on ${i.missing.join(", ")}.`
          : i.problem || (i.enabled ? "Not running yet." : "Switched off."),
        tools: [],
        mutations: [],
        required: false,
        settings_group: `plugin:${i.name}`,
        settings: [],
      };
    });
  return [...plugins, ...pending];
}

/**
 * Instances waiting on their settings.
 *
 * A plugin is not mounted until every required setting has a value, and starts
 * serving on its own the moment the last one is filled in. Naming what is
 * missing is the difference between "not finished" and "broken".
 */
function PendingNotice({ instances }: { instances: PluginInstance[] }) {
  const waiting = instances.filter((i) => i.enabled && !i.mounted);
  if (waiting.length === 0) return null;
  return (
    <Message tone="attention">
      <span>
        {waiting.map((i) => (
          <span key={i.name} className="pending-row">
            <strong>{i.name}</strong>
            {i.missing?.length
              ? ` needs ${i.missing.join(", ")}.`
              : i.problem
                ? ` ${i.problem}`
                : " is not running yet."}
          </span>
        ))}
        {" "}It starts serving as soon as it has what it needs — nothing to restart.
      </span>
    </Message>
  );
}

function AddPlugin({ types, onClose, onAdded }: {
  types: PluginType[];
  onClose: () => void;
  onAdded: (name: string) => void;
}) {
  const [type, setType] = useState(types[0]?.name ?? "");
  const [name, setName] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  // Named after its type by default, which is right until there are two.
  const effective = name.trim() || type;

  async function add() {
    setBusy(true);
    setError("");
    try {
      await api.addInstance(effective, type);
      onAdded(effective);
    } catch (e) {
      setError(e instanceof ApiError ? e.detail : "Couldn't add it.");
    } finally {
      setBusy(false);
    }
  }

  if (types.length === 0) {
    return (
      <Message tone="attention">
        This build has no integrations compiled in.
      </Message>
    );
  }

  return (
    <div className="card">
      <div className="card-head"><h3>Add a plugin</h3></div>
      <div className="card-body">
        {error && <Message tone="problem">{error}</Message>}

        <div className="field">
          <label htmlFor="ptype">Integration</label>
          <select id="ptype" value={type} onChange={(e) => setType(e.target.value)}>
            {types.map((t) => <option key={t.name} value={t.name}>{t.title}</option>)}
          </select>
          <p className="note">
            {types.find((t) => t.name === type)?.description}
          </p>
        </div>

        <div className="field">
          <label htmlFor="pname">Name (optional)</label>
          <input id="pname" type="text" value={name} placeholder={type}
                 onChange={(e) => setName(e.target.value)} />
          <p className="note">
            Its endpoint, its tool prefix, and what the history calls it. Name
            it only when you have more than one of the same integration —
            <code>nas-primary</code> and <code>nas-backup</code> rather than
            two things both called {type}.
          </p>
        </div>

        <div className="row">
          <button className="btn primary" disabled={busy || !type} onClick={add}>
            {busy ? "Adding…" : "Add"}
          </button>
          <button className="btn quiet" type="button" onClick={onClose}>Cancel</button>
        </div>
      </div>
    </div>
  );
}

/** Instances of one integration, kept together and in a stable order. */
function groupByType(plugins: Plugin[]): { type: string; members: Plugin[] }[] {
  const byType = new Map<string, Plugin[]>();
  for (const p of plugins) {
    const list = byType.get(p.type) ?? [];
    list.push(p);
    byType.set(p.type, list);
  }
  return [...byType.entries()]
    .map(([type, members]) => ({
      type,
      members: [...members].sort((a, b) => a.name.localeCompare(b.name)),
    }))
    .sort((a, b) => a.type.localeCompare(b.type));
}

function PluginCard({ plugin, tunnels, settings, instances, open, onToggle, onChange, show }: {
  plugin: Plugin;
  tunnels: TunnelInfo | null;
  settings: SettingsPayload | null;
  instances: PluginInstance[];
  open: boolean;
  onToggle: () => void;
  onChange: () => void;
  show: Notify;
}) {
  const admin = useIsAdmin();
  // An instance that is not mounted has no endpoint, no tools and nothing to
  // connect to. Its card exists for one reason -- the settings form -- and
  // showing "0 read" beside an address that goes nowhere invites the reading
  // that the integration is broken rather than unfinished.
  const running = plugin.endpoint !== "";
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
          {running ? (
            <>
              <span className="note tight">{reads.length} read</span>
              {writes.length > 0 && (
                <span className="note tight">{writes.length} write</span>
              )}
            </>
          ) : (
            <Pill tone="attention">Not running</Pill>
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
                onSaved={onChange} show={show} readOnly={!admin}
              />
            </div>
          )}

          {running && (
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
          )}

          {running && (
            <>
              <div className="section">
                <p className="eyebrow">Address</p>
                <Copyable value={plugin.connect_url} label="address" />
              </div>

              <div className="section">
                <p className="eyebrow">ChatGPT</p>
                <TunnelControl plugin={plugin} tunnels={tunnels} tunnel={tunnel}
                               onChange={onChange} show={show} />
              </div>
            </>
          )}

          <RemoveControl plugin={plugin} instances={instances}
                         onChange={onChange} show={show} />
        </div>
      )}
    </div>
  );
}

/** Removing an instance, where it can be done and where it cannot. */
function RemoveControl({ plugin, instances, onChange, show }: {
  plugin: Plugin;
  instances: PluginInstance[];
  onChange: () => void;
  show: Notify;
}) {
  const admin = useIsAdmin();
  const [busy, setBusy] = useState(false);
  const inst = instances.find((i) => i.name === plugin.name);

  if (!admin || !inst) return null;

  // An instance from the file would come back on the next start, so offering
  // to remove it here would be offering something that does not stick.
  if (inst.from_file) {
    return (
      <div className="section">
        <p className="note tight">
          Defined in the configuration file. Remove it there rather than here,
          or it returns on the next start.
        </p>
      </div>
    );
  }

  async function remove() {
    if (!confirm(`Remove ${plugin.name}? Its settings, including credentials, go with it.`)) return;
    setBusy(true);
    try {
      await api.removeInstance(plugin.name);
      show("good", `Removed ${plugin.name}.`);
    } catch (e) {
      show("problem", e instanceof ApiError ? e.detail : "Couldn't remove it.");
    } finally {
      setBusy(false);
      onChange();
    }
  }

  return (
    <div className="section row">
      <span className="note tight grow">
        Removing this forgets its settings, including any credentials.
      </span>
      <button className="btn sm danger" disabled={busy} onClick={remove}>
        {busy ? "Removing…" : "Remove"}
      </button>
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
