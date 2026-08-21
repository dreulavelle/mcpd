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
      <p className="lede">
        What mcpd can work with. Each one brings its own set of things an
        assistant can look up or change.
      </p>

      {error && <Message tone="problem">{error}</Message>}

      {!plugins ? (
        <Skeleton rows={3} />
      ) : plugins.length === 0 ? (
        <Empty mark="○" title="No systems yet">
          Add one to your startup file and restart. The echo system is a good
          first one — it touches nothing, so it only checks that everything
          works.
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

          <h2>Connecting something directly</h2>
          <div className="card">
            <div className="card-body">
              <p className="note">
                For anything that can already reach this machine. ChatGPT can't
                — it comes in through a tunnel instead, which you set up on the
                Tunnels page.
              </p>

              {endpoints && (
                <>
                  <div className="section">
                    <p className="eyebrow">Everything at once</p>
                    <Copyable value={endpoints.aggregate} label="address" />
                    <p className="note" style={{ marginTop: "var(--s2)" }}>
                      Gives an assistant everything its key allows. Each system
                      also has its own address, shown in its row above.
                    </p>
                  </div>

                  <div className="section">
                    <p className="eyebrow">Send your key with every request</p>
                    <CodeBlock>{`Authorization: Bearer YOUR_KEY`}</CodeBlock>
                    <p className="note tight">
                      The same key you signed in here with — it's in your{" "}
                      <code>.env</code> file.
                    </p>
                  </div>
                </>
              )}
            </div>
          </div>
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
              <p className="note">Happens straight away. Nothing changes.</p>
              <div className="row wrap">
                {lookups.length === 0
                  ? <span className="note tight">Nothing.</span>
                  : lookups.map((t) => (
                      <Pill key={t.name}>{plain(t.name, plugin.name)}</Pill>
                    ))}
              </div>
            </div>
            <div>
              <p className="eyebrow">Can suggest</p>
              <p className="note">Needs someone to say yes before it happens.</p>
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
            <p className="note">
              For something connecting directly with a key that's been given
              access to this system.
            </p>
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
