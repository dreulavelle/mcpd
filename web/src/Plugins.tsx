import { useEffect, useState } from "react";
import { api, type Endpoints, type Plugin } from "./api";
import { Banner } from "./components";

/**
 * Connections page.
 *
 * Each integration collapses to a single row showing the only two things that
 * matter at a glance: whether it is working, and what it is. Everything else --
 * the address to connect to, what it can do, how it is configured -- opens on
 * demand, because it is what you need while setting one up and noise
 * afterwards.
 */
export function Plugins() {
  const [plugins, setPlugins] = useState<Plugin[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [open, setOpen] = useState<string | null>(null);
  const [endpoints, setEndpoints] = useState<Endpoints | null>(null);

  useEffect(() => {
    api.endpoints().then(setEndpoints).catch(() => setEndpoints(null));
  }, []);

  useEffect(() => {
    const load = () =>
      api
        .plugins()
        .then((r) => {
          setPlugins(r.plugins ?? []);
          setError("");
        })
        .catch((e) => setError(e instanceof Error ? e.message : "Could not load connections."))
        .finally(() => setLoading(false));

    load();
    const timer = setInterval(load, 15_000);
    return () => clearInterval(timer);
  }, []);

  if (loading) return <p className="empty">Loading…</p>;

  return (
    <>
      <h1>Connections</h1>
      <p className="subtitle">
        The systems mcpd can talk to. Each one has its own address, so you can
        give an assistant access to one without giving it access to the others.
      </p>

      {error && <Banner tone="error">{error}</Banner>}

      {endpoints && plugins.length > 0 && (
        <div className="card all-at-once">
          <div className="card-body">
            <h3>Everything at once</h3>
            <p className="hint">
              This one address gives an assistant everything its token is
              allowed to reach. Use it when whatever you're connecting can only
              point at a single address — ChatGPT's tunnel works this way.
            </p>
            <Copyable value={endpoints.aggregate} />
            <p className="hint" style={{ marginTop: 10, marginBottom: 0 }}>
              Prefer one system at a time? Every connection below has its own
              address. Pair it with a token limited to that system and an
              assistant can't reach anything else, even by accident.
            </p>
          </div>
        </div>
      )}

      {plugins.length === 0 && !error && (
        <div className="card">
          <p className="empty">
            No connections are turned on yet. See the Setup tab to add one.
          </p>
        </div>
      )}

      <div className="stack">
        {plugins.map((p) => (
          <PluginRow
            key={p.name}
            plugin={p}
            expanded={open === p.name}
            onToggle={() => setOpen(open === p.name ? null : p.name)}
          />
        ))}
      </div>
    </>
  );
}

function PluginRow({
  plugin,
  expanded,
  onToggle,
}: {
  plugin: Plugin;
  expanded: boolean;
  onToggle: () => void;
}) {
  const reads = plugin.tools.filter((t) => t.kind === "read");
  const proposes = plugin.tools.filter((t) => t.kind === "propose");

  return (
    <div className={`row-card${expanded ? " open" : ""}`}>
      <button className="row-head" onClick={onToggle} aria-expanded={expanded}>
        <span className={`dot ${healthTone(plugin.health)}`} aria-hidden="true" />
        <span className="row-title">
          {plugin.title}
          <span className="row-sub">{plugin.description}</span>
        </span>
        <span className="row-meta">
          {reads.length} {reads.length === 1 ? "lookup" : "lookups"}
          {proposes.length > 0 && ` · ${proposes.length} change${proposes.length === 1 ? "" : "s"}`}
        </span>
        <span className="chev" aria-hidden="true">
          {expanded ? "▾" : "▸"}
        </span>
      </button>

      {expanded && (
        <div className="row-body">
          {plugin.health !== "healthy" && (
            <Banner tone={plugin.health === "degraded" ? "warn" : "error"}>
              {plugin.health_message ||
                (plugin.health === "degraded"
                  ? "This connection is having trouble reaching the system it manages."
                  : "This connection is not working.")}
            </Banner>
          )}

          <Section title="Address">
            <p className="hint">
              Paste this into ChatGPT, or point a tunnel at it. It only works
              with a token that has been given access to this connection.
            </p>
            <Copyable value={plugin.connect_url} />
          </Section>

          <Section title="What it can do">
            <div className="ability">
              <div>
                <span className="ability-label read">Look things up</span>
                <p className="hint">Happens straight away. Nothing is changed.</p>
                <ul className="tool-list">
                  {reads.map((t) => (
                    <li key={t.name}>{friendly(t.name, plugin.name)}</li>
                  ))}
                  {reads.length === 0 && <li className="muted">Nothing</li>}
                </ul>
              </div>
              <div>
                <span className="ability-label propose">Suggest changes</span>
                <p className="hint">
                  Waits for someone to approve it. Nothing happens until you say
                  yes.
                </p>
                <ul className="tool-list">
                  {proposes.map((t) => (
                    <li key={t.name}>{friendly(t.name, plugin.name)}</li>
                  ))}
                  {proposes.length === 0 && <li className="muted">Nothing</li>}
                </ul>
              </div>
            </div>
          </Section>

          {plugin.settings.length > 0 && (
            <Section title="Settings">
              <p className="hint">
                These come from your config file. Passwords and keys are never
                shown here — you'll see where they're read from instead.
              </p>
              <dl className="settings">
                {plugin.settings.map((s) => (
                  <div className="setting" key={s.key}>
                    <dt>{s.key.replace(/_/g, " ")}</dt>
                    <dd>
                      <code>{s.value}</code>
                      {s.secret && <span className="pill">hidden</span>}
                    </dd>
                  </div>
                ))}
              </dl>
            </Section>
          )}

          <Section title="Details">
            <dl className="settings">
              <div className="setting">
                <dt>version</dt>
                <dd><code>{plugin.version}</code></dd>
              </div>
              <div className="setting">
                <dt>status</dt>
                <dd><code>{plugin.health}</code></dd>
              </div>
              {plugin.required && (
                <div className="setting">
                  <dt>required</dt>
                  <dd>
                    <code>yes</code>
                    <span className="hint inline">
                      mcpd will not start if this connection fails
                    </span>
                  </dd>
                </div>
              )}
            </dl>
          </Section>
        </div>
      )}
    </div>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="detail-section">
      <h3>{title}</h3>
      {children}
    </section>
  );
}

function Copyable({ value }: { value: string }) {
  const [copied, setCopied] = useState(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      // Clipboard access is refused outside a secure context, which is the
      // normal case on a plain-http LAN address. The value is selectable, so
      // there is still a way through.
      setCopied(false);
    }
  }

  return (
    <div className="copyable">
      <code>{value}</code>
      <button className="btn small" onClick={copy}>
        {copied ? "Copied" : "Copy"}
      </button>
    </div>
  );
}

/** Turns cnmaestro_list_devices into "list devices". */
function friendly(toolName: string, plugin: string): string {
  return toolName.replace(new RegExp(`^${plugin}_`), "").replace(/_/g, " ");
}

function healthTone(h: Plugin["health"]): string {
  if (h === "healthy") return "up";
  if (h === "degraded") return "warn";
  return "down";
}
