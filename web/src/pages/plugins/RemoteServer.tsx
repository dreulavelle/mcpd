import { useCallback, useMemo, useState } from "react";
import { RefreshCw } from "lucide-react";
import {
  api, ApiError, type MCPDiff, type MCPServer, type MCPTool, type MCPToolState,
} from "@/lib/api";
import { when, whenExact } from "@/lib/format";
import { useLoader } from "@/lib/hooks";
import { useRouter } from "@/lib/router";
import { useCan } from "@/lib/session";
import { useNotify } from "@/components/toast";
import {
  Copyable, Detail, EmptyState, Loading, Notice, Section,
} from "@/components/chrome";
import { Chip, StatusDot } from "@/components/status";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";
import { ClassifyDialog } from "./ClassifyDialog";

const TOOL_STATES: Record<MCPToolState, { label: string; tone: "good" | "attention" | "neutral" }> = {
  enabled: { label: "Served", tone: "good" },
  pending: { label: "Waiting on you", tone: "attention" },
  disabled: { label: "Not served", tone: "neutral" },
};

type ToolFilter = "all" | MCPToolState;

/** Where a server stands. "Unreadable" first: no other state applies to one. */
export function ServerState({ server: s }: { server: MCPServer }) {
  if (!s.readable) {
    return <Chip tone="problem">Unreadable document</Chip>;
  }
  if (!s.enabled) {
    return <Chip tone="neutral">Switched off</Chip>;
  }
  if (s.mounted) {
    return (
      <Chip tone="good">
        <StatusDot tone="good" />
        Serving
      </Chip>
    );
  }
  return (
    <Chip tone="attention">
      <StatusDot tone="attention" />
      {s.enabled_tools === 0 ? "No tools enabled" : "Not mounted"}
    </Chip>
  );
}

/**
 * Everything about a plugin that happens to be somebody else's server. The tool
 * snapshot is shown in every state: "pending" is work outstanding and
 * "disabled" is a decision already taken.
 */
export function RemoteServer({ name, onChanged }: {
  name: string;
  /** Re-reads the plugin around this, whose health follows what happens here. */
  onChanged: () => void;
}) {
  const loadServers = useCallback(() => api.mcpServers(), []);
  const loadTools = useCallback(() => api.mcpServerTools(name), [name]);

  const servers = useLoader(loadServers, "Couldn't load remote servers.");
  const tools = useLoader(loadTools, "Couldn't load this server's tools.");

  const server = (servers.data?.servers ?? []).find((s) => s.name === name) ?? null;

  // The reload functions, not the loader objects: those are fresh every render
  // and would re-fire anything downstream that watched this.
  const { reload: reloadServers } = servers;
  const { reload: reloadTools } = tools;
  const reload = useCallback(() => {
    reloadServers();
    reloadTools();
    onChanged();
  }, [reloadServers, reloadTools, onChanged]);

  if (servers.error) return <Notice tone="problem">{servers.error}</Notice>;
  if (servers.data === null) return <Loading rows={4} />;
  if (!server) {
    return (
      <Notice tone="problem">
        <code className="font-mono">{name}</code> is mounted as a remote MCP
        server, but no imported document goes with it. Nothing here can be
        changed until that is sorted out.
      </Notice>
    );
  }

  return (
    <Body
      server={server}
      tools={tools.data?.tools ?? null}
      toolsError={tools.error}
      onChanged={reload}
    />
  );
}

function Body({ server, tools, toolsError, onChanged }: {
  server: MCPServer;
  tools: MCPTool[] | null;
  toolsError: string | null;
  onChanged: () => void;
}) {
  const notify = useNotify();
  const { navigate } = useRouter();
  // Reading is an operator's; classifying, discovering and removing are an
  // administrator's, which is how the endpoints are gated.
  const mayAdminister = useCan("admin");
  const [filter, setFilter] = useState<ToolFilter>("all");
  const [classifying, setClassifying] = useState<MCPTool | null>(null);
  const [discovering, setDiscovering] = useState(false);
  const [diff, setDiff] = useState<MCPDiff | null>(null);
  const [busy, setBusy] = useState(false);

  const shown = useMemo(() => {
    const list = tools ?? [];
    const filtered = filter === "all" ? list : list.filter((t) => t.state === filter);
    // Pending first: it is the only state that is asking for something.
    const rank: Record<MCPToolState, number> = { pending: 0, enabled: 1, disabled: 2 };
    return [...filtered].sort(
      (a, b) => rank[a.state] - rank[b.state] || a.name.localeCompare(b.name),
    );
  }, [tools, filter]);

  const pending = (tools ?? []).filter((t) => t.state === "pending").length;

  async function discover() {
    setDiscovering(true);
    setDiff(null);
    try {
      const result = await api.discoverMCPServer(server.name);
      setDiff(result.diff ?? {});
      notify("good", result.note ?? "Discovered.");
    } catch (e) {
      notify("problem", e instanceof ApiError
        ? e.detail
        : "Couldn't reach the server.");
    } finally {
      setDiscovering(false);
      onChanged();
    }
  }

  async function toggle(enabled: boolean) {
    setBusy(true);
    try {
      await api.setMCPServerEnabled(server.name, enabled);
      notify("good", enabled ? "Switched on." : "Switched off.");
    } catch (e) {
      notify("problem", e instanceof ApiError ? e.detail : "Couldn't change that.");
    } finally {
      setBusy(false);
      onChanged();
    }
  }

  async function remove() {
    if (!confirm(`Remove ${server.name}? Its document, its tool snapshot and its settings go with it.`)) {
      return;
    }
    setBusy(true);
    try {
      await api.removeMCPServer(server.name);
      notify("good", `Removed ${server.name}.`);
      navigate("/plugins");
    } catch (e) {
      notify("problem", e instanceof ApiError ? e.detail : "Couldn't remove it.");
      setBusy(false);
      onChanged();
    }
  }

  return (
    <>
      <ClassifyDialog
        server={server.name}
        tool={classifying}
        open={classifying !== null}
        onOpenChange={(open) => { if (!open) setClassifying(null); }}
        onDone={onChanged}
      />

      {!server.readable && (
        <Notice tone="problem">
          <strong>This build cannot read the stored document.</strong> The row
          can be listed and removed, and nothing else. It was imported by a
          build that understood a schema this one does not.
        </Notice>
      )}

      {/* Before the table rather than counted off it: a
          pending tool is not served, and that is the fact an operator most
          often has the wrong idea about. */}
      {pending > 0 && (
        <Notice tone="attention">
          {pending} {pending === 1 ? "tool is" : "tools are"} waiting to be
          classified, and{" "}
          {pending === 1 ? "it is not being served" : "they are not being served"}{" "}
          until you decide.
        </Notice>
      )}

      {diff && <DiscoveryResult diff={diff} />}

      <Section
        title="Tools"
        description="A tool arrives pending and is not served. What you classify is a description and a schema, not a name — if the server changes either, the tool comes back here."
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <NativeSelect
              aria-label="Show"
              className="w-44"
              value={filter}
              onChange={(e) => setFilter(e.target.value as ToolFilter)}
            >
              <option value="all">Everything</option>
              <option value="pending">Waiting on you</option>
              <option value="enabled">Served</option>
              <option value="disabled">Not served</option>
            </NativeSelect>
            {mayAdminister && (
              <Button variant="outline" size="sm" disabled={discovering} onClick={discover}>
                <RefreshCw
                  className={discovering ? "size-3.5 animate-spin" : "size-3.5"}
                  aria-hidden="true"
                />
                {discovering ? "Asking…" : "Discover"}
              </Button>
            )}
          </div>
        }
      >
        {toolsError && <Notice tone="problem">{toolsError}</Notice>}

        {tools === null && !toolsError ? (
          <Loading rows={4} />
        ) : shown.length === 0 ? (
          <EmptyState title={tools?.length ? "Nothing in that state" : "No tools yet"}>
            {tools?.length
              ? "Try another filter."
              : "Run discovery and mcpd will ask the server what it offers."}
          </EmptyState>
        ) : (
          <Card className="overflow-hidden p-0">
            <div className="scroll-x">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Tool</TableHead>
                    <TableHead>State</TableHead>
                    <TableHead>Last seen</TableHead>
                    <TableHead />
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {shown.map((t) => (
                    <TableRow key={t.name}>
                      <TableCell>
                        <div className="font-mono text-sm">{t.name}</div>
                        <div className="max-w-[52ch] truncate text-xs text-muted-foreground">
                          {t.descriptor.description || "No description."}
                        </div>
                        {t.problem && (
                          <div className="text-xs text-problem">{t.problem}</div>
                        )}
                      </TableCell>
                      <TableCell>
                        <Chip tone={TOOL_STATES[t.state].tone}>
                          {TOOL_STATES[t.state].label}
                        </Chip>
                      </TableCell>
                      <TableCell className="whitespace-nowrap text-muted-foreground">
                        {when(t.last_seen_at)}
                      </TableCell>
                      <TableCell className="text-right">
                        {mayAdminister && (
                          <Button
                            variant={t.state === "pending" ? "default" : "outline"}
                            size="sm"
                            onClick={() => setClassifying(t)}
                          >
                            {t.state === "pending" ? "Review" : "Change"}
                          </Button>
                        )}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          </Card>
        )}
      </Section>

      <Section title="The document">
        <Card>
          <CardContent>
            <dl className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
              {server.title && <Detail label="Title">{server.title}</Detail>}
              {server.version && <Detail label="Version">{server.version}</Detail>}
              <Detail label="Transport">
                <code className="font-mono text-xs">{server.transport || "—"}</code>
              </Detail>
              <Detail label="Schema">
                <code className="font-mono text-xs">{server.schema_version || "—"}</code>
              </Detail>
              <Detail label="Added">{whenExact(server.created_at)}</Detail>
              <Detail label="Last change">{whenExact(server.updated_at)}</Detail>
              <Detail label="Address" className="sm:col-span-2 lg:col-span-3">
                <Copyable value={server.url} label="address" />
                <span className="mt-1 block text-xs text-muted-foreground">
                  The template as imported. Anything in braces is filled in at
                  dial time from this server's settings, which is why a
                  credential never appears here.
                </span>
              </Detail>
            </dl>
          </CardContent>
        </Card>
      </Section>

      <Section title="Credentials">
        <Headers server={server} mayAdminister={mayAdminister} onChanged={onChanged} />
      </Section>

      <Section title="This server">
        <Card>
          <CardContent className="flex flex-wrap items-center justify-between gap-3">
            <div className="max-w-[52ch] space-y-2">
              <ServerState server={server} />
              {mayAdminister && (
                <p className="text-sm text-muted-foreground">
                  Switching it off stops serving its tools and keeps everything
                  that was decided about them. Removing it forgets the document,
                  the snapshot and the settings, including any credential.
                </p>
              )}
            </div>
            {mayAdminister && (
              <div className="flex gap-2">
                <Button
                  variant="outline" size="sm" disabled={busy || !server.readable}
                  onClick={() => toggle(!server.enabled)}
                >
                  {server.enabled ? "Switch off" : "Switch on"}
                </Button>
                <Button
                  variant="outline" size="sm" disabled={busy}
                  className="text-destructive hover:text-destructive"
                  onClick={remove}
                >
                  Remove
                </Button>
              </div>
            )}
          </CardContent>
        </Card>
      </Section>
    </>
  );
}

/** What the last discovery changed. */
/**
 * The headers this host sends that the published document never declared.
 *
 * Four in five published documents name no header and no variable at all. Some
 * of those servers really are open -- roughly a quarter answer without a
 * credential -- so this is offered rather than demanded: nothing here has to
 * be filled in for a server that needs nothing, and a 401 is the signal that
 * it does.
 *
 * Only the declaration is made here. The value is typed on the settings page,
 * into the field this creates, where it is encrypted like every other stored
 * credential and never read back.
 */
function Headers({ server, mayAdminister, onChanged }: {
  server: MCPServer;
  mayAdminister: boolean;
  onChanged: () => void;
}) {
  const notify = useNotify();
  const [adding, setAdding] = useState(false);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [busy, setBusy] = useState(false);
  const headers = server.extra_headers ?? [];

  async function add() {
    setBusy(true);
    try {
      const r = await api.addMCPServerHeader(server.name, name.trim(), description.trim(), true);
      notify("good", r.note ?? `Added ${name.trim()}.`);
      setName("");
      setDescription("");
      setAdding(false);
      onChanged();
    } catch (e) {
      notify("problem", e instanceof ApiError ? e.detail : "Couldn't add that header.");
    } finally {
      setBusy(false);
    }
  }

  async function remove(header: string) {
    setBusy(true);
    try {
      await api.removeMCPServerHeader(server.name, header);
      notify("good", `Removed ${header}.`);
      onChanged();
    } catch (e) {
      notify("problem", e instanceof ApiError ? e.detail : "Couldn't remove that header.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardContent className="space-y-4">
        {server.declares_no_credential && headers.length === 0 && (
          <Notice tone="info">
            This server's document declares no credential. That may be right —
            some servers need none — but it is silence rather than a statement,
            and it is the usual reason discovery comes back 401. If it does, add
            the header the server asks for and fill its value in on the settings
            page.
          </Notice>
        )}

        {headers.length > 0 && (
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Header</TableHead>
                  <TableHead>Note</TableHead>
                  {mayAdminister && <TableHead className="w-0" />}
                </TableRow>
              </TableHeader>
              <TableBody>
                {headers.map((h) => (
                  <TableRow key={h.name}>
                    <TableCell className="font-mono text-xs">{h.name}</TableCell>
                    <TableCell className="text-sm text-muted-foreground">
                      {h.description || "—"}
                    </TableCell>
                    {mayAdminister && (
                      <TableCell>
                        <Button
                          variant="ghost" size="sm" disabled={busy}
                          onClick={() => remove(h.name)}
                        >
                          Remove
                        </Button>
                      </TableCell>
                    )}
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}

        {mayAdminister && (adding ? (
          <div className="space-y-3">
            <div className="grid gap-3 sm:grid-cols-2">
              <label className="space-y-1 text-sm">
                <span className="font-medium">Header name</span>
                <Input
                  value={name} autoFocus
                  placeholder="X-Api-Key"
                  onChange={(e) => setName(e.target.value)}
                />
              </label>
              <label className="space-y-1 text-sm">
                <span className="font-medium">Note (optional)</span>
                <Input
                  value={description}
                  placeholder="Where to generate one"
                  onChange={(e) => setDescription(e.target.value)}
                />
              </label>
            </div>
            <p className="text-sm text-muted-foreground">
              The exact header the server expects — a 401 usually names it. Its
              value is a credential, so it is stored encrypted and asked for on
              the settings page rather than here.
            </p>
            <div className="flex gap-2">
              <Button disabled={busy || !name.trim()} onClick={add}>
                {busy ? "Adding…" : "Add header"}
              </Button>
              <Button
                variant="ghost" disabled={busy}
                onClick={() => { setAdding(false); setName(""); setDescription(""); }}
              >
                Cancel
              </Button>
            </div>
          </div>
        ) : (
          <Button variant="secondary" onClick={() => setAdding(true)}>
            Add a header
          </Button>
        ))}

        {!mayAdminister && headers.length === 0 && (
          <p className="text-sm text-muted-foreground">
            No headers have been added to this server.
          </p>
        )}
      </CardContent>
    </Card>
  );
}

function DiscoveryResult({ diff }: { diff: MCPDiff }) {
  const added = diff.added ?? [];
  const changed = diff.changed ?? [];
  const removed = diff.removed ?? [];
  const unchanged = diff.unchanged ?? [];

  if (added.length + changed.length + removed.length === 0) {
    return (
      <Notice tone="good">
        Nothing changed. {unchanged.length}{" "}
        {unchanged.length === 1 ? "tool is" : "tools are"} exactly as they were.
      </Notice>
    );
  }

  return (
    <Notice tone="attention">
      <div className="space-y-1">
        {added.length > 0 && (
          <p><strong>New:</strong> {added.join(", ")} — pending, and not served.</p>
        )}
        {changed.length > 0 && (
          <p>
            <strong>Changed:</strong> {changed.join(", ")} — the description or
            schema differs, so any decision about the old one no longer applies.
          </p>
        )}
        {removed.length > 0 && (
          <p><strong>Withdrawn by the server:</strong> {removed.join(", ")}.</p>
        )}
      </div>
    </Notice>
  );
}
