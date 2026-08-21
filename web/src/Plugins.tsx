import { useCallback, useState } from "react";
import { api, type Endpoints, type Plugin } from "./api";
import {
  CodeBlock, Copyable, Dot, Empty, Message, Pill, Skeleton, usePoll,
} from "./components";

/**
 * The systems mcpd can work with.
 *
 * One row each, collapsed to the two things worth knowing at a glance: whether
 * it is healthy, and whether it can change anything. Opening a row shows what
 * it can actually do and where to point something at it.
 */
export function Plugins() {
  const [plugins, setPlugins] = useState<Plugin[] | null>(null);
  const [endpoints, setEndpoints] = useState<Endpoints | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  const [error, setError] = useState("");

  const load = useCallback(() => {
    api.plugins()
      .then((r) => { setPlugins(r.plugins ?? []); setError(""); })
      .catch(() => setError("Couldn't load your systems. Is mcpd still running?"));
    api.endpoints().then(setEndpoints).catch(() => setEndpoints(null));
  }, []);
  usePoll(load, 15_000);

  return (
    <>
      <h1>Systems</h1>
      <p className="lede">What mcpd can work with.</p>

      {error && <Message tone="problem">{error}</Message>}

      {!plugins ? (
        <Skeleton rows={3} />
      ) : plugins.length === 0 ? (
        <Empty mark="○" title="No systems yet">
          Add one to your startup file and restart.
        </Empty>
      ) : (
        <>
          <div className="stack">
            {plugins.map((p) => (
              <Row key={p.name} plugin={p}
                   open={open === p.name}
                   onToggle={() => setOpen(open === p.name ? null : p.name)} />
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

function Row({ plugin, open, onToggle }: {
  plugin: Plugin;
  open: boolean;
  onToggle: () => void;
}) {
  const lookups = plugin.tools.filter((t) => t.kind === "read");
  const changes = plugin.tools.filter((t) => t.kind !== "read");

  return (
    <div className={`expander${open ? " open" : ""}`}>
      <button className="expander-head" onClick={onToggle} aria-expanded={open}>
        <Dot tone={plugin.health === "healthy" ? "good"
          : plugin.health === "degraded" ? "attention" : "problem"} />
        <span className="expander-title">
          <div className="name">{plugin.title}</div>
          <div className="sub">{plugin.description}</div>
        </span>
        <span className="dim" style={{ fontSize: 13, whiteSpace: "nowrap" }}>
          {lookups.length} {lookups.length === 1 ? "lookup" : "lookups"}
          {changes.length > 0 &&
            ` · ${changes.length} ${changes.length === 1 ? "change" : "changes"}`}
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
            <p className="eyebrow">Its own address</p>
            <Copyable value={plugin.connect_url} label="address" />
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
