import { useCallback, useState } from "react";
import { api, type Endpoints, type Meta, type Plugin, type TunnelInfo } from "./api";
import { CodeBlock, Copyable, Dot, Message, Out, usePoll } from "./components";

/** The two pages people actually need. Linking them directly is better than
 *  describing where they are and warning about the neighbouring page. */
const OPENAI_TUNNELS = "https://platform.openai.com/settings/organization/tunnels";
const OPENAI_API_KEYS = "https://platform.openai.com/settings/organization/api-keys";

/**
 * Setup.
 *
 * Written for whoever is standing this up. Every step says what it's for, not
 * just what to type, and each one links straight to the page it means.
 */
export function Setup({ meta, plugins }: { meta: Meta | null; plugins: Plugin[] }) {
  const [tab, setTab] = useState<"chatgpt" | "other" | "systems">("chatgpt");
  const [endpoints, setEndpoints] = useState<Endpoints | null>(null);
  const [tunnel, setTunnel] = useState<TunnelInfo | null>(null);

  const load = useCallback(() => {
    api.endpoints().then(setEndpoints).catch(() => setEndpoints(null));
    api.tunnel().then(setTunnel).catch(() => setTunnel(null));
  }, []);
  usePoll(load, 10_000);

  return (
    <>
      <h1>Setup</h1>
      <p className="lede">
        How to connect an assistant, and how to add a system for it to work with.
      </p>

      <div className="segmented" role="tablist">
        <button role="tab" aria-current={tab === "chatgpt" ? "page" : undefined}
                onClick={() => setTab("chatgpt")}>Connect ChatGPT</button>
        <button role="tab" aria-current={tab === "other" ? "page" : undefined}
                onClick={() => setTab("other")}>Connect something else</button>
        <button role="tab" aria-current={tab === "systems" ? "page" : undefined}
                onClick={() => setTab("systems")}>Add a system</button>
      </div>

      {tab === "chatgpt" && <ChatGPT tunnel={tunnel} plugins={plugins} meta={meta} />}
      {tab === "other" && <Direct endpoints={endpoints} />}
      {tab === "systems" && <AddSystem />}
    </>
  );
}

/* ── ChatGPT ────────────────────────────────────────────────────────────── */

function ChatGPT({ tunnel, plugins, meta }: {
  tunnel: TunnelInfo | null;
  plugins: Plugin[];
  meta: Meta | null;
}) {
  const state = tunnel?.status.state ?? "disabled";
  const connected = state === "connected";

  return (
    <>
      <Message tone="info">
        <strong>mcpd doesn't need to be on the internet for this.</strong> ChatGPT
        reaches it through a private connection made outward from here, so you
        never open a port on your router. It's built in — nothing extra to install.
      </Message>

      <div className="card">
        <div className="card-body">
          <Step n={1} title="Create a tunnel">
            <p>
              Open <Out href={OPENAI_TUNNELS}>Tunnels in your OpenAI account</Out>{" "}
              and create one. Copy the ID it gives you.
            </p>
          </Step>

          <Step n={2} title="Create a key">
            <p>
              Open <Out href={OPENAI_API_KEYS}>API keys</Out> and create one with
              the <strong>Tunnels: Read and Use</strong> permission.
            </p>
            <p className="note">
              That's the page you want — keys made anywhere else in your OpenAI
              settings can't run a tunnel.
            </p>
          </Step>

          <Step n={3} title="Paste them into Settings">
            <p>
              Go to <strong>Settings</strong>, switch the ChatGPT connection on,
              and paste in both. Pick which systems it's allowed to reach while
              you're there.
            </p>
            <p>Save, and it connects straight away.</p>
            {tunnel && (
              <p className="row" style={{ marginTop: "var(--s3)" }}>
                <Dot tone={connected ? "good" : state === "failed" ? "problem" : state === "starting" ? "busy" : ""} />
                <span className="note tight">
                  Right now: <strong>{statusWord(state)}</strong>
                </span>
              </p>
            )}
          </Step>

          <Step n={4} title="Point ChatGPT at it">
            <p>
              In ChatGPT, go to <strong>Plugins</strong>, create a developer-mode
              app, and choose <strong>Tunnel</strong>. Yours will be in the list.
            </p>
            <p className="note tight">No address to copy, no key to paste.</p>
          </Step>

          {meta?.auth_mode !== "static" && (
            <Step n={5} title="Sign in when it asks">
              <p>
                ChatGPT will send you here to sign in and confirm what it may do.
                That's how mcpd knows who's asking — everyone using the connector
                gets their own sign-in rather than sharing one.
              </p>
              <p className="note tight">
                You'll approve this once. After that it stays connected.
              </p>
            </Step>
          )}
        </div>
      </div>

      <h2>If ChatGPT can't see your tunnel</h2>
      <div className="card">
        <div className="card-body">
          <dl className="kv">
            <div>
              <dt>It isn't connected</dt>
              <dd>
                A tunnel only shows up in ChatGPT while something is actually
                serving it. Check it's green above — this is nearly always the reason.
              </dd>
            </div>
            <div>
              <dt>Wrong kind of key</dt>
              <dd>
                Make it on <Out href={OPENAI_API_KEYS}>the API keys page</Out>,
                with Tunnels: Read and Use. Keys from elsewhere look right and
                won't work.
              </dd>
            </div>
            <div>
              <dt>It says mcpd doesn't do sign-ins</dt>
              <dd>
                ChatGPT checks for a sign-in method before it will create the
                connector at all. mcpd offers one whenever it isn't set to a
                single shared key — check <strong>How people sign in</strong> at
                the bottom of Settings.
              </dd>
            </div>
            <div>
              <dt>Different accounts</dt>
              <dd>
                Tunnel access and ChatGPT developer mode are granted separately.
                Check the tunnel is in the same workspace as the ChatGPT account
                you're signed into.
              </dd>
            </div>
          </dl>
        </div>
      </div>

      {meta?.auth_mode === "static" && (
        <Message tone="attention">
          <strong>Everyone shares one sign-in right now.</strong> That means mcpd
          can't tell two people apart, so it can't require that someone other than
          the suggester approves a change. It refuses those rather than pretending.
        </Message>
      )}

      {plugins.length === 0 && (
        <Message tone="attention">
          There's nothing connected for ChatGPT to work with yet — see "Add a system".
        </Message>
      )}
    </>
  );
}

function statusWord(state: string): string {
  switch (state) {
    case "connected": return "connected, and ChatGPT should see it";
    case "starting": return "connecting";
    case "failed": return "not connecting — see Connections for why";
    case "stopped": return "set up but switched off";
    default: return "not set up yet";
  }
}

/* ── direct ─────────────────────────────────────────────────────────────── */

function Direct({ endpoints }: { endpoints: Endpoints | null }) {
  const address = endpoints?.aggregate ?? "http://your-mcpd/mcp";

  return (
    <>
      <p className="lede">
        Anything that speaks the Model Context Protocol can connect directly.
        Give it an address and a key.
      </p>

      <div className="card">
        <div className="card-body">
          <Step n={1} title="Take the address">
            <p>
              This one covers everything your key is allowed to reach. Each system
              also has its own, on the Connections page, if you'd rather keep them apart.
            </p>
            <Copyable value={address} label="address" />
          </Step>

          <Step n={2} title="Send your key with every request">
            <p>As a header:</p>
            <CodeBlock>{`Authorization: Bearer YOUR_KEY`}</CodeBlock>
            <p className="note tight">
              The same key you signed in here with — it's in your <code>.env</code> file.
            </p>
          </Step>

          <Step n={3} title="Check it works">
            <CodeBlock>{`curl -X POST ${address} \\
  -H "Authorization: Bearer YOUR_KEY" \\
  -H "Accept: application/json, text/event-stream" \\
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}'`}</CodeBlock>
            <p className="note tight">
              You should get back a list of everything it can do.
            </p>
          </Step>
        </div>
      </div>

      <Message tone="attention">
        <strong>Keep this address private.</strong> Anyone who can reach it and
        has a key can use it. Put it behind HTTPS before it's reachable from
        anywhere you don't control.
      </Message>
    </>
  );
}

/* ── systems ────────────────────────────────────────────────────────────── */

function AddSystem() {
  return (
    <>
      <p className="lede">
        A system is anything mcpd can work with — a network controller, a
        server, a DNS provider.
      </p>

      <div className="card">
        <div className="card-body">
          <Step n={1} title="Switch one on">
            <p>Some come built in. In your startup file, set the one you want to on:</p>
            <CodeBlock>{`plugins:
  cnmaestro:
    enabled: true
    settings:
      base_url: "https://cloud.cambiumnetworks.com"
      client_id_ref: env:MY_CLIENT_ID
      client_secret_ref: env:MY_CLIENT_SECRET`}</CodeBlock>
          </Step>

          <Step n={2} title="Keep the passwords out of it">
            <p>
              Notice those say <code>env:MY_CLIENT_ID</code> rather than the real
              value. mcpd never wants passwords written into that file, because
              it tends to end up in backups and version control.
            </p>
            <p>Put the real ones in a <code>.env</code> file next to it:</p>
            <CodeBlock>{`MY_CLIENT_ID=abc123
MY_CLIENT_SECRET=shhh`}</CodeBlock>
            <p className="note tight">
              Keep that file to yourself — <code>chmod 600 .env</code>.
            </p>
          </Step>

          <Step n={3} title="Restart">
            <p>
              The new system appears on Connections. If a setting's wrong, mcpd
              says so as it starts rather than failing later.
            </p>
          </Step>
        </div>
      </div>

      <Message tone="info">
        <strong>Building your own?</strong> Write it in Go and drop it in the
        plugins folder — no rebuilding mcpd. There's a complete one in a single
        file at <code>examples/echo</code> in the source.
      </Message>
    </>
  );
}

function Step({ n, title, children }: { n: number; title: string; children: React.ReactNode }) {
  return (
    <div className="step">
      <div className="step-n" aria-hidden="true">{n}</div>
      <div className="step-body">
        <h3>{title}</h3>
        {children}
      </div>
    </div>
  );
}
