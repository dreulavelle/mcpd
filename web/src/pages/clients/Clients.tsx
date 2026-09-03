import { useCallback, useMemo, type ReactNode } from "react";
import { api, type Endpoints, type Plugin } from "@/lib/api";
import { useLoader } from "@/lib/hooks";
import { Link, useQueryParam } from "@/lib/router";
import { useCan } from "@/lib/session";
import { CodeBlock, Copyable, Loading, Notice, Out, PageHeader, Section } from "@/components/chrome";
import { Chip } from "@/components/status";
import { Card, CardContent } from "@/components/ui/card";
import { NativeSelect } from "@/components/ui/native-select";

/**
 * How to reach this host from something other than ChatGPT.
 *
 * Tunnels get a page because a tunnel is a thing this host runs. A direct
 * client is not: it is an address, a key and a few lines in somebody else's
 * configuration file, and every client spells those lines differently. This
 * page exists so that nobody has to work the spelling out from the client's
 * own documentation with the address on a clipboard -- the address and the
 * chosen plugin are filled into each snippet as they stand on this host.
 *
 * The key is never here. Every snippet reads it from an environment variable
 * or a prompt, because a page that pasted a key into a file people commit is
 * a page that leaks keys.
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

  return (
    <>
      <PageHeader
        title="Clients"
        lede="Reach this host from Claude Code, Codex, VS Code, or anything else that speaks MCP over HTTP."
      />
      <div className="space-y-6">
        <WhichWay />
        {endpoints.error && <Notice tone="problem">{endpoints.error}</Notice>}
        {plugins.error && <Notice tone="problem">{plugins.error}</Notice>}
        {!endpoints.data ? (
          <Loading rows={4} />
        ) : (
          <Steps
            endpoints={endpoints.data}
            plugins={list}
            chosen={chosen}
            onReach={setReach}
            client={CLIENTS.some((c) => c.id === client) ? client : CLIENTS[0]!.id}
            onClient={(id) => setClient(id === CLIENTS[0]!.id ? "" : id)}
          />
        )}
        <Exposed />
      </div>
    </>
  );
}

/** The decision before any of the steps: whether this page is the right one. */
function WhichWay() {
  return (
    <Card>
      <CardContent className="grid gap-4 text-sm sm:grid-cols-2">
        <div className="space-y-1">
          <p className="font-medium">A client that can reach this machine</p>
          <p className="text-muted-foreground">
            Claude Code, Codex, an IDE, a script. It connects to the address
            below and presents a key. This page is for that.
          </p>
        </div>
        <div className="space-y-1">
          <p className="font-medium">ChatGPT</p>
          <p className="text-muted-foreground">
            Cannot hold a key and does not reach in; mcpd reaches out to it
            instead. That is a{" "}
            <Link to="/tunnels" className="text-primary hover:underline">tunnel</Link>,
            and nothing here applies to it.
          </p>
        </div>
      </CardContent>
    </Card>
  );
}

function Steps({ endpoints, plugins, chosen, onReach, client, onClient }: {
  endpoints: Endpoints;
  plugins: Plugin[];
  chosen: Plugin | null;
  onReach: (name: string) => void;
  client: string;
  onClient: (id: string) => void;
}) {
  const address = chosen ? chosen.connect_url : endpoints.aggregate;
  // An address that is only a path means nothing has been advertised: the
  // server returns the route bare rather than inventing a host from a header.
  const advertised = /^https?:\/\//.test(address);
  const canIssue = useCan("access:write");

  const snippet = useMemo(
    () => CLIENTS.find((c) => c.id === client)!.render({
      address,
      name: chosen ? `mcpd-${chosen.name}` : "mcpd",
    }),
    [client, address, chosen],
  );

  return (
    <>
      <Section
        title="1. The address"
        description="One address serves everything the key is allowed to reach. A plugin's own address serves that plugin alone, and answers nothing else."
      >
        <Card>
          <CardContent className="space-y-3">
            <div className="flex flex-wrap items-center gap-2">
              <label htmlFor="reach" className="text-sm text-muted-foreground">Reach</label>
              <NativeSelect
                id="reach"
                value={chosen?.name ?? ""}
                onChange={(e) => onReach(e.target.value)}
                className="w-auto"
              >
                <option value="">everything the key allows</option>
                {plugins.map((p) => (
                  <option key={p.name} value={p.name}>{p.title || p.name} only</option>
                ))}
              </NativeSelect>
            </div>
            <Copyable value={address} label="address" />
            {!advertised && (
              <Notice tone="attention">
                That is only a path. This host has not been told the address it
                is reached at, so it will not guess one from a request. Set{" "}
                <em>Address assistants use</em> under{" "}
                <Link to="/settings" className="text-primary hover:underline">Settings › General</Link>
                {" "}and the snippets below fill in.
              </Notice>
            )}
          </CardContent>
        </Card>
      </Section>

      <Section
        title="2. A key"
        description="A key is a bearer token this host issued. It carries which plugins it reaches and what it may do, and it is shown once."
      >
        <Card>
          <CardContent className="space-y-3 text-sm">
            <p className="text-muted-foreground">
              Issue one per client rather than sharing, and give it only the
              plugins that client needs: a key limited to one plugin gets a 404
              from every other address, so it cannot find out what else is
              here. An expiry is cheap to set and expensive to wish you had.
            </p>
            {canIssue ? (
              <p>
                <Link to="/settings/keys" className="text-primary hover:underline">
                  Issue a key under Settings › API Keys
                </Link>
                . It begins <code className="font-mono">mcpd_</code> and is
                never shown again, so copy it into the environment variable
                the snippets read before closing the dialog.
              </p>
            ) : (
              <p>
                An administrator issues keys under Settings › API Keys. Ask
                for one that reaches what you need, and put it in the
                environment variable the snippets read.
              </p>
            )}
            <CodeBlock>{"export MCPD_KEY=mcpd_…"}</CodeBlock>
          </CardContent>
        </Card>
      </Section>

      <Section
        title="3. The client"
        description="The address and the name above are already filled in. The key is read from the environment, never written into a file that gets committed."
      >
        <Card>
          <CardContent className="space-y-3">
            <div className="flex flex-wrap items-center gap-1.5" role="group" aria-label="Client">
              {CLIENTS.map((c) => (
                <button
                  key={c.id}
                  type="button"
                  onClick={() => onClient(c.id)}
                  aria-pressed={client === c.id}
                >
                  <Chip tone={client === c.id ? "info" : "neutral"}>{c.label}</Chip>
                </button>
              ))}
            </div>
            {snippet}
          </CardContent>
        </Card>
      </Section>

      <Section
        title="4. Check it"
        description="The same request every client makes first. No session to open: this host answers each request on its own."
      >
        <Card>
          <CardContent className="space-y-3 text-sm">
            <CodeBlock>{curl(address)}</CodeBlock>
            <p className="text-muted-foreground">
              The answer lists tools, each named for its plugin and its verb,
              so <code className="font-mono">search_messages</code> on graylog
              arrives as <code className="font-mono">graylog_search_messages</code>.
              A 401 is a key this host did not issue or has revoked; a 404 on
              a plugin's address is a key that does not reach that plugin. The
              call itself is on{" "}
              <Link to="/activity" className="text-primary hover:underline">Activity</Link>
              {" "}under the key's name.
            </p>
          </CardContent>
        </Card>
      </Section>
    </>
  );
}

/**
 * When this host is reachable from the internet rather than from a desk on
 * the same network. Nothing here is a step; it is what changes.
 */
function Exposed() {
  return (
    <Section
      title="If this host is on the internet"
      description="Nothing above changes. What changes is what you expose, and how carefully."
    >
      <Card>
        <CardContent className="space-y-3 text-sm text-muted-foreground">
          <p>
            <span className="font-medium text-foreground">Expose the MCP listener only.</span>{" "}
            The dashboard is a separate port on purpose, so a firewall rule can
            tell the two apart. Clients need the first; nobody on the internet
            needs the second.
          </p>
          <p>
            <span className="font-medium text-foreground">Terminate TLS in front, or let mcpd do it.</span>{" "}
            Behind a reverse proxy, leave <em>Certificate for the MCP endpoint</em>{" "}
            off and set <em>Address assistants use</em> to the https address the
            proxy answers on; that is also how mcpd knows the connection was
            really https. Reaching mcpd directly, set it to self-signed and
            restart. Both are under{" "}
            <Link to="/settings" className="text-primary hover:underline">Settings › General</Link>.
          </p>
          <p>
            <span className="font-medium text-foreground">Treat every key as a door.</span>{" "}
            One per client, the narrowest grant that works, an expiry, and a
            revocation the moment a laptop is lost. Every call a key makes is
            on Activity with the key's name against it, which is what makes
            the narrowing worth doing.
          </p>
          <p>
            <span className="font-medium text-foreground">ChatGPT still goes through a tunnel.</span>{" "}
            A connector cannot carry a key of its own, and mcpd is not an OAuth
            authorization server, so a public address does not let ChatGPT
            connect directly. The tunnel needs no inbound port at all, which
            is the better arrangement anyway.
          </p>
        </CardContent>
      </Card>
    </Section>
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

function curl(address: string): string {
  return [
    `curl -sS ${address} \\`,
    `  -H "Authorization: Bearer $MCPD_KEY" \\`,
    `  -H "Content-Type: application/json" \\`,
    `  -H "Accept: application/json, text/event-stream" \\`,
    `  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'`,
  ].join("\n");
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
 * syntax, which is why they differ: ${MCPD_KEY} is expanded by Claude Code
 * itself, ${env:MCPD_KEY} by Cursor, ${input:…} by VS Code at connect time,
 * and Codex takes the variable's name rather than a reference.
 */
const CLIENTS: ClientGuide[] = [
  {
    id: "claude-code",
    label: "Claude Code",
    render: ({ address, name }) => (
      <Guide>
        <Note>
          One command. The shell expands the key before Claude Code stores the
          entry, so this goes in your own configuration
          (<code className="font-mono">--scope user</code>), never in a
          project's.
        </Note>
        <CodeBlock>{[
          `claude mcp add --transport http ${name} ${address} \\`,
          `  --header "Authorization: Bearer $MCPD_KEY" --scope user`,
        ].join("\n")}</CodeBlock>
        <Note>
          For a <code className="font-mono">.mcp.json</code> that is committed
          with a project, reference the variable instead and Claude Code
          expands it when it connects. The file then holds no key.
        </Note>
        <CodeBlock>{JSON.stringify({
          mcpServers: {
            [name]: {
              type: "http",
              url: address,
              headers: { Authorization: "Bearer ${MCPD_KEY}" },
            },
          },
        }, null, 2)}</CodeBlock>
        <Note>
          <code className="font-mono">/mcp</code> inside Claude Code shows
          whether it connected and which tools it found.{" "}
          <Out href="https://code.claude.com/docs/en/mcp">Claude Code's MCP reference</Out>.
        </Note>
      </Guide>
    ),
  },
  {
    id: "codex",
    label: "Codex",
    render: ({ address, name }) => (
      <Guide>
        <Note>
          Codex is told the variable's name and reads the key from it on every
          start, so the key is never in its configuration.
        </Note>
        <CodeBlock>{`codex mcp add ${name} --url ${address} --bearer-token-env MCPD_KEY`}</CodeBlock>
        <Note>Or the same thing written into <code className="font-mono">~/.codex/config.toml</code>:</Note>
        <CodeBlock>{[
          `[mcp_servers.${name.replace(/-/g, "_")}]`,
          `url = "${address}"`,
          `bearer_token_env_var = "MCPD_KEY"`,
        ].join("\n")}</CodeBlock>
        <Note>
          <Out href="https://developers.openai.com/codex/mcp">Codex's MCP reference</Out>.
        </Note>
      </Guide>
    ),
  },
  {
    id: "vscode",
    label: "VS Code",
    render: ({ address, name }) => (
      <Guide>
        <Note>
          VS Code asks for the key the first time it connects and keeps it in
          its own secret store, so the file below can be committed as it is.
          Put it at <code className="font-mono">.vscode/mcp.json</code> for one
          project, or in the user profile through{" "}
          <em>MCP: Open User Configuration</em> for all of them.
        </Note>
        <CodeBlock>{JSON.stringify({
          inputs: [{
            id: "mcpd-key",
            type: "promptString",
            description: "mcpd API key",
            password: true,
          }],
          servers: {
            [name]: {
              type: "http",
              url: address,
              headers: { Authorization: "Bearer ${input:mcpd-key}" },
            },
          },
        }, null, 2)}</CodeBlock>
        <Note>
          <Out href="https://code.visualstudio.com/docs/copilot/customization/mcp-servers">VS Code's MCP reference</Out>.
          Forks of VS Code differ in where the file lives and which of the two
          shapes they read; Cursor's is the next tab.
        </Note>
      </Guide>
    ),
  },
  {
    id: "cursor",
    label: "Cursor",
    render: ({ address, name }) => (
      <Guide>
        <Note>
          <code className="font-mono">.cursor/mcp.json</code> in a project, or{" "}
          <code className="font-mono">~/.cursor/mcp.json</code> for every
          project. Cursor resolves the variable when it connects.
        </Note>
        <CodeBlock>{JSON.stringify({
          mcpServers: {
            [name]: {
              url: address,
              headers: { Authorization: "Bearer ${env:MCPD_KEY}" },
            },
          },
        }, null, 2)}</CodeBlock>
        <Note>
          <Out href="https://cursor.com/docs/context/mcp">Cursor's MCP reference</Out>.
        </Note>
      </Guide>
    ),
  },
  {
    id: "stdio",
    label: "Claude Desktop and other stdio-only clients",
    render: ({ address, name }) => (
      <Guide>
        <Note>
          Some clients only start a local process and talk to it over stdio,
          and cannot send a header of their own. <code className="font-mono">mcp-remote</code>{" "}
          bridges the gap: the client runs it, and it carries the key to this
          host. For Claude Desktop this goes in{" "}
          <code className="font-mono">claude_desktop_config.json</code>.
        </Note>
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
          The header is written without a space and the value comes from the
          process environment, because some of these clients split arguments
          on spaces. This file does hold the key, so it belongs where only you
          can read it.
        </Note>
      </Guide>
    ),
  },
];
