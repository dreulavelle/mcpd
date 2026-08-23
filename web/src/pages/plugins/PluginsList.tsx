import { useCallback, useMemo, useState } from "react";
import { Boxes } from "lucide-react";
import {
  api, ApiError, type Plugin, type PluginInstance, type PluginType,
  type StaleRemoval,
} from "@/lib/api";
import { when } from "@/lib/format";
import { usePoll } from "@/lib/hooks";
import { Link } from "@/lib/router";
import { useCan } from "@/lib/session";
import { cn } from "@/lib/utils";
import {
  EmptyState, Loading, Notice, PageHeader, Section,
} from "@/components/chrome";
import { Chip, healthTone, StatusDot } from "@/components/status";
import { useNotify } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
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
  /**
   * Removed here while the configuration file still declares it.
   *
   * The row stays. An operator who removes the wrong plugin and then cannot
   * find it again to undo is worse off than before the button existed.
   */
  removed: boolean;
  removedBy: string;
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
    removed: false,
    removedBy: "",
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
      // A removed plugin is not waiting on anything and is not broken. Saying
      // "switched off" of it would hide the one fact that explains it, which
      // is that somebody removed it and the file still declares it.
      healthMessage: i.removed
        ? "Removed here. The configuration file still declares it."
        : i.missing?.length
          ? `Waiting on ${i.missing.join(", ")}.`
          : i.problem || (i.enabled ? "Not running yet." : "Switched off."),
      reads: 0,
      writes: 0,
      removed: i.removed ?? false,
      removedBy: i.removed_by ?? "",
    });
  }

  return rows.sort((a, b) => a.name.localeCompare(b.name));
}

/**
 * Everything this host serves.
 *
 * Split on `runtime` rather than on a name or a type string, because the two
 * kinds carry different guarantees: a builtin proposes changes for approval
 * and mcpd can plan against its state, a remote server can do neither and only
 * serves the tools somebody classified. Both are managed from their own page
 * here -- the split is a statement about trust, not about where to go next.
 */
export function PluginsList() {
  const mayAdd = useCan("admin");
  const [plugins, setPlugins] = useState<Plugin[] | null>(null);
  const [instances, setInstances] = useState<PluginInstance[]>([]);
  const [stale, setStale] = useState<StaleRemoval[]>([]);
  const [types, setTypes] = useState<PluginType[]>([]);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);

  const load = useCallback(() => {
    api.plugins()
      .then((r) => { setPlugins(r.plugins ?? []); setError(""); })
      .catch(() => setError("Couldn't load plugins."));
    api.instances()
      .then((r) => {
        setInstances(r.instances ?? []);
        setStale(r.stale_removals ?? []);
      })
      .catch(() => { setInstances([]); setStale([]); });
    api.pluginTypes().then((r) => setTypes(r.types ?? [])).catch(() => setTypes([]));
  }, []);
  usePoll(load, 15_000);

  const rows = useMemo(
    () => (plugins ? toRows(plugins, instances, types) : null),
    [plugins, instances, types],
  );
  const builtin = rows?.filter((r) => r.runtime === "builtin") ?? [];
  const remote = rows?.filter((r) => r.runtime === "mcp") ?? [];
  // A removed instance is not enabled, so it is already out of this -- which
  // is right: it is not waiting on anything, it is not being served on
  // purpose, and its own row says so.
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

      <StaleRemovals rows={stale} mayManage={mayAdd} onChanged={load} />

      {rows === null && !error ? (
        <Loading rows={4} />
      ) : rows && rows.length === 0 ? (
        <EmptyState mark={<Boxes />} title="No plugins yet">
          Add an integration this build carries with the button above, find a
          remote MCP server in the{" "}
          <Link to="/marketplace" className="text-primary hover:underline">
            Marketplace
          </Link>
          , or declare one under <code className="font-mono">plugins:</code> in
          the configuration file — that one needs a restart, because the file is
          read once when the host starts.
        </EmptyState>
      ) : (
        <div className="mt-4 space-y-8">
          {builtin.length > 0 && (
            <Section
              title="Built in"
              description="Integrations this binary was built with. They propose changes for approval and mcpd can plan against their state."
            >
              <PluginTable rows={builtin} mayManage={mayAdd} onChanged={load} />
            </Section>
          )}

          {remote.length > 0 && (
            <Section
              title="Remote MCP servers"
              description="Somebody else's servers, mounted as plugins and managed here like any other. They cannot propose changes, and only the tools an administrator classified are served."
              actions={mayAdd
                ? (
                  <Link to="/marketplace" className="text-sm text-primary hover:underline">
                    Add one
                  </Link>
                )
                : undefined}
            >
              <PluginTable rows={remote} mayManage={mayAdd} onChanged={load} />
            </Section>
          )}

        </div>
      )}
    </>
  );
}

function PluginTable({ rows, mayManage, onChanged }: {
  rows: PluginRow[];
  mayManage: boolean;
  onChanged: () => void;
}) {
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
              /* The whole row is the click target, and the row is the
                 positioning context that makes that work. The alternative --
                 wrapping the row in an anchor -- is invalid inside a table and
                 would put the Restore button inside a link, which breaks both
                 the keyboard and the button. */
              <TableRow
                key={row.name}
                className={cn(
                  "relative transition-colors hover:bg-muted/50",
                  // The focus ring belongs to the row rather than to four
                  // characters of link text, so tabbing through the list shows
                  // where you are. The link's own outline is suppressed
                  // because two rings on one target read as two targets.
                  "has-[a:focus-visible]:bg-muted/50 has-[a:focus-visible]:outline-2",
                  "has-[a:focus-visible]:-outline-offset-2 has-[a:focus-visible]:outline-ring",
                  row.removed && "opacity-70",
                )}
              >
                <TableCell>
                  <Link
                    to={`/plugins/${encodeURIComponent(row.name)}`}
                    className="font-medium outline-none hover:underline"
                  >
                    {/* Raised above the surface below it, so the name can
                        still be selected and copied. An overlay that swallows
                        drag-select makes the one string an operator most often
                        wants to copy impossible to copy. */}
                    <span className="relative z-10">{row.name}</span>
                    {/* The stretched link. A real element rather than a
                        pseudo-element so that a test can click the row's
                        surface the way a person does. */}
                    <span aria-hidden="true" className="absolute inset-0" />
                  </Link>
                  <div className="max-w-[52ch] truncate text-xs text-muted-foreground">
                    {row.title !== row.name ? `${row.title} — ` : ""}
                    {row.description}
                  </div>
                </TableCell>
                <TableCell>
                  {row.removed ? (
                    <Chip>Removed</Chip>
                  ) : row.running ? (
                    <Chip tone={healthTone(row.health)}>
                      <StatusDot tone={healthTone(row.health)} />
                      {row.health === "healthy" ? "Serving" : row.health}
                    </Chip>
                  ) : (
                    <Chip tone="attention">Not running</Chip>
                  )}
                  {(row.removed || row.health !== "healthy") && row.healthMessage && (
                    <div className="max-w-[40ch] text-xs text-muted-foreground">
                      {row.healthMessage}
                    </div>
                  )}
                  {row.removed && mayManage && (
                    <RestoreButton
                      name={row.name} label="Restore" onChanged={onChanged}
                    />
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

/**
 * Undoes a removal that overrode the configuration file.
 *
 * Raised above the row's click surface, so it receives its own clicks and does
 * not also navigate. A button rather than a link, because one of those
 * performs an action and the other goes somewhere, and a control wearing the
 * wrong one is how somebody restores a plugin they meant to open.
 */
export function RestoreButton({ name, label, onChanged }: {
  name: string;
  label: string;
  onChanged: () => void;
}) {
  const notify = useNotify();
  const [busy, setBusy] = useState(false);

  async function restore() {
    setBusy(true);
    try {
      const result = await api.restoreInstance(name);
      notify("good", result.note ?? `Restored ${name}.`);
    } catch (e) {
      notify("problem", e instanceof ApiError ? e.detail : "Couldn't restore it.");
    } finally {
      setBusy(false);
      onChanged();
    }
  }

  return (
    <Button
      variant="outline" size="xs" disabled={busy}
      className="relative z-10 mt-1.5"
      onClick={restore}
    >
      {busy ? "Restoring…" : label}
    </Button>
  );
}

/**
 * Removals with nothing left to remove.
 *
 * A removal is keyed on the name the configuration file used, and it outlives
 * the entry: an operator who removes a plugin here and later deletes the block
 * from their YAML leaves a row that matches nothing. Discarding those
 * automatically would mean one start against a truncated file forgetting every
 * removal and resurrecting all of them on the next good deploy, so they are
 * kept -- and shown here, because a name that would quietly refuse to come
 * back if it were ever declared again is the kind of thing that costs somebody
 * an afternoon.
 */
function StaleRemovals({ rows, mayManage, onChanged }: {
  rows: StaleRemoval[];
  mayManage: boolean;
  onChanged: () => void;
}) {
  if (rows.length === 0) return null;
  return (
    <Notice tone="info">
      <div className="space-y-2">
        <p>
          These were removed here, and the configuration file no longer declares
          them. Nothing is being held back — the removal only bites if the name
          is declared again. Forget one to clear it.
        </p>
        {rows.map((r) => (
          <p key={r.name} className="flex flex-wrap items-center gap-x-2 text-sm">
            <strong className="font-medium">{r.name}</strong>
            <span className="text-muted-foreground">
              was a {r.declared_type}, removed by {r.removed_by} on{" "}
              {when(r.removed_at)}
            </span>
            {mayManage && (
              <RestoreButton name={r.name} label="Forget" onChanged={onChanged} />
            )}
          </p>
        ))}
      </div>
    </Notice>
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
