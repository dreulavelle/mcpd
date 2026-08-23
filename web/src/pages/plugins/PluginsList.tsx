import { useCallback, useMemo, useState } from "react";
import { Boxes } from "lucide-react";
import {
  api, ApiError, type Endpoints, type Plugin, type PluginInstance,
  type PluginType,
} from "@/lib/api";
import { useLoader, usePoll } from "@/lib/hooks";
import { Link } from "@/lib/router";
import { useCan } from "@/lib/session";
import {
  CodeBlock, Copyable, EmptyState, Loading, Notice, PageHeader, Section,
} from "@/components/chrome";
import { Chip, healthTone, StatusDot } from "@/components/status";
import { useNotify } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";

/**
 * One row of the list: a configured instance, whether or not it is serving.
 *
 * A plugin that has not been configured is not mounted, so it is absent from
 * the plugins endpoint -- and its own page is exactly where its settings form
 * lives. Without folding the instance list in, adding an integration produced
 * a notice saying what it needed and nowhere to type it.
 */
export interface PluginRow {
  name: string;
  title: string;
  description: string;
  /** "builtin" or "mcp", read from the instances endpoint rather than guessed. */
  runtime: "builtin" | "mcp";
  running: boolean;
  health: string;
  healthMessage: string;
  reads: number;
  writes: number;
}

/**
 * Joins what is mounted with what is merely configured.
 *
 * Runtime comes from the instances endpoint, which is the only place that
 * carries it. An instance with no matching row there is assumed builtin,
 * because that is what every plugin compiled into the binary is and a remote
 * server always has a row.
 */
export function toRows(
  plugins: Plugin[], instances: PluginInstance[], types: PluginType[],
): PluginRow[] {
  const byName = new Map(instances.map((i) => [i.name, i]));
  const rows: PluginRow[] = plugins.map((p) => ({
    name: p.name,
    title: p.title || p.name,
    description: p.description,
    runtime: byName.get(p.name)?.runtime ?? "builtin",
    running: p.endpoint !== "",
    health: p.health,
    healthMessage: p.health_message ?? "",
    reads: p.tools.filter((t) => t.kind === "read").length,
    writes: p.tools.filter((t) => t.kind !== "read").length,
  }));

  const mounted = new Set(plugins.map((p) => p.name));
  for (const i of instances) {
    if (mounted.has(i.name)) continue;
    const type = types.find((t) => t.name === i.type);
    rows.push({
      name: i.name,
      title: type?.title ?? i.type,
      description: type?.description ?? "",
      runtime: i.runtime ?? "builtin",
      running: false,
      health: "unhealthy",
      healthMessage: i.missing?.length
        ? `Waiting on ${i.missing.join(", ")}.`
        : i.problem || (i.enabled ? "Not running yet." : "Switched off."),
      reads: 0,
      writes: 0,
    });
  }

  return rows.sort((a, b) => a.name.localeCompare(b.name));
}

/**
 * Plugins.
 *
 * Split on `runtime` rather than on a name or a type string, because the two
 * kinds are managed in different places and by different people: a builtin has
 * a compiled-in type and a settings form, a remote MCP server has an imported
 * document and a tool list somebody has to classify. Listing them together
 * with no line between them made "why can't I edit this one's tools" a
 * question worth asking.
 */
export function PluginsList() {
  const mayAdd = useCan("admin");
  const [plugins, setPlugins] = useState<Plugin[] | null>(null);
  const [instances, setInstances] = useState<PluginInstance[]>([]);
  const [types, setTypes] = useState<PluginType[]>([]);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);

  const load = useCallback(() => {
    api.plugins()
      .then((r) => { setPlugins(r.plugins ?? []); setError(""); })
      .catch(() => setError("Couldn't load plugins."));
    api.instances().then((r) => setInstances(r.instances ?? [])).catch(() => setInstances([]));
    api.pluginTypes().then((r) => setTypes(r.types ?? [])).catch(() => setTypes([]));
  }, []);
  usePoll(load, 15_000);

  const rows = useMemo(
    () => (plugins ? toRows(plugins, instances, types) : null),
    [plugins, instances, types],
  );
  const builtin = rows?.filter((r) => r.runtime === "builtin") ?? [];
  const remote = rows?.filter((r) => r.runtime === "mcp") ?? [];
  const waiting = instances.filter((i) => i.enabled && !i.mounted);

  return (
    <>
      <PageHeader
        title="Plugins"
        lede="What mcpd can work with, and what each one is set up to reach."
        actions={mayAdd && rows
          ? <Button onClick={() => setAdding(true)}>Add plugin</Button>
          : undefined}
      />

      <AddPlugin
        types={types} open={adding} onOpenChange={setAdding} onAdded={load}
      />

      {error && <Notice tone="problem">{error}</Notice>}

      {/* An instance added since the last start is configured and not serving.
          Saying so is the difference between "waiting" and "broken". */}
      {waiting.length > 0 && (
        <Notice tone="attention">
          <div className="space-y-0.5">
            {waiting.map((i) => (
              <p key={i.name}>
                <strong>{i.name}</strong>
                {i.missing?.length
                  ? ` needs ${i.missing.join(", ")}.`
                  : i.problem ? ` ${i.problem}` : " is not running yet."}
              </p>
            ))}
            <p>It starts serving as soon as it has what it needs — nothing to restart.</p>
          </div>
        </Notice>
      )}

      {rows === null && !error ? (
        <Loading rows={4} />
      ) : rows && rows.length === 0 ? (
        <EmptyState mark={<Boxes />} title="No plugins yet">
          Add one above, or enable it in your startup file and restart.
        </EmptyState>
      ) : (
        <div className="mt-4 space-y-8">
          {builtin.length > 0 && (
            <Section
              title="Built in"
              description="Integrations this binary was built with. They propose changes for approval and mcpd can plan against their state."
            >
              <PluginTable rows={builtin} />
            </Section>
          )}

          {remote.length > 0 && (
            <Section
              title="Remote MCP servers"
              description="Somebody else's servers, mounted as plugins. They cannot propose changes, and only the tools an administrator classified are served."
            >
              <PluginTable rows={remote} />
            </Section>
          )}

          <ConnectingDirectly />
        </div>
      )}
    </>
  );
}

function PluginTable({ rows }: { rows: PluginRow[] }) {
  return (
    <Card className="overflow-hidden p-0">
      <div className="scroll-x">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Plugin</TableHead>
              <TableHead>State</TableHead>
              <TableHead className="text-right">Read</TableHead>
              <TableHead className="text-right">Write</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((row) => (
              <TableRow key={row.name}>
                <TableCell>
                  <Link
                    to={`/plugins/${encodeURIComponent(row.name)}`}
                    className="font-medium hover:underline"
                  >
                    {row.name}
                  </Link>
                  <div className="max-w-[52ch] truncate text-xs text-muted-foreground">
                    {row.title !== row.name ? `${row.title} — ` : ""}
                    {row.description}
                  </div>
                </TableCell>
                <TableCell>
                  {row.running ? (
                    <Chip tone={healthTone(row.health)}>
                      <StatusDot tone={healthTone(row.health)} />
                      {row.health === "healthy" ? "Serving" : row.health}
                    </Chip>
                  ) : (
                    <Chip tone="attention">Not running</Chip>
                  )}
                  {row.health !== "healthy" && row.healthMessage && (
                    <div className="max-w-[40ch] text-xs text-muted-foreground">
                      {row.healthMessage}
                    </div>
                  )}
                </TableCell>
                <TableCell className="text-right tabular-nums">
                  {row.running ? row.reads : <span className="text-muted-foreground">—</span>}
                </TableCell>
                <TableCell className="text-right tabular-nums">
                  {row.running
                    ? (row.writes || <span className="text-muted-foreground">0</span>)
                    : <span className="text-muted-foreground">—</span>}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
    </Card>
  );
}

function ConnectingDirectly() {
  const load = useCallback(() => api.endpoints(), []);
  const { data } = useLoader<Endpoints>(load, "Couldn't load the address.");
  if (!data) return null;
  return (
    <Section title="Connecting directly">
      <Card>
        <CardContent className="space-y-3">
          <p className="text-sm text-muted-foreground">
            For clients that can reach this machine. ChatGPT uses a tunnel
            instead.
          </p>
          <Copyable value={data.aggregate} label="address" />
          <CodeBlock>{"Authorization: Bearer YOUR_KEY"}</CodeBlock>
        </CardContent>
      </Card>
    </Section>
  );
}

function AddPlugin({ types, open, onOpenChange, onAdded }: {
  types: PluginType[];
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onAdded: () => void;
}) {
  const notify = useNotify();
  const [type, setType] = useState("");
  const [name, setName] = useState("");
  const [problem, setProblem] = useState("");
  const [busy, setBusy] = useState(false);

  // Named after its type by default, which is right until there are two.
  const chosen = type || types[0]?.name || "";
  const effective = name.trim() || chosen;

  async function add() {
    setBusy(true);
    setProblem("");
    try {
      const result = await api.addInstance(effective, chosen);
      notify("good", result.note ?? `Added ${effective}.`);
      setName("");
      onOpenChange(false);
      onAdded();
    } catch (e) {
      setProblem(e instanceof ApiError ? e.detail : "Couldn't add it.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Add a plugin</DialogTitle>
          <DialogDescription>
            An instance of an integration this build carries. It records intent;
            it starts serving once it has what it needs.
          </DialogDescription>
        </DialogHeader>

        {problem && <Notice tone="problem">{problem}</Notice>}

        {types.length === 0 ? (
          <Notice tone="attention">
            This build has no integrations compiled in.
          </Notice>
        ) : (
          <div className="space-y-4">
            <div className="space-y-1.5">
              <Label htmlFor="ptype">Integration</Label>
              <NativeSelect
                id="ptype" value={chosen}
                onChange={(e) => setType(e.target.value)}
              >
                {types.map((t) => (
                  <option key={t.name} value={t.name}>{t.title}</option>
                ))}
              </NativeSelect>
              <p className="text-xs text-muted-foreground">
                {types.find((t) => t.name === chosen)?.description}
              </p>
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="pname">Name (optional)</Label>
              <Input
                id="pname" value={name} placeholder={chosen}
                onChange={(e) => setName(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">
                Its endpoint, its tool prefix, and what the history calls it.
                Name it only when you have more than one of the same integration
                — <code className="font-mono">nas-primary</code> and{" "}
                <code className="font-mono">nas-backup</code> rather than two
                things both called {chosen}.
              </p>
            </div>
          </div>
        )}

        <DialogFooter className="sm:justify-start">
          <Button disabled={busy || !chosen} onClick={add}>
            {busy ? "Adding…" : "Add"}
          </Button>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>Cancel</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
