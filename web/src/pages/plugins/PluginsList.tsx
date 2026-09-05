import { useCallback, useId, useMemo, useState } from "react";
import { Boxes } from "lucide-react";
import {
  api,
  type Plugin,
  type PluginInstance,
  type PluginType,
  type StaleRemoval,
  problemText,
} from "@/lib/api";
import { principalWords, when } from "@/lib/format";
import { usePoll } from "@/lib/hooks";
import { Link } from "@/lib/router";
import { parseHealth, statusTone, statusWords } from "@/lib/health";
import { useCan } from "@/lib/session";
import { cn } from "@/lib/utils";
import {
  EmptyState, Loading, Notice, PageHeader, Section,
} from "@/components/chrome";
import { Chip, healthTone, healthWords, StatusDot } from "@/components/status";
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
 * One row: a configured instance, serving or not. An unconfigured plugin is
 * absent from the plugins endpoint, so the instance list is folded in.
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
 * Joins what is mounted with what is merely configured. Runtime comes from the
 * instances endpoint; an instance missing from it is builtin.
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
    // A removed plugin is not on this host, so it is not in the list of what
    // is. The configuration file still lists it, and the way to bring it back
    // is the Add dialog, beside every other plugin that can be added.
    if (i.removed) continue;
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
 * Everything this host serves, split on `runtime`. The split is about trust: a
 * builtin proposes changes mcpd can plan against, a remote server cannot.
 */
export function PluginsList() {
  const mayAdd = useCan("plugins:write");
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
  // By name, title or health, once the list is long enough to need finding in.
  const [query, setQuery] = useState("");
  const q = query.trim().toLowerCase();
  const matches = (r: PluginRow) =>
    !q || [String(r.name ?? ""), String(r.title ?? ""), String(r.health ?? "")].join(" ").toLowerCase().includes(q);
  const builtin = rows?.filter((r) => r.runtime === "builtin" && matches(r)) ?? [];
  const remote = rows?.filter((r) => r.runtime === "mcp" && matches(r)) ?? [];
  // Removed, and the file still lists them. They are not on this host, so they
  // are not in the table; the Add dialog is where they can be added again.
  const removed = instances.filter((i) => i.removed);
  // Two different things to say, so two different notices. An instance nobody
  // has finished filling in is waiting and will serve on its own; one that
  // tried and failed is not, and telling somebody it "starts as soon as it has
  // what it needs" underneath a certificate error reads as the host not having
  // noticed.
  const notServing = instances.filter((i) => i.enabled && !i.mounted);
  const failing = notServing.filter((i) => !i.missing?.length && i.problem);
  const waiting = notServing.filter((i) => i.missing?.length || !i.problem);

  return (
    <>
      <PageHeader
        title="Plugins"
        lede="Every system this host can reach, and what each one is set up to do."
        actions={
          <div className="flex flex-wrap items-center gap-2">
            {(rows?.length ?? 0) > 8 && (
              <Input
                aria-label="Find a plugin"
                className="w-48"
                placeholder="Find a plugin…"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
              />
            )}
            {mayAdd && rows && <Button onClick={() => setAdding(true)}>Add plugin</Button>}
          </div>
        }
      />

      <AddPlugin
        types={types} declared={removed} open={adding}
        onOpenChange={setAdding} onAdded={load}
      />

      {error && <Notice tone="problem">{error}</Notice>}

      {waiting.length > 0 && (
        <Notice tone="attention">
          <div className="space-y-0.5">
            {waiting.map((i) => (
              <p key={i.name}>
                <strong>{i.name}</strong>
                {i.missing?.length
                  ? ` needs ${i.missing.join(", ")}.`
                  : " is not running yet."}
              </p>
            ))}
            <p>It starts serving as soon as it has what it needs — nothing to restart.</p>
          </div>
        </Notice>
      )}

      {failing.length > 0 && (
        <Notice tone="problem">
          <div className="space-y-1.5">
            {failing.map((i) => (
              <p key={i.name}>
                {/* The message is a whole sentence about "this plugin", so
                    the name labels it rather than starting it. Running the
                    two together read "graylog This plugin could not start." */}
                <strong className="block">{i.name}</strong>
                {i.problem}
              </p>
            ))}
            <p className="text-xs">
              It keeps working with its old settings, and picks up the new ones
              as soon as they work.
            </p>
          </div>
        </Notice>
      )}

      <StaleRemovals rows={stale} mayManage={mayAdd} onChanged={load} />

      {rows === null && !error ? (
        <Loading rows={4} />
      ) : rows && rows.length === 0 ? (
        <EmptyState mark={<Boxes />} title="No plugins yet">
          Add one with the button above, or find a remote MCP server in the{" "}
          <Link to="/marketplace" className="text-primary hover:underline">
            Marketplace
          </Link>
          .
        </EmptyState>
      ) : (
        <div className="mt-4 space-y-8">
          {q && builtin.length === 0 && remote.length === 0 && (
            <EmptyState title="No plugin matches that">
              Try part of a name, a type, or a health state.
            </EmptyState>
          )}
          {builtin.length > 0 && (
            <Section
              title="Built in"
              description="Integrations that ship with mcpd. These can suggest changes for someone to approve."
            >
              <PluginTable rows={builtin} />
            </Section>
          )}

          {remote.length > 0 && (
            <Section
              title="Remote MCP servers"
              description="Servers somebody else runs, added here and managed like any other plugin. They cannot suggest changes, and only the tools an administrator has allowed are served."
              actions={mayAdd
                ? (
                  <Link to="/marketplace" className="text-sm text-primary hover:underline">
                    Add one
                  </Link>
                )
                : undefined}
            >
              <PluginTable rows={remote} />
            </Section>
          )}

        </div>
      )}
    </>
  );
}

function HealthSummary({ message }: { message: string }) {
  const h = parseHealth(message);
  if (h.status !== undefined) {
    // The words say what happened; the number is evidence, so it goes in the
    // title rather than into the chip a person is meant to read at a glance.
    return (
      <span className="mt-1 block" title={`HTTP ${h.status} — ${h.title}`}>
        <Chip tone={statusTone(h.status)}>{statusWords(h.status)}</Chip>
      </span>
    );
  }
  return (
    <span className="mt-1 block max-w-[40ch] truncate text-xs text-muted-foreground" title={message}>
      {h.title}
    </span>
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
              <TableHead className="text-right">Read tools</TableHead>
              <TableHead className="text-right">Write tools</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((row) => (
              /* The whole row is the click target, and this is its
                 positioning context. Wrapping the row in an anchor is invalid
                 inside a table and would swallow anything the row holds. */
              <TableRow
                key={row.name}
                className={cn(
                  "relative transition-colors hover:bg-muted/50",
                  // The ring belongs to the row, not to the link text; the
                  // link's own outline is suppressed so there is only one.
                  "has-[a:focus-visible]:bg-muted/50 has-[a:focus-visible]:outline-2",
                  "has-[a:focus-visible]:-outline-offset-2 has-[a:focus-visible]:outline-ring",
                )}
              >
                <TableCell>
                  <Link
                    to={`/plugins/${encodeURIComponent(row.name)}`}
                    className="font-medium outline-none hover:underline"
                  >
                    {/* Raised above the overlay, so the name stays selectable. */}
                    <span className="relative z-10">{row.name}</span>
                    {/* The stretched link. A real element, not a
                        pseudo-element, so a test can click the row's surface. */}
                    <span aria-hidden="true" className="absolute inset-0" />
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
                      {row.health === "healthy" ? "Serving" : healthWords(row.health)}
                    </Chip>
                  ) : (
                    <Chip tone="attention">Not running</Chip>
                  )}
                  {/* The number, where the message names one: a table cell
                      has room for "408" and not for the paragraph that
                      explains it, which is on the plugin's page. */}
                  {row.health !== "healthy" && row.healthMessage && (
                    <HealthSummary message={row.healthMessage} />
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
 * Forgets a removal, which two different acts are made of: adding back a
 * plugin the configuration file still lists, and forgetting a removal whose
 * declaration has left the file. One endpoint, two things to say, so the
 * caller brings its own words rather than the button guessing which it is.
 *
 * The server's own note is not used for the same reason -- it cannot tell the
 * two apart either.
 */
export function ForgetRemovalButton({ name, label, pending, done, failed, onChanged }: {
  name: string;
  label: string;
  pending: string;
  done: string;
  failed: string;
  onChanged: (ok: boolean) => void;
}) {
  const notify = useNotify();
  const [busy, setBusy] = useState(false);

  async function act() {
    setBusy(true);
    let ok = false;
    try {
      await api.restoreInstance(name);
      ok = true;
      notify("good", done);
    } catch (e) {
      notify("problem", problemText(e, failed));
    } finally {
      setBusy(false);
      onChanged(ok);
    }
  }

  return (
    <Button
      variant="outline" size="xs" disabled={busy}
      className="relative z-10 mt-1.5"
      onClick={act}
    >
      {busy ? pending : label}
    </Button>
  );
}

/**
 * Removals whose declaration has left the file. Kept rather than discarded, so
 * one bad deploy does not resurrect everything, and shown because a name that
 * would refuse to come back is worth knowing about.
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
          These were removed here, and the configuration file no longer lists
          them. Nothing is being held back. Forget one to take it off this list.
        </p>
        {rows.map((r) => (
          <p key={r.name} className="flex flex-wrap items-center gap-x-2 text-sm">
            <strong className="font-medium">{r.name}</strong>
            <span className="text-muted-foreground">
              was a {r.declared_type}, removed by {principalWords(r.removed_by)}{" "}
              on {when(r.removed_at)}
            </span>
            {mayManage && (
              <ForgetRemovalButton
                name={r.name} label="Forget" pending="Forgetting…"
                done={`Forgot the removal of ${r.name}.`}
                failed="Couldn't forget it."
                onChanged={onChanged}
              />
            )}
          </p>
        ))}
      </div>
    </Notice>
  );
}

function AddPlugin({ types, declared, open, onOpenChange, onAdded }: {
  types: PluginType[];
  /** Removed here, still listed in the configuration file. */
  declared: PluginInstance[];
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onAdded: () => void;
}) {
  const notify = useNotify();
  // Not a fixed string: a dialog is one instance today and there is nothing
  // stopping a second, and two elements sharing an id give the wrong label.
  const declaredHeading = useId();
  const [type, setType] = useState("");
  const [name, setName] = useState("");
  const [problem, setProblem] = useState("");
  const [busy, setBusy] = useState(false);

  const chosen = type || types[0]?.name || "";
  const effective = name.trim() || chosen;

  // Closing forgets the attempt. A failure left its notice and a half-typed
  // name for the next opening, which read as a new problem with a new plugin.
  function close(open: boolean) {
    if (!open) {
      setProblem("");
      setName("");
      setType("");
    }
    onOpenChange(open);
  }

  async function add() {
    setBusy(true);
    setProblem("");
    try {
      const result = await api.addInstance(effective, chosen);
      notify("good", result.note ?? `Added ${effective}.`);
      close(false);
      onAdded();
    } catch (e) {
      setProblem(problemText(e, "Couldn't add it."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Add a plugin</DialogTitle>
          <DialogDescription>
            Adds an integration that ships with mcpd. It starts working once it
            has what it needs.
          </DialogDescription>
        </DialogHeader>

        {problem && <Notice tone="problem">{problem}</Notice>}

        {declared.length > 0 && (
          <section
            aria-labelledby={declaredHeading}
            className="space-y-2 rounded-md border p-3"
          >
            <p id={declaredHeading} className="text-sm font-medium">
              Listed in the configuration file
            </p>
            <p className="text-xs text-muted-foreground">
              These were removed from this host. Adding one back brings it in
              with nothing entered here.
            </p>
            {declared.map((i) => (
              <div
                key={i.name}
                className="flex flex-wrap items-center justify-between gap-x-3"
              >
                <span className="text-sm">
                  <strong className="font-medium">{i.name}</strong>
                  {i.removed_by && (
                    <span className="text-muted-foreground">
                      {" "}— {principalWords(i.removed_by)} removed it
                      {i.removed_at ? ` on ${when(i.removed_at)}` : ""}
                    </span>
                  )}
                </span>
                <ForgetRemovalButton
                  name={i.name} label="Add" pending="Adding…"
                  done={`Added ${i.name}. Set it up on its page.`}
                  failed="Couldn't add it."
                  onChanged={(added) => { onAdded(); if (added) close(false); }}
                />
              </div>
            ))}
          </section>
        )}

        {types.length === 0 ? (
          <Notice tone="attention">
            This build of mcpd has no integrations to add.
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
                What this one is called everywhere else in mcpd. Name it only
                when you have more than one of the same integration —{" "}
                <code className="font-mono">nas-primary</code> and{" "}
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
