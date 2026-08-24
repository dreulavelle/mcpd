import { useEffect, useState } from "react";
import { api, type Plugin } from "@/lib/api";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";

/**
 * Which systems a grant covers.
 *
 * The three places this appears — an account, a group, a key — used to be a
 * text box asking for a comma-separated list. That asks somebody to know the
 * exact name of a plugin, spell it, and find out they were wrong only when the
 * grant silently reaches nothing: a name matching no plugin is not an error,
 * it is a grant to a system that does not exist. Offering what the host
 * actually has removes the whole class.
 *
 * `*` is kept as the wildcard the server understands rather than translated,
 * because it is what the API takes and what the audit trail will show.
 */
export const EVERYTHING = "*";

export function ReachPicker({ id, value, onChange, subject }: {
  id: string;
  /** The grant, as the API takes it: `["*"]`, or a list of plugin names. */
  value: string[];
  onChange: (next: string[]) => void;
  /** What is being granted, for the labels: "this key", "everyone in it". */
  subject: string;
}) {
  const [plugins, setPlugins] = useState<Plugin[] | null>(null);

  useEffect(() => {
    let live = true;
    api.plugins()
      .then((r) => { if (live) setPlugins(r.plugins ?? []); })
      // A list that cannot be fetched leaves the boxes below unrendered, and
      // the note says so. Silently offering nothing would look like a host
      // with no plugins, which is a different and much more alarming fact.
      .catch(() => { if (live) setPlugins([]); });
    return () => { live = false; };
  }, []);

  const everything = value.includes(EVERYTHING);
  const chosen = new Set(value);

  function toggle(name: string, on: boolean) {
    const next = new Set(value.filter((v) => v !== EVERYTHING));
    if (on) next.add(name);
    else next.delete(name);
    onChange([...next]);
  }

  return (
    <div className="space-y-1.5">
      <Label htmlFor={id}>Can reach</Label>
      <NativeSelect
        id={id}
        value={everything ? "all" : "some"}
        onChange={(e) => onChange(e.target.value === "all" ? [EVERYTHING] : [])}
      >
        <option value="some">Only the systems I choose</option>
        <option value="all">Every system on this host</option>
      </NativeSelect>

      {!everything && (
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
                {plugins.map((p) => (
                  <label key={p.name} className="flex items-center gap-2 text-sm">
                    <input
                      type="checkbox"
                      checked={chosen.has(p.name)}
                      onChange={(e) => toggle(p.name, e.target.checked)}
                    />
                    <span>{p.title || p.name}</span>
                    {/* The name as well as the title: the name is what the
                        grant stores and what an audit record will say. */}
                    {p.title && p.title !== p.name && (
                      <span className="font-mono text-xs text-muted-foreground">
                        {p.name}
                      </span>
                    )}
                  </label>
                ))}
              </fieldset>
            )
      )}

      {!everything && value.length === 0 && (
        <p className="text-xs text-muted-foreground">
          Nothing chosen reaches nothing, which is the safe default rather than
          an incomplete form.
        </p>
      )}
    </div>
  );
}
