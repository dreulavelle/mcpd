import { useCallback, useEffect, useRef, useState } from "react";
import { RefreshCw, Store } from "lucide-react";
import { ApiError } from "@/lib/api";
import { relative, when } from "@/lib/format";
import { cn } from "@/lib/utils";
import { Link } from "@/lib/router";
import { EmptyState, LoadingCards, Notice } from "@/components/chrome";
import { Chip } from "@/components/status";
import { useNotify } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import {
  loadCatalog, loadCatalogEntry,
  type Catalog, type CatalogChoice, type CatalogEntry, type CatalogLoader,
  type CatalogSource, type DocumentLoader,
} from "./catalog";

/**
 * What this host already has, in the two forms the catalogue can be compared
 * against.
 *
 * Two, because a name is not an identity and an address is. The catalogues
 * merge across each other by URL for exactly this reason -- the official
 * registry calls a server `app.linear/linear` and Docker calls it `linear` --
 * and the same rule is what tells this page whether the thing in front of the
 * operator is already here. The names are the weaker question and a different
 * one: whether the suggested name is free.
 */
export interface Installed {
  /** Local plugin names in use, so a name collision is caught before the import. */
  names: Set<string>;
  /** Local name, by the address it dials. */
  byAddress: Map<string, string>;
}

/** How long a keystroke waits before it becomes a request. */
const DEBOUNCE_MS = 250;

/**
 * The public catalogues, browsable and searchable.
 *
 * The one component that knows a catalogue exists. Everything it assumes about
 * the API is in `catalog.ts` beside it; this file renders what that returns
 * and hands a chosen document back up. Adding is the caller's job precisely so
 * that it can be the same import the "Add Custom MCP" button runs -- one add
 * path, one set of validation, one classification flow.
 *
 * Three things here are not decoration:
 *
 *   - Search is the catalogue's, debounced. The sources hold thousands of
 *     entries and answer a page at a time, so filtering what is on screen
 *     would search one page and call it the catalogue -- and a request per
 *     keystroke would be a request per keystroke at somebody else's server.
 *   - An entry this host cannot accept is shown, greyed, with the reason. Half
 *     of what the catalogues publish only runs locally, and "why is the thing
 *     I came for not here" is a worse question than a row that answers it.
 *   - Stale is said out loud. A catalogue that could not be reached is served
 *     from what was last seen, which is the right behaviour and a lie if the
 *     page does not mention it.
 */
export function CatalogList({
  installed,
  onAdd,
  load = loadCatalog,
  loadDocument = loadCatalogEntry,
}: {
  installed: Installed;
  onAdd: (choice: CatalogChoice) => void;
  load?: CatalogLoader;
  loadDocument?: DocumentLoader;
}) {
  const notify = useNotify();
  const [search, setSearch] = useState("");
  // What was actually asked, which trails the box by a debounce. `nonce`
  // separates "ask again" from "ask something else": pressing refresh with an
  // unchanged query still has to reach the server.
  const [asked, setAsked] = useState({ search: "", refresh: false, nonce: 0 });
  const [page, setPage] = useState<Catalog | null>(null);
  const [entries, setEntries] = useState<CatalogEntry[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<"page" | "more" | null>("page");
  const [picking, setPicking] = useState<string | null>(null);
  const live = useRef(true);

  useEffect(() => {
    live.current = true;
    return () => { live.current = false; };
  }, []);

  useEffect(() => {
    const t = setTimeout(() => {
      const next = search.trim();
      setAsked((a) => (a.search === next ? a : { search: next, refresh: false, nonce: a.nonce + 1 }));
    }, DEBOUNCE_MS);
    return () => clearTimeout(t);
  }, [search]);

  // A new question replaces the list. Appending would leave the previous
  // search's results underneath the new one's.
  useEffect(() => {
    let current = true;
    setBusy("page");
    load({ search: asked.search, refresh: asked.refresh }).then(
      (answer) => {
        if (!current) return;
        setPage(answer);
        setEntries(answer.entries);
        setError(null);
        setBusy(null);
      },
      (e) => {
        if (!current) return;
        setError(message(e, "Couldn't reach the catalogue."));
        setBusy(null);
      },
    );
    return () => { current = false; };
  }, [load, asked]);

  const more = useCallback(async () => {
    const cursor = page?.next_cursor;
    if (!cursor || busy) return;
    setBusy("more");
    try {
      const answer = await load({ search: asked.search, cursor });
      if (!live.current) return;
      setPage(answer);
      // Deduplicated on the way in. The sources are merged per page rather
      // than across pages, so the same server arriving twice is a duplicate
      // React key -- which is a broken list rather than a repeated row.
      setEntries((prev) => {
        const held = new Set(prev.map((e) => e.name));
        return [...prev, ...answer.entries.filter((e) => !held.has(e.name))];
      });
      setError(null);
    } catch (e) {
      if (live.current) setError(message(e, "Couldn't read the next page."));
    } finally {
      if (live.current) setBusy(null);
    }
  }, [asked.search, busy, load, page]);

  const refresh = useCallback(() => {
    setAsked((a) => ({ search: a.search, refresh: true, nonce: a.nonce + 1 }));
  }, []);

  /**
   * Picking one.
   *
   * The listing has no document -- browsing a hundred servers should not mean
   * holding a hundred `server.json` files -- so the document is fetched here
   * and handed straight to the caller's import dialog.
   */
  const pick = useCallback(async (entry: CatalogEntry) => {
    setPicking(entry.name);
    try {
      const detail = await loadDocument(entry.name);
      if (!live.current) return;
      onAdd({
        name: detail.name,
        suggested_name: detail.suggested_name || entry.suggested_name,
        document: detail.document,
      });
    } catch (e) {
      if (live.current) notify("problem", message(e, "Couldn't read that entry."));
    } finally {
      if (live.current) setPicking(null);
    }
  }, [loadDocument, notify, onAdd]);

  // Nothing is known yet, not even whether there is a catalogue. A skeleton
  // rather than a spinner, because the shape of what is coming is known.
  if (page === null && error === null) return <LoadingCards count={4} />;

  if (page === null) {
    return (
      <div className="space-y-3">
        <Notice tone="problem">{error}</Notice>
        <Button variant="outline" size="sm" onClick={refresh}>Try again</Button>
      </div>
    );
  }

  // Unavailable and empty are different sentences. One says the catalogue has
  // nothing to offer; the other says this build never got to ask.
  if (!page.available) {
    return (
      <EmptyState mark={<Store />} title="No catalogue here">
        {page.note}
      </EmptyState>
    );
  }

  const failed = page.sources.filter((s) => !s.ok);

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <Input
          type="search"
          aria-label="Search the catalogue"
          placeholder="Search by name or what it does"
          className="max-w-sm"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
        {/* The mark never spins. This control is about whether a third
            party answered, and a mark that turns reads as an answer arriving
            -- the label is what says the request is in flight. */}
        <Button
          variant="outline" size="sm"
          disabled={busy !== null}
          onClick={refresh}
        >
          <RefreshCw className="size-3.5" aria-hidden="true" />
          {busy === "page" ? "Asking…" : "Refresh"}
        </Button>
        <Fetched page={page} shown={entries.length} />
      </div>

      {page.stale && (
        <Notice tone="info">
          <strong>This is what was last seen.</strong> The catalogue could not
          be reached just now, so these entries are from{" "}
          {when(page.retrieved_at)} ({relative(page.retrieved_at)}). Nothing
          here is broken — press Refresh to ask again.
        </Notice>
      )}

      {failed.length > 0 && <SourcesDown sources={failed} />}

      {error && <Notice tone="problem">{error}</Notice>}

      {entries.length === 0 ? (
        <EmptyState
          mark={<Store />}
          title={asked.search ? "Nothing matches that" : "The catalogue is empty"}
        >
          {asked.search
            ? "Try fewer words, or add the server from its own server.json."
            : "The catalogues answered, and none of them lists anything."}
        </EmptyState>
      ) : (
        <>
          {/* What is on screen stays there while the next answer is fetched.
              Clearing to a skeleton on every keystroke's worth of debounce
              would flash the page at somebody who is reading it; dimming says
              the same thing without taking the words away. */}
          <div
            className={cn(
              "grid gap-3 transition-opacity md:grid-cols-2",
              busy === "page" && "opacity-60",
            )}
            aria-busy={busy === "page" || undefined}
          >
            {entries.map((entry) => (
              <Entry
                key={entry.name}
                entry={entry}
                addedAs={entry.url ? installed.byAddress.get(entry.url) : undefined}
                nameTaken={installed.names.has(entry.suggested_name)}
                picking={picking === entry.name}
                disabled={picking !== null}
                onAdd={() => pick(entry)}
              />
            ))}
          </div>

          {busy === "more" && <LoadingCards count={2} />}

          {page.next_cursor && (
            <div className="flex justify-center">
              <Button variant="outline" disabled={busy !== null} onClick={more}>
                {busy === "more" ? "Loading…" : "Show more"}
              </Button>
            </div>
          )}
        </>
      )}
    </div>
  );
}

/** Where these entries came from and when, in one quiet line. */
function Fetched({ page, shown }: {
  page: { source: string; retrieved_at: string };
  shown: number;
}) {
  if (!page.source) return null;
  return (
    <p className="text-xs text-muted-foreground">
      {shown} from {page.source}
      {page.retrieved_at && <>, read {relative(page.retrieved_at)}</>}
    </p>
  );
}

/**
 * A catalogue that did not answer.
 *
 * Named rather than left out. A shorter list that does not say a source is
 * missing reads as "there is nothing else" rather than as "we could not ask",
 * and the operator deciding whether to wait needs to know which.
 */
function SourcesDown({ sources }: { sources: CatalogSource[] }) {
  return (
    <Notice tone="attention">
      <strong>{sources.map((s) => s.source).join(" and ")} did not answer.</strong>{" "}
      Nothing {sources.length === 1 ? "it lists" : "they list"} is on this
      page, so what is below is shorter than the catalogue — not the whole of
      it.
    </Notice>
  );
}

function Entry({ entry, addedAs, nameTaken, picking, disabled, onAdd }: {
  entry: CatalogEntry;
  /** The local name it is already installed under, matched by address. */
  addedAs?: string;
  /** Whether the name it suggests is taken here by something else. */
  nameTaken: boolean;
  picking: boolean;
  disabled: boolean;
  onAdd: () => void;
}) {
  return (
    <Card className={addedAs || !entry.addable ? "h-full bg-muted/30" : "h-full"}>
      <CardContent className="flex h-full flex-col gap-3">
        <div className="space-y-1">
          <div className="flex flex-wrap items-baseline justify-between gap-2">
            <h3 className="font-medium">{entry.title || entry.name}</h3>
            <span className="text-xs text-muted-foreground">{entry.source}</span>
          </div>
          <p className="text-sm text-muted-foreground">{entry.description}</p>
        </div>

        <dl className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
          {entry.version && (
            <div className="flex gap-1">
              <dt className="sr-only">Version</dt>
              <dd className="font-mono">{entry.version}</dd>
            </div>
          )}
          {entry.transport && (
            <div className="flex gap-1">
              <dt className="sr-only">Transport</dt>
              <dd>{entry.transport}</dd>
            </div>
          )}
          {entry.updated_at && (
            <div className="flex gap-1">
              <dt>Updated</dt>
              <dd>{when(entry.updated_at)}</dd>
            </div>
          )}
        </dl>

        {entry.url && (
          <p className="truncate font-mono text-xs text-muted-foreground" title={entry.url}>
            {entry.url}
          </p>
        )}

        {/* The reason, whenever there is one. An Add button that cannot work
            is the thing the server works hard to avoid offering, so the page
            must not put one back. */}
        {!entry.addable && (
          <Notice tone="neutral">
            <strong>Not addable here.</strong>{" "}
            {entry.reason || "This host cannot serve it."}
          </Notice>
        )}

        <div className="mt-auto flex flex-wrap items-center gap-3">
          {addedAs ? (
            // Matched by the address it dials, not by the name: two entries
            // that reach one endpoint are one server however they are named.
            <Link
              to={`/plugins/${encodeURIComponent(addedAs)}`}
              className="text-sm text-primary hover:underline"
            >
              Already added as {addedAs} — manage it
            </Link>
          ) : entry.addable ? (
            <>
              <Button size="sm" disabled={disabled} onClick={onAdd}>
                {picking ? "Reading…" : "Add"}
              </Button>
              {nameTaken && (
                <span className="text-xs text-attention">
                  <code className="font-mono">{entry.suggested_name}</code> is
                  taken here — you will be asked for another name.
                </span>
              )}
            </>
          ) : (
            <Chip>Can't be added</Chip>
          )}
        </div>
      </CardContent>
    </Card>
  );
}

function message(e: unknown, fallback: string): string {
  return e instanceof ApiError ? e.detail : fallback;
}
