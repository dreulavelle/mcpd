import { useEffect, useState } from "react";
import { api, type Grant, type Plugin } from "@/lib/api";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";

/**
 * Which systems a grant covers, and how far.
 *
 * The four places this appears -- an account, a group, a key, a ChatGPT
 * account -- used to be a text box asking for a comma-separated list. That
 * asks somebody to know the exact name of a plugin, spell it, and find out
 * they were wrong only when the grant silently reaches nothing: a name
 * matching no plugin is not an error, it is a grant to a system that does
 * not exist. Offering what the host actually has removes the whole class.
 *
 * Each system is held at read or write. Read calls its read tools; write
 * also proposes changes through it. That is where "read-only" lives, per
 * system, so one key can read Graylog and change cnMaestro.
 *
 * `*` is kept as the wildcard the server understands rather than translated,
 * because it is what the API takes and what the audit trail will show.
 */
export const EVERYTHING = "*";

type Level = Grant["level"];

/** Renders grants the way every list of systems here is rendered. */
export function grantsLabel(grants: Grant[]): string {
  if (grants.length === 0) return "Nothing";
  const wild = grants.find((g) => g.plugin === EVERYTHING);
  const named = grants.filter((g) => g.plugin !== EVERYTHING);
  const parts: string[] = [];
  if (wild) parts.push(wild.level === "write" ? "Everything" : "Everything, read only");
  for (const g of named) {
    if (wild && wild.level === "write") continue;
    parts.push(g.level === "read" ? `${g.plugin} (read)` : g.plugin);
  }
  return parts.join(", ");
}

function levelOf(grants: Grant[], plugin: string): Level | "" {
  return grants.find((g) => g.plugin === plugin)?.level ?? "";
}

function setLevel(grants: Grant[], plugin: string, level: Level | ""): Grant[] {
  const rest = grants.filter((g) => g.plugin !== plugin);
  return level ? [...rest, { plugin, level }] : rest;
}

export function GrantsPicker({ id, value, onChange, subject, disabled }: {
  id: string;
  /** The grants, as the API takes them. */
  value: Grant[];
  onChange: (next: Grant[]) => void;
  /** What is being granted, for the labels: "this key", "everyone in it". */
  subject: string;
  disabled?: boolean;
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
  const mode = wild === "" ? "some" : wild;

  function chooseMode(next: string) {
    if (next === "some") onChange(value.filter((g) => g.plugin !== EVERYTHING));
    else onChange([{ plugin: EVERYTHING, level: next as Level }]);
  }

  const named = value.filter((g) => g.plugin !== EVERYTHING);

  return (
    <div className="space-y-1.5">
      <Label htmlFor={id}>Can reach</Label>
      <NativeSelect id={id} value={mode} disabled={disabled} onChange={(e) => chooseMode(e.target.value)}>
        <option value="some">Only the systems I choose</option>
        <option value="read">Every system on this host, read only</option>
        <option value="write">Every system on this host, read and write</option>
      </NativeSelect>

      {mode === "some" && (
        plugins === null
          ? <p className="text-xs text-muted-foreground">Looking up this host's systems…</p>
          : plugins.length === 0
            ? (
              <p className="text-xs text-muted-foreground">
                This host has no systems to grant yet. Add one on the Plugins
                page, and it will be offered here.
              </p>
            )
            : (
              <fieldset className="space-y-1 rounded-md border p-2">
                <legend className="sr-only">Systems {subject} can reach</legend>
                {plugins.map((p) => {
                  const level = levelOf(value, p.name);
                  return (
                    <div key={p.name} className="flex items-center gap-2 text-sm">
                      <NativeSelect
                        aria-label={`${p.title || p.name} access`}
                        value={level}
                        disabled={disabled}
                        className="w-32"
                        onChange={(e) => onChange(setLevel(value, p.name, e.target.value as Level | ""))}
                      >
                        <option value="">No access</option>
                        <option value="read">Read</option>
                        <option value="write">Read and write</option>
                      </NativeSelect>
                      <span>{p.title || p.name}</span>
                      {/* The name as well as the title: the name is what the
                          grant stores and what an audit record will say. */}
                      {p.title && p.title !== p.name && (
                        <span className="font-mono text-xs text-muted-foreground">{p.name}</span>
                      )}
                    </div>
                  );
                })}
              </fieldset>
            )
      )}

      {mode === "some" && named.length === 0 && (
        <p className="text-xs text-muted-foreground">
          Nothing chosen reaches nothing, which is the safe default rather than
          an incomplete form.
        </p>
      )}
      {mode !== "some" && (
        <p className="text-xs text-muted-foreground">
          {mode === "read"
            ? "Every system's read tools, and no proposing. A system named on a group can still add write."
            : "Every system's read tools and every change it can propose."}
        </p>
      )}
    </div>
  );
}
