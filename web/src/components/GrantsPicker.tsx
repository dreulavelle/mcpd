import { useEffect, useState } from "react";
import { api, type Grant, type Plugin } from "@/lib/api";
import { Segmented } from "@/components/Segmented";
import { Label } from "@/components/ui/label";

/**
 * Which systems a grant covers, and how far.
 *
 * One row per system this host has, each a segmented None / Read / Write,
 * under a row for every system at once. Read calls a system's read tools;
 * write also proposes changes through it. That is where "read-only" lives,
 * per system, so one key can read Graylog and change cnMaestro.
 *
 * Offering what the host actually has, rather than a text box, removes a
 * whole class of mistake: a name matching no plugin is not an error, it is
 * a grant to a system that does not exist.
 *
 * `*` is kept as the wildcard the server understands rather than translated,
 * because it is what the API takes and what the audit trail will show.
 */
export const EVERYTHING = "*";

type Level = Grant["level"] | "none";

/** Renders grants the way every list of systems here is rendered. */
export function grantsLabel(grants: Grant[]): string {
  if (grants.length === 0) return "Nothing";
  const wild = grants.find((g) => g.plugin === EVERYTHING);
  const named = grants.filter((g) => g.plugin !== EVERYTHING);
  if (wild?.level === "write") return "Everything";
  const parts: string[] = [];
  if (wild) parts.push("Everything, read only");
  for (const g of named) parts.push(g.level === "read" ? `${g.plugin} (read)` : g.plugin);
  return parts.join(", ");
}

function levelOf(grants: Grant[], plugin: string): Level {
  return grants.find((g) => g.plugin === plugin)?.level ?? "none";
}

function setLevel(grants: Grant[], plugin: string, level: Level): Grant[] {
  const rest = grants.filter((g) => g.plugin !== plugin);
  return level === "none" ? rest : [...rest, { plugin, level }];
}

const OPTIONS: { value: Level; label: string }[] = [
  { value: "none", label: "None" },
  { value: "read", label: "Read" },
  { value: "write", label: "Write" },
];

export function GrantsPicker({ id, value, onChange, subject, disabled, readOnly }: {
  id: string;
  /** The grants, as the API takes them. */
  value: Grant[];
  onChange?: (next: Grant[]) => void;
  /** What is being granted, for the labels: "this key", "everyone in it". */
  subject: string;
  disabled?: boolean;
  readOnly?: boolean;
}) {
  const [plugins, setPlugins] = useState<Plugin[] | null>(null);

  useEffect(() => {
    let live = true;
    api.plugins()
      .then((r) => { if (live) setPlugins(r.plugins ?? []); })
      // A list that cannot be fetched leaves the rows below unrendered, and
      // the note says so. Silently offering nothing would look like a host
      // with no plugins, which is a different and much more alarming fact.
      .catch(() => { if (live) setPlugins([]); });
    return () => { live = false; };
  }, []);

  const wild = levelOf(value, EVERYTHING);
  const named = value.filter((g) => g.plugin !== EVERYTHING);

  return (
    <div className="space-y-2">
      <Label htmlFor={id}>Can reach</Label>
      <div id={id} className="divide-y rounded-lg border">
        <Row
          name="Every system"
          hint={wild === "write" ? "Read and change through all of them." : wild === "read" ? "Read tools on all of them; write on the ones set below." : "Set per system below."}
          value={wild}
          label={`Every system ${subject} can reach`}
          disabled={disabled} readOnly={readOnly}
          onChange={(l) => onChange?.(l === "none" ? named : [{ plugin: EVERYTHING, level: l }, ...named.filter((g) => !levelIncludes(l, g.level))])}
          emphasis
        />
        {plugins === null ? (
          <p className="px-4 py-3 text-xs text-muted-foreground">Looking up this host's systems…</p>
        ) : plugins.length === 0 ? (
          <p className="px-4 py-3 text-xs text-muted-foreground">
            No systems yet. Add one under Plugins and it is offered here.
          </p>
        ) : plugins.map((p) => {
          const own = levelOf(value, p.name);
          const effective = levelIncludes(wild, own) ? wild : own;
          return (
            <Row
              key={p.name}
              name={p.title || p.name}
              code={p.title && p.title !== p.name ? p.name : undefined}
              value={effective}
              label={`${p.title || p.name} access`}
              disabled={disabled || (wild === "write")}
              readOnly={readOnly}
              onChange={(l) => onChange?.(setLevel(value, p.name, levelIncludes(wild, l) ? "none" : l))}
            />
          );
        })}
      </div>
      {!readOnly && wild === "none" && named.length === 0 && (
        <p className="text-xs text-muted-foreground">Nothing chosen reaches nothing.</p>
      )}
    </div>
  );
}

function levelIncludes(held: Level, need: Level): boolean {
  const rank = (l: Level) => (l === "write" ? 2 : l === "read" ? 1 : 0);
  return need !== "none" && rank(held) >= rank(need);
}

function Row({ name, code, hint, value, label, onChange, disabled, readOnly, emphasis }: {
  name: string;
  code?: string;
  hint?: string;
  value: Level;
  label: string;
  onChange: (l: Level) => void;
  disabled?: boolean;
  readOnly?: boolean;
  emphasis?: boolean;
}) {
  return (
    <div className={`flex items-center gap-4 px-4 py-2.5 ${emphasis ? "bg-muted/30" : ""}`}>
      <div className="min-w-0 flex-1">
        <span className={`text-sm ${emphasis ? "font-medium" : ""}`}>{name}</span>
        {/* The name as well as the title: the name is what the grant stores
            and what an audit record will say. */}
        {code && <span className="ml-2 font-mono text-xs text-muted-foreground">{code}</span>}
        {hint && <div className="text-xs text-muted-foreground">{hint}</div>}
      </div>
      <Segmented<Level>
        label={label} value={value} options={OPTIONS}
        onChange={onChange} disabled={disabled} readOnly={readOnly}
      />
    </div>
  );
}
