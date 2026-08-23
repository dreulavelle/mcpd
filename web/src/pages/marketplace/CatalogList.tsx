import { useCallback, useMemo, useState } from "react";
import { Store } from "lucide-react";
import { useLoader } from "@/lib/hooks";
import { Link } from "@/lib/router";
import { EmptyState, Loading, Notice, Out } from "@/components/chrome";
import { Chip } from "@/components/status";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { loadCatalog, type CatalogEntry, type CatalogLoader } from "./catalog";

/**
 * The public catalog, browsable and searchable.
 *
 * The one component that knows a catalog exists. Everything it assumes about
 * the not-yet-merged API is in `catalog.ts` beside it; this file only renders
 * what that returns and hands a chosen entry back up. Adding is the caller's
 * job precisely so that it can be the same import the "Add Custom MCP" button
 * runs -- one add path, one set of validation, one classification flow.
 *
 * Search filters what has already been fetched. A catalog large enough to need
 * a server-side query is a change to the loader rather than to this.
 */
export function CatalogList({ installed, onAdd, load = loadCatalog }: {
  /** Names already imported here, so the catalog does not offer them twice. */
  installed: Set<string>;
  onAdd: (entry: CatalogEntry) => void;
  load?: CatalogLoader;
}) {
  const [query, setQuery] = useState("");
  const fetchCatalog = useCallback(() => load(), [load]);
  const { data, error } = useLoader(fetchCatalog, "Couldn't reach the catalog.");

  const shown = useMemo(() => {
    const entries = data?.entries ?? [];
    const needle = query.trim().toLowerCase();
    if (!needle) return entries;
    return entries.filter((e) =>
      [e.title, e.suggestedName, e.description, e.publisher ?? "", ...(e.categories ?? [])]
        .join(" ").toLowerCase().includes(needle));
  }, [data, query]);

  if (error) return <Notice tone="problem">{error}</Notice>;
  if (data === null) return <Loading rows={3} />;

  // Unavailable and empty are different sentences. One says the catalog has
  // nothing to offer; the other says this build never got to ask.
  if (!data.available) {
    return (
      <EmptyState mark={<Store />} title="No catalog yet">
        {data.note
          ?? "This build cannot browse a catalog. Add a server from its published server.json instead."}
      </EmptyState>
    );
  }

  return (
    <div className="space-y-4">
      <Input
        type="search"
        aria-label="Search the catalog"
        placeholder="Search by name, publisher or what it does"
        className="max-w-sm"
        value={query}
        onChange={(e) => setQuery(e.target.value)}
      />

      {shown.length === 0 ? (
        <EmptyState
          mark={<Store />}
          title={data.entries.length === 0 ? "The catalog is empty" : "Nothing matches that"}
        >
          {data.entries.length === 0
            ? "Nothing is published here yet."
            : "Try fewer words, or add the server from its own server.json."}
        </EmptyState>
      ) : (
        <div className="grid gap-3 md:grid-cols-2">
          {shown.map((entry) => (
            <Entry
              key={entry.id}
              entry={entry}
              added={installed.has(entry.suggestedName)}
              onAdd={() => onAdd(entry)}
            />
          ))}
        </div>
      )}
    </div>
  );
}

function Entry({ entry, added, onAdd }: {
  entry: CatalogEntry;
  added: boolean;
  onAdd: () => void;
}) {
  return (
    <Card className="h-full">
      <CardContent className="flex h-full flex-col gap-3">
        <div className="space-y-1">
          <div className="flex flex-wrap items-baseline justify-between gap-2">
            <h3 className="font-medium">{entry.title}</h3>
            {entry.publisher && (
              <span className="text-xs text-muted-foreground">{entry.publisher}</span>
            )}
          </div>
          <p className="text-sm text-muted-foreground">{entry.description}</p>
        </div>

        {entry.categories && entry.categories.length > 0 && (
          <div className="flex flex-wrap gap-1.5">
            {entry.categories.map((c) => <Chip key={c}>{c}</Chip>)}
          </div>
        )}

        <div className="mt-auto flex flex-wrap items-center gap-3">
          {added ? (
            // Already here, so this is not a place to add it from. The link
            // goes where it is managed, which is its plugin page.
            <Link
              to={`/plugins/${encodeURIComponent(entry.suggestedName)}`}
              className="text-sm text-primary hover:underline"
            >
              Already added — manage it
            </Link>
          ) : (
            <Button size="sm" onClick={onAdd}>Add</Button>
          )}
          {entry.homepage && <Out href={entry.homepage}>What it does</Out>}
        </div>
      </CardContent>
    </Card>
  );
}
