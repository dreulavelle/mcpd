import { memo, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { LayoutGrid, List, RefreshCw, Search, Store } from "lucide-react";
import { ApiError } from "@/lib/api";
import { relative, when } from "@/lib/format";
import { cn } from "@/lib/utils";
import { Link } from "@/lib/router";
import { EmptyState, LoadingCards, Notice } from "@/components/chrome";
import { useNotify } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import {
  loadCatalog, loadCatalogEntry,
  type Catalog, type CatalogChoice, type CatalogEntry, type CatalogLoader,
  type CatalogSource, type DocumentLoader,
} from "./catalog";
import { monogram } from "./monogram";

/**
 * What this host already has. Two forms because a name is not an identity and
 * an address is: one catalogue calls a server `app.linear/linear` and another
 * calls it `linear`.
 */
export interface Installed {
  /** Local plugin names in use, so a name collision is caught before the import. */
  names: Set<string>;
  /** Local name, by the address it dials. */
  byAddress: Map<string, string>;
}

/** How long a keystroke waits before it becomes a request. */
const DEBOUNCE_MS = 250;

/** How many entries a page asks for, per density. A sample, not a page. */
const PAGE_SIZE = { cards: 10, rows: 40 } as const;

type Density = keyof typeof PAGE_SIZE;

/**
 * The public catalogues, searchable and sampled.
 *
 * Search and paging are the server's, debounced -- filtering what is on screen
 * would search one page and call it the catalogue. Adding is the caller's job,
 * so a catalogued server goes through the same import a pasted one does.
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
  const [density, setDensity] = useState<Density>("cards");
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
  const size = PAGE_SIZE[density];

  // Held from a browse and ignored during a search, where the server reports
  // the size of the match instead.
  const [catalogueSize, setCatalogueSize] = useState<number>();

  // The next page, fetched at idle. The server over-fetches each source, so it
  // is usually a local cache read.
  const prefetched = useRef<{ key: string; answer: Promise<Catalog> } | null>(null);

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

  // A new question replaces the list rather than appending to it.
  useEffect(() => {
    let current = true;
    prefetched.current = null;
    setBusy("page");
    load({ search: asked.search, refresh: asked.refresh, limit: size }).then(
      (answer) => {
        if (!current) return;
        setPage(answer);
        setEntries(answer.entries);
        if (!asked.search && answer.addable_estimate) {
          setCatalogueSize(answer.addable_estimate);
        }
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
    // `size` is read but deliberately not a dependency: changing density must
    // not re-ask for the page already on screen.
  }, [load, asked]);

  const cursor = page?.next_cursor;
  // JSON rather than a joined string: a search term can contain the separator,
  // and two questions sharing a key spend one's prefetch on the other.
  const nextKey = cursor ? JSON.stringify([asked.search, cursor, size]) : "";

  useEffect(() => {
    if (!cursor || busy || !nextKey) return;
    if (typeof window.requestIdleCallback !== "function") return;
    if (prefetched.current?.key === nextKey) return;
    const id = window.requestIdleCallback(() => {
      const answer = load({ search: asked.search, cursor, limit: size });
      // Attached now so a rejected prefetch is never an unhandled rejection.
      answer.catch(() => {});
      prefetched.current = { key: nextKey, answer };
    }, { timeout: 2_000 });
    return () => window.cancelIdleCallback?.(id);
  }, [asked.search, busy, cursor, load, nextKey, size]);

  const more = useCallback(async () => {
    if (!cursor || busy) return;
    setBusy("more");
    try {
      const held = prefetched.current;
      prefetched.current = null;
      const answer = held?.key === nextKey
        ? await held.answer
        : await load({ search: asked.search, cursor, limit: size });
      if (!live.current) return;
      setPage(answer);
      // Sources are merged per page, not across them, so the same server can
      // arrive twice -- a duplicate React key, not just a repeated row.
      setEntries((prev) => {
        const seen = new Set(prev.map((e) => e.name));
        return [...prev, ...answer.entries.filter((e) => !seen.has(e.name))];
      });
      setError(null);
    } catch (e) {
      if (live.current) setError(message(e, "Couldn't read the next page."));
    } finally {
      if (live.current) setBusy(null);
    }
  }, [asked.search, busy, cursor, load, nextKey, size]);

  const refresh = useCallback(() => {
    prefetched.current = null;
    setAsked((a) => ({ search: a.search, refresh: true, nonce: a.nonce + 1 }));
  }, []);

  /** The listing carries no document, so picking one fetches it. */
  const pick = useCallback(async (entry: CatalogEntry) => {
    setPicking(entry.name);
    try {
      const detail = await loadDocument(entry.name);
      if (!live.current) return;
      onAdd({
        name: detail.name,
        suggested_name: detail.suggested_name || entry.suggested_name,
        document: detail.document,
        entry: { ...entry, ...detail },
      });
    } catch (e) {
      if (live.current) notify("problem", message(e, "Couldn't read that entry."));
    } finally {
      if (live.current) setPicking(null);
    }
  }, [loadDocument, notify, onAdd]);

  const rows = useMemo(() => entries.map((entry) => ({
    entry,
    addedAs: entry.url ? installed.byAddress.get(entry.url) : undefined,
  })), [entries, installed]);

  if (page === null && error === null) return <LoadingCards count={4} />;

  if (page === null) {
    return (
      <div className="space-y-3">
        <Notice tone="problem">{error}</Notice>
        <Button variant="outline" size="sm" onClick={refresh}>Try again</Button>
      </div>
    );
  }

  // Unavailable and empty are different sentences.
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
      <div className="space-y-2">
        <div className="flex flex-wrap items-center gap-2">
          <div className="relative min-w-64 flex-1">
            <Search
              className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground"
              aria-hidden="true"
            />
            <Input
              type="search"
              aria-label="Search the catalogue"
              aria-describedby={catalogueSize ? "catalogue-size" : undefined}
              placeholder="Search by name or what it does"
              className="h-11 pl-9 text-base"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
            />
          </div>
          <DensityToggle value={density} onChange={setDensity} />
          {/* The mark never spins: a turning mark reads as an answer arriving. */}
          <Button
            variant="outline" size="sm"
            disabled={busy !== null}
            onClick={refresh}
          >
            <RefreshCw className="size-3.5" aria-hidden="true" />
            {busy === "page" ? "Asking…" : "Refresh"}
          </Button>
        </div>
        <Size
          count={catalogueSize}
          missing={failed.length}
          retrievedAt={page.retrieved_at}
        />
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
          {/* Dimmed rather than cleared, so a debounce does not flash the page. */}
          <div
            className={cn("transition-opacity", busy === "page" && "opacity-60")}
            aria-busy={busy === "page" || undefined}
          >
            {density === "cards" ? (
              <div className="grid gap-3 md:grid-cols-2">
                {rows.map(({ entry, addedAs }) => (
                  <EntryCard
                    key={entry.name}
                    entry={entry}
                    addedAs={addedAs}
                    picking={picking === entry.name}
                    disabled={picking !== null}
                    onAdd={pick}
                  />
                ))}
              </div>
            ) : (
              <ul className="divide-y rounded-md border">
                {rows.map(({ entry, addedAs }) => (
                  <EntryRow
                    key={entry.name}
                    entry={entry}
                    addedAs={addedAs}
                    picking={picking === entry.name}
                    disabled={picking !== null}
                    onAdd={pick}
                  />
                ))}
              </ul>
            )}
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

/**
 * How big the catalogue is. An estimate that says so on the page: a source that
 * did not answer is not counted, and none of them report how many of their
 * servers this host would accept.
 */
function Size({ count, missing, retrievedAt }: {
  count?: number;
  missing: number;
  retrievedAt: string;
}) {
  if (!count) {
    return retrievedAt
      ? <p className="text-xs text-muted-foreground">Read {relative(retrievedAt)}.</p>
      : null;
  }
  return (
    <p id="catalogue-size" className="text-xs text-muted-foreground">
      <strong className="font-medium text-foreground">
        {count.toLocaleString()}+
      </strong>{" "}
      servers you can add — an estimate, and a low one: not every catalogue
      says how much it holds.
      {missing > 0 && (
        <> {missing === 1 ? "One is" : `${missing} are`} not counted here at
        all, having not answered.</>
      )}
      {retrievedAt && <> Read {relative(retrievedAt)}.</>}
    </p>
  );
}

/** Cards or rows, over the same data. No `animate-`: it reports a preference. */
function DensityToggle({ value, onChange }: {
  value: Density;
  onChange: (next: Density) => void;
}) {
  const options = [
    { key: "cards" as const, label: "Cards", mark: LayoutGrid },
    { key: "rows" as const, label: "Rows", mark: List },
  ];
  return (
    <div className="flex rounded-md border p-0.5" role="group" aria-label="How much to show">
      {options.map(({ key, label, mark: Mark }) => (
        <button
          key={key}
          type="button"
          aria-pressed={value === key}
          onClick={() => onChange(key)}
          className={cn(
            "flex items-center gap-1.5 rounded-sm px-2.5 py-1 text-xs transition-colors",
            "focus-visible:ring-[3px] focus-visible:ring-ring/50 focus-visible:outline-none",
            value === key
              ? "bg-accent text-accent-foreground"
              : "text-muted-foreground hover:text-foreground",
          )}
        >
          <Mark className="size-3.5" aria-hidden="true" />
          {label}
        </button>
      ))}
    </div>
  );
}

/** A catalogue that did not answer, named so a short list is not read as all of it. */
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

/**
 * The picture, or a generated monogram where there is none -- absent, refused
 * or dead all land on the same box, so nothing shifts.
 *
 * `referrerPolicy` because the icon hosts are third parties; `loading="lazy"`
 * so a row never waits on somebody else's image server.
 */
function EntryIcon({ src, name, label, className }: {
  src?: string;
  name: string;
  label: string;
  className?: string;
}) {
  const [failed, setFailed] = useState(false);
  useEffect(() => { setFailed(false); }, [src]);
  const mark = monogram(name, label);

  return (
    <span
      aria-hidden="true"
      className={cn(
        "flex shrink-0 items-center justify-center overflow-hidden rounded-md border",
        src && !failed ? "bg-muted" : "",
        className,
      )}
    >
      {src && !failed ? (
        <img
          src={src}
          alt=""
          loading="lazy"
          decoding="async"
          referrerPolicy="no-referrer"
          className="size-full object-contain"
          onError={() => setFailed(true)}
        />
      ) : (
        // SVG so the letters scale with the box, which is 24px in the compact
        // list and 36px on a card.
        <svg
          viewBox="0 0 24 24" className="size-full"
          style={{ backgroundColor: mark.background }}
        >
          <text
            x="12" y="12" textAnchor="middle" dominantBaseline="central"
            fontSize={mark.text.length > 1 ? 11 : 14} fontWeight="600"
            fill={mark.ink}
          >
            {mark.text}
          </text>
        </svg>
      )}
    </span>
  );
}

/** Add, or a link to where it is already managed. */
function Action({ entry, addedAs, picking, disabled, onAdd }: {
  entry: CatalogEntry;
  addedAs?: string;
  picking: boolean;
  disabled: boolean;
  onAdd: (entry: CatalogEntry) => void;
}) {
  if (addedAs) {
    // Matched by the address it dials: two names, one endpoint, one server.
    return (
      <Link
        to={`/plugins/${encodeURIComponent(addedAs)}`}
        className="shrink-0 text-xs text-primary hover:underline"
      >
        Added as {addedAs}
      </Link>
    );
  }
  if (!entry.addable) {
    return (
      <span className="shrink-0 text-xs text-muted-foreground" title={entry.reason}>
        Can't be added
      </span>
    );
  }
  return (
    <Button size="sm" className="shrink-0" disabled={disabled} onClick={() => onAdd(entry)}>
      {picking ? "Reading…" : "Add"}
    </Button>
  );
}

/** One card: a picture, a name, and what it does. Everything else is in the dialog. */
const EntryCard = memo(function EntryCard({ entry, addedAs, picking, disabled, onAdd }: {
  entry: CatalogEntry;
  addedAs?: string;
  picking: boolean;
  disabled: boolean;
  onAdd: (entry: CatalogEntry) => void;
}) {
  return (
    <Card className={addedAs ? "h-full bg-muted/30" : "h-full"}>
      <CardContent className="flex h-full items-start gap-3">
        <EntryIcon
          src={entry.icon} name={entry.name} label={entry.title}
          className="size-9"
        />
        <div className="min-w-0 flex-1">
          <h3 className="truncate font-medium" title={entry.title || entry.name}>
            {entry.title || entry.name}
          </h3>
          <p className="line-clamp-2 text-sm text-muted-foreground">
            {entry.description}
          </p>
        </div>
        <Action
          entry={entry} addedAs={addedAs}
          picking={picking} disabled={disabled} onAdd={onAdd}
        />
      </CardContent>
    </Card>
  );
});

/**
 * The same entry as one line. Memoised, like the card: every keystroke in the
 * search box re-renders the held list during the debounce.
 */
const EntryRow = memo(function EntryRow({ entry, addedAs, picking, disabled, onAdd }: {
  entry: CatalogEntry;
  addedAs?: string;
  picking: boolean;
  disabled: boolean;
  onAdd: (entry: CatalogEntry) => void;
}) {
  return (
    <li className={cn("flex items-center gap-3 px-3 py-2", addedAs && "bg-muted/30")}>
      <EntryIcon
        src={entry.icon} name={entry.name} label={entry.title}
        className="size-6"
      />
      <span className="w-44 shrink-0 truncate text-sm font-medium" title={entry.title || entry.name}>
        {entry.title || entry.name}
      </span>
      <span className="min-w-0 flex-1 truncate text-sm text-muted-foreground" title={entry.description}>
        {entry.description}
      </span>
      <Action
        entry={entry} addedAs={addedAs}
        picking={picking} disabled={disabled} onAdd={onAdd}
      />
    </li>
  );
});

function message(e: unknown, fallback: string): string {
  return e instanceof ApiError ? e.detail : fallback;
}
