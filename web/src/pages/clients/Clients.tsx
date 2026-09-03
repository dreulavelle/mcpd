import { useCallback, useMemo, type ReactNode } from "react";
import { api, type Endpoints, type Plugin } from "@/lib/api";
import { useLoader } from "@/lib/hooks";
import { Link, useQueryParam } from "@/lib/router";
import { useCan } from "@/lib/session";
import { CodeBlock, Copyable, Loading, Notice, Out, PageHeader } from "@/components/chrome";
import { Segmented } from "@/components/Segmented";
import { Card, CardContent } from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";

/**
 * How to reach this host from something other than ChatGPT.
 *
 * One panel: choose what to reach and which client, and the address and
 * the snippet are filled in as they stand on this host. The key is never
 * here; every snippet reads it from an environment variable or a prompt,
 * because a page that pasted a key into a file people commit is a page
 * that leaks keys.
 */
export function Clients() {
  const loadEndpoints = useCallback(() => api.endpoints(), []);
  const loadPlugins = useCallback(() => api.plugins(), []);
  const endpoints = useLoader(loadEndpoints, "Couldn't read this host's address.");
  const plugins = useLoader(loadPlugins, "Couldn't list the plugins.");
  const [reach, setReach] = useQueryParam("reach");
  const [client, setClient] = useQueryParam("client");

  const list = plugins.data?.plugins ?? [];
  const chosen = list.find((p) => p.name === reach) ?? null;
  const clientId = CLIENTS.some((c) => c.id === client) ? client : CLIENTS[0]!.id;

  return (
    <>
      <PageHeader
        title="Clients"
        lede="Claude Code, Codex, an IDE, a script. ChatGPT connects through a tunnel instead."
      />
      <div className="space-y-8">
        {endpoints.error && <Notice tone="problem">{endpoints.error}</Notice>}
        {plugins.error && <Notice tone="problem">{plugins.error}</Notice>}
        {!endpoints.data ? (
          <Loading rows={4} />
        ) : (
          <Connect
            endpoints={endpoints.data}
            plugins={list}
            chosen={chosen}
            onReach={setReach}
            client={clientId}
            onClient={(id) => setClient(id === CLIENTS[0]!.id ? "" : id)}
          />
        )}
        <Exposed />
      </div>
    </>
  );
}

function Connect({ endpoints, plugins, chosen, onReach, client, onClient }: {
  endpoints: Endpoints;
  plugins: Plugin[];
  chosen: Plugin | null;
  onReach: (name: string) => void;
  client: string;
  onClient: (id: string) => void;
}) {
  const stored = chosen ? chosen.connect_url : endpoints.aggregate;
  // With no public address set the server returns the route bare rather
  // than inventing a host from a header. The page knows one thing the
  // server refuses to guess: the host it was itself reached on. That plus
  // the MCP port is right for a client on the same network, and is said
  // to be a guess.
  const advertised = /^https?:\/\//.test(stored);
  const address = advertised ? stored : `http://${window.location.hostname}:${endpoints.port}${stored}`;
  const canIssue = useCan("access:write");
  const guide = CLIENTS.find((c) => c.id === client)!;

  const snippet = useMemo(
    () => guide.render({ address, name: chosen ? `mcpd-${chosen.name}` : "mcpd" }),
    [guide, address, chosen],
  );

  return (
    <Card>
      <CardContent className="space-y-5">
        <div className="flex flex-wrap items-end gap-x-6 gap-y-3">
          <div className="space-y-1.5">
            <Label htmlFor="reach">Reach</Label>
            <NativeSelect id="reach" value={chosen?.name ?? ""} onChange={(e) => onReach(e.target.value)} className="w-auto">
              <option value="">Everything the key allows</option>
              {plugins.map((p) => <option key={p.name} value={p.name}>{p.title || p.name} only</option>)}
            </NativeSelect>
          </div>
          <div className="space-y-1.5">
            <Label>Client</Label>
            <div>
              <Segmented
                label="Client" value={client} size="md"
                options={CLIENTS.map((c) => ({ value: c.id, label: c.label }))}
                onChange={onClient}
              />
            </div>
          </div>
        </div>

        <div className="space-y-1.5">
          <Label>Address</Label>
          <Copyable value={address} label="address" />
          {!advertised && (
            <Notice tone="neutral">
              A guess from this page's host and the MCP port. If clients reach
              this host another way, set <em>Address assistants use</em> under{" "}
              <Link to="/settings" className="text-primary hover:underline">Settings › General</Link>.
            </Notice>
          )}
        </div>

        {snippet}

        <p className="text-sm text-muted-foreground">
          The snippet reads the key from <code className="font-mono">MCPD_KEY</code>.{" "}
          {canIssue ? (
            <>
              <Link to="/settings/keys" className="text-primary hover:underline">Issue a key under Settings › API Keys</Link>
              ; it is shown once. Give it only the systems this client needs.
            </>
          ) : (
            <>An administrator issues keys under Settings › API Keys; ask for one that reaches what you need.</>
          )}
        </p>
      </CardContent>
    </Card>
  );
}

/** What changes when this host is reachable from the internet. */
function Exposed() {
  return (
    <div className="space-y-3 text-sm">
      <h2 className="text-xs font-semibold tracking-wide text-muted-foreground uppercase">If this host is on the internet</h2>
      <ul className="list-disc space-y-1.5 pl-5 text-muted-foreground">
        <li>Expose the MCP listener only. The dashboard is a separate port so a firewall can tell them apart.</li>
        <li>
          Terminate TLS in front and set <em>Address assistants use</em> to the https address, or set{" "}
          <em>Certificate for the MCP endpoint</em> to self-signed. Both under{" "}
          <Link to="/settings" className="text-primary hover:underline">Settings › General</Link>.
        </li>
        <li>One key per client, the narrowest grant that works, an expiry. Every call lands on Activity under the key's name.</li>
        <li>ChatGPT still needs a tunnel: a connector cannot carry a key, and mcpd is not an OAuth server.</li>
      </ul>
    </div>
  );
}

/* -- the snippets ---------------------------------------------------------- */

interface Fill {
  address: string;
  /** The name the client shows the server under. */
  name: string;
}

interface ClientGuide {
  id: string;
  label: string;
  render: (fill: Fill) => ReactNode;
}

function Guide({ children }: { children: ReactNode }) {
  return <div className="space-y-3 text-sm">{children}</div>;
}

function Note({ children }: { children: ReactNode }) {
  return <p className="text-muted-foreground">{children}</p>;
}

/**
 * One entry per client, each checked against that client's own documentation
 * rather than remembered. Env var references are in each client's own
 * syntax, which is why they differ.
 */
const CLIENTS: ClientGuide[] = [
  {
    id: "claude-code",
    label: "Claude Code",
    render: ({ address, name }) => (
      <Guide>
        <CodeBlock>{[
          `claude mcp add --transport http ${name} ${address} \\`,
          `  --header "Authorization: Bearer $MCPD_KEY" --scope user`,
        ].join("\n")}</CodeBlock>
        <Note>
          For a committed <code className="font-mono">.mcp.json</code>, put{" "}
          <code className="font-mono">"Authorization": "Bearer ${"{MCPD_KEY}"}"</code> under{" "}
          <code className="font-mono">headers</code>; Claude Code expands it when it connects.{" "}
          <Out href="https://code.claude.com/docs/en/mcp">Reference</Out>.
        </Note>
      </Guide>
    ),
  },
  {
    id: "codex",
    label: "Codex",
    render: ({ address, name }) => (
      <Guide>
        <CodeBlock>{`codex mcp add ${name} --url ${address} --bearer-token-env MCPD_KEY`}</CodeBlock>
        <Note>
          Or in <code className="font-mono">~/.codex/config.toml</code>:{" "}
          <code className="font-mono">[mcp_servers.{name.replace(/-/g, "_")}]</code> with{" "}
          <code className="font-mono">url</code> and <code className="font-mono">bearer_token_env_var = "MCPD_KEY"</code>.{" "}
          <Out href="https://developers.openai.com/codex/mcp">Reference</Out>.
        </Note>
      </Guide>
    ),
  },
  {
    id: "vscode",
    label: "VS Code",
    render: ({ address, name }) => (
      <Guide>
        <CodeBlock>{JSON.stringify({
          inputs: [{ id: "mcpd-key", type: "promptString", description: "mcpd API key", password: true }],
          servers: { [name]: { type: "http", url: address, headers: { Authorization: "Bearer ${input:mcpd-key}" } } },
        }, null, 2)}</CodeBlock>
        <Note>
          <code className="font-mono">.vscode/mcp.json</code>, or the user profile via <em>MCP: Open User Configuration</em>.
          VS Code asks for the key once and keeps it.{" "}
          <Out href="https://code.visualstudio.com/docs/copilot/customization/mcp-servers">Reference</Out>.
        </Note>
      </Guide>
    ),
  },
  {
    id: "cursor",
    label: "Cursor",
    render: ({ address, name }) => (
      <Guide>
        <CodeBlock>{JSON.stringify({
          mcpServers: { [name]: { url: address, headers: { Authorization: "Bearer ${env:MCPD_KEY}" } } },
        }, null, 2)}</CodeBlock>
        <Note>
          <code className="font-mono">.cursor/mcp.json</code> in a project, or <code className="font-mono">~/.cursor/mcp.json</code>.{" "}
          <Out href="https://cursor.com/docs/context/mcp">Reference</Out>.
        </Note>
      </Guide>
    ),
  },
  {
    id: "stdio",
    label: "Claude Desktop",
    render: ({ address, name }) => (
      <Guide>
        <CodeBlock>{JSON.stringify({
          mcpServers: {
            [name]: {
              command: "npx",
              args: ["-y", "mcp-remote", address, "--header", "Authorization:${AUTH_HEADER}"],
              env: { AUTH_HEADER: "Bearer mcpd_…" },
            },
          },
        }, null, 2)}</CodeBlock>
        <Note>
          For clients that only run a local process: <code className="font-mono">mcp-remote</code> carries the key.
          This file holds it, so keep it where only you can read it.
        </Note>
      </Guide>
    ),
  },
  {
    id: "curl",
    label: "curl",
    render: ({ address }) => (
      <Guide>
        <CodeBlock>{[
          `curl -sS ${address} \\`,
          `  -H "Authorization: Bearer $MCPD_KEY" \\`,
          `  -H "Content-Type: application/json" \\`,
          `  -H "Accept: application/json, text/event-stream" \\`,
          `  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'`,
        ].join("\n")}</CodeBlock>
        <Note>
          The request every client makes first. Tools come back plugin first, as in{" "}
          <code className="font-mono">graylog_search_messages</code>. A 401 is a key this host did not issue;
          a 404 on a plugin's address is a key that does not reach it.
        </Note>
      </Guide>
    ),
  },
];
