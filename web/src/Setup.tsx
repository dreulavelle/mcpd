import { useEffect, useState } from "react";
import { api, type Endpoints, type Meta, type Plugin } from "./api";

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

  useEffect(() => {
    api.endpoints().then(setEndpoints).catch(() => setEndpoints(null));
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

      {tab === "chatgpt" && <ChatGPTGuide plugins={plugins} meta={meta} endpoints={endpoints} />}
      {tab === "other" && <OtherGuide plugins={plugins} />}
      {tab === "adding" && <AddingGuide />}
    </>
  );
}

function ChatGPTGuide({ plugins, meta, endpoints }: {
  plugins: Plugin[];
  meta: Meta | null;
  endpoints: Endpoints | null;
}) {
  const aggregate = endpoints?.aggregate ?? plugins[0]?.connect_url ?? "http://your-mcpd:9080/mcp";

  return (
    <>
      <div className="callout">
        <strong>Good news:</strong> mcpd doesn't need to be on the public
        internet for this. ChatGPT can reach it through a private tunnel that
        dials out from your network, so you don't have to open any ports on your
        router or firewall.
      </div>

      <Step n={1} title="Create a tunnel">
        <p>
          Go to your OpenAI account settings and create a tunnel. You'll get two
          things: a tunnel ID and an API key. Keep both handy.
        </p>
        <p className="hint">
          Look under <em>Settings → Organization → Tunnels</em> on the OpenAI
          platform site.
        </p>
      </Step>

      <Step n={2} title="Run the tunnel program">
        <p>
          OpenAI provides a small program that sits on your network and relays
          messages. It connects outward to OpenAI, so nothing needs to reach in.
        </p>
        <Code>{`brew install openai/tools/tunnel-client

export CONTROL_PLANE_API_KEY='your-runtime-api-key'
export CONTROL_PLANE_TUNNEL_ID='your-tunnel-id'

tunnel-client run \\
  --mcp.server-url ${aggregate} \\
  --mcp.extra-headers "Authorization: Bearer YOUR_TOKEN"`}</Code>
        <p className="hint">
          Replace <code>YOUR_TOKEN</code> with a token from your config file —
          the same kind you used to sign in here. The address above covers
          everything that token is allowed to reach.
        </p>
        <p className="hint">
          The API key is a <strong>runtime</strong> key from your OpenAI
          account, not an admin key. Admin keys are only for creating and
          deleting tunnels.
        </p>
      </Step>

      <Step n={3} title="Point ChatGPT at it">
        <p>
          In ChatGPT, go to <em>Plugins</em>, create a developer-mode app, and
          choose <strong>Tunnel</strong> as the connection type. Pick the tunnel
          you just made.
        </p>
        <p>
          That's it. ChatGPT can now use whatever you've given that token access
          to.
        </p>
      </Step>

      <div className="callout subtle">
        <strong>Want to keep systems apart?</strong> The address above covers
        everything the token can reach. If you'd rather one assistant only ever
        touched one system, point the tunnel at that system's own address
        instead — you'll find it on the Connections tab — and give it a token
        limited to the same thing. Then a mistake in one conversation can't
        touch anything else.
      </div>

      {meta?.auth_mode === "static" && (
        <div className="callout subtle">
          <strong>About approvals.</strong> Right now everyone shares one token,
          so mcpd can't tell two people apart. That means it can't enforce
          "someone else has to approve this". If you want that, switch to
          sign-in accounts in your config file — see the docs for{" "}
          <code>auth.mode: oauth</code>.
        </div>
      )}
    </>
  );
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
