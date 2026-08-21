import { useEffect, useState } from "react";
import { api, type Endpoints, type Meta, type Plugin, type TunnelInfo } from "./api";

/**
 * Setup guide.
 *
 * Written for whoever is standing this up, not for whoever built it. No
 * protocol names, no acronyms unless they appear on a screen the reader will
 * actually see, and every step says what it is for rather than only what to
 * type.
 */
export function Setup({ meta, plugins }: { meta: Meta | null; plugins: Plugin[] }) {
  const [tab, setTab] = useState<"chatgpt" | "other" | "adding">("chatgpt");
  const [endpoints, setEndpoints] = useState<Endpoints | null>(null);
  const [tunnel, setTunnel] = useState<TunnelInfo | null>(null);

  useEffect(() => {
    api.endpoints().then(setEndpoints).catch(() => setEndpoints(null));
    api.tunnel().then(setTunnel).catch(() => setTunnel(null));
  }, []);

  return (
    <>
      <h1>Setup</h1>
      <p className="subtitle">
        How to connect an assistant to mcpd, and how to add a new system for it
        to manage.
      </p>

      <div className="segmented">
        <button onClick={() => setTab("chatgpt")} aria-current={tab === "chatgpt" ? "page" : undefined}>
          Connect ChatGPT
        </button>
        <button onClick={() => setTab("other")} aria-current={tab === "other" ? "page" : undefined}>
          Connect something else
        </button>
        <button onClick={() => setTab("adding")} aria-current={tab === "adding" ? "page" : undefined}>
          Add a system
        </button>
      </div>

      {tab === "chatgpt" && (
        <ChatGPTGuide plugins={plugins} meta={meta} endpoints={endpoints} tunnel={tunnel} />
      )}
      {tab === "other" && <OtherGuide plugins={plugins} />}
      {tab === "adding" && <AddingGuide />}
    </>
  );
}

function ChatGPTGuide({ plugins, meta, endpoints, tunnel }: {
  plugins: Plugin[];
  meta: Meta | null;
  endpoints: Endpoints | null;
  tunnel: TunnelInfo | null;
}) {
  const state = tunnel?.status.state ?? "disabled";
  const connected = state === "connected";

  return (
    <>
      <div className="callout">
        <strong>Good news:</strong> mcpd doesn't need to be on the public
        internet. ChatGPT reaches it through a private tunnel that dials out
        from here, so you never open a port on your router or firewall. The
        tunnel is built into mcpd — there's nothing extra to install or run.
      </div>

      <Step n={1} title="Create a tunnel in your OpenAI account">
        <p>
          Go to <em>Settings → Organization → Tunnels</em> on the OpenAI
          platform site and create one. Copy the tunnel ID — it looks like{" "}
          <code>tunnel_</code> followed by a long string.
        </p>
      </Step>

      <Step n={2} title="Make an API key">
        <p>
          In the same place, under <em>API keys</em>, create a key. It needs
          the <strong>Tunnels: Read and Use</strong> permission.
        </p>
        <div className="callout subtle" style={{ marginTop: 4 }}>
          <strong>Not an admin key.</strong> Admin keys only create and delete
          tunnels — they can't run one. If you use one here, mcpd will connect
          to nothing and ChatGPT won't see your tunnel.
        </div>
      </Step>

      <Step n={3} title="Paste them into Settings">
        <p>
          Open the <strong>Settings</strong> tab, turn the tunnel on, and paste
          in your tunnel ID and key. Choose which systems it's allowed to reach
          while you're there.
        </p>
        <p>
          Save, and mcpd connects straight away. You'll see it go green on the
          Connections tab.
        </p>
        {tunnel && (
          <p className={connected ? "hint ok-text" : "hint"}>
            Right now: <strong>{describeState(state)}</strong>
          </p>
        )}
      </Step>

      <Step n={4} title="Point ChatGPT at it">
        <p>
          In ChatGPT, go to <em>Plugins</em>, create a developer-mode app, and
          choose <strong>Tunnel</strong> as the connection type. Yours should
          be in the list.
        </p>
        <p>That's it — no address to copy, no token to paste.</p>
      </Step>

      <div className="callout subtle">
        <strong>ChatGPT can't see your tunnel?</strong> Nearly always one of
        these three:
        <ul style={{ margin: "10px 0 0", paddingLeft: 18 }}>
          <li>
            <strong>It isn't connected.</strong> A tunnel only appears in
            ChatGPT while something is actually serving it. Check the
            Connections tab shows it green — if it's off or failed, ChatGPT has
            nothing to list. This is the usual one.
          </li>
          <li>
            <strong>The key is an admin key.</strong> See step 2. It'll look
            like it should work and won't.
          </li>
          <li>
            <strong>Different accounts.</strong> Tunnel access and ChatGPT
            developer mode are granted separately — having one doesn't give you
            the other. Check the tunnel is in the same workspace as the ChatGPT
            account you're using.
          </li>
        </ul>
      </div>

      {meta?.auth_mode === "static" && (
        <div className="callout subtle">
          <strong>About approvals.</strong> Everyone currently shares one
          sign-in, so mcpd can't tell two people apart. That means it can't
          enforce "someone else has to approve this" — it refuses those changes
          rather than pretending. Switch to individual accounts in your startup
          file if you want that.
        </div>
      )}

      {plugins.length === 0 && (
        <div className="callout subtle">
          <strong>Nothing to connect to yet.</strong> Turn on a system first —
          see the "Add a system" tab.
        </div>
      )}

      {endpoints && (
        <div className="callout subtle">
          <strong>Not using the tunnel?</strong> You can point any client at{" "}
          <code>{endpoints.aggregate}</code> directly with a token. See
          "Connect something else".
        </div>
      )}
    </>
  );
}

function describeState(state: string): string {
  switch (state) {
    case "connected":
      return "connected — ChatGPT should see it";
    case "starting":
      return "connecting…";
    case "failed":
      return "failed to connect — check the Connections tab for why";
    case "stopped":
      return "set up but switched off";
    default:
      return "not set up yet";
  }
}

function OtherGuide({ plugins }: { plugins: Plugin[] }) {
  const example = plugins[0];

  return (
    <>
      <p>
        Anything that speaks the Model Context Protocol can connect. Point it at
        one of the addresses on the Connections tab and give it a token.
      </p>

      <Step n={1} title="Get the address">
        <p>
          Open the Connections tab. The address at the top covers everything
          your token can reach; each system below also has its own, if you'd
          rather keep them separate.
        </p>
        {example && <Code>{example.connect_url}</Code>}
      </Step>

      <Step n={2} title="Send the token with every request">
        <p>The client needs to send your token as a header:</p>
        <Code>{`Authorization: Bearer YOUR_TOKEN`}</Code>
      </Step>

      <Step n={3} title="Try it">
        <p>To check it works from a terminal:</p>
        <Code>{`curl -X POST ${example?.connect_url ?? "http://your-mcpd:9080/mcp/example"} \\
  -H "Authorization: Bearer YOUR_TOKEN" \\
  -H "Accept: application/json, text/event-stream" \\
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}'`}</Code>
        <p className="hint">
          You should get back a list of everything that system can do.
        </p>
      </Step>

      <div className="callout subtle">
        <strong>Keep the address private.</strong> Anyone who can reach it and
        has a token can use it. Put it behind a tunnel or a proxy with HTTPS
        before exposing it beyond your own network.
      </div>
    </>
  );
}

function AddingGuide() {
  return (
    <>
      <p>
        A "system" here is anything mcpd can manage — a network controller, a
        server, a DNS provider. Each one is a small program that mcpd talks to.
      </p>

      <Step n={1} title="Turn one on">
        <p>
          Some are built in. Open your config file and set the one you want to{" "}
          <code>enabled: true</code>:
        </p>
        <Code>{`plugins:
  cnmaestro:
    enabled: true
    settings:
      base_url: "https://cloud.cambiumnetworks.com"
      client_id_ref: env:MY_CLIENT_ID
      client_secret_ref: env:MY_CLIENT_SECRET`}</Code>
      </Step>

      <Step n={2} title="Put the passwords somewhere safe">
        <p>
          Notice the settings above say <code>env:MY_CLIENT_ID</code> rather
          than the actual value. mcpd never wants passwords written into the
          config file, because that file tends to end up in backups and version
          control.
        </p>
        <p>
          Put the real values in a file called <code>.env</code> next to your
          config:
        </p>
        <Code>{`MY_CLIENT_ID=abc123
MY_CLIENT_SECRET=shhh`}</Code>
        <p className="hint">
          Keep that file private — <code>chmod 600 .env</code> on Linux or Mac.
        </p>
      </Step>

      <Step n={3} title="Restart">
        <p>
          Restart mcpd and the new system appears on the Connections tab. If
          something's wrong with the settings, mcpd will say so on startup
          rather than failing later.
        </p>
      </Step>

      <div className="callout subtle">
        <strong>Adding your own.</strong> You can write a system of your own in
        Go and drop it into the plugins folder — no rebuilding required. See{" "}
        <code>examples/echo</code> in the source for a complete one in a single
        file.
      </div>
    </>
  );
}

function Step({ n, title, children }: { n: number; title: string; children: React.ReactNode }) {
  return (
    <section className="step">
      <div className="step-n" aria-hidden="true">{n}</div>
      <div className="step-body">
        <h3>{title}</h3>
        {children}
      </div>
    </section>
  );
}

function Code({ children }: { children: string }) {
  const [copied, setCopied] = useState(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(children);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      setCopied(false);
    }
  }

  return (
    <div className="codeblock">
      <button className="btn small copy" onClick={copy}>
        {copied ? "Copied" : "Copy"}
      </button>
      <pre>{children}</pre>
    </div>
  );
}
