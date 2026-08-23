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
import { NativeSelect } from "@/components/ui/native-select";
import {
  loadCatalog, loadCatalogEntry, orderEntries,
  type Catalog, type CatalogChoice, type CatalogEntry, type CatalogLoader,
  type CatalogSort, type CatalogSource, type DocumentLoader,
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
 * The orders offered, in the order they are offered.
 *
 * "Most used" is not always among them. It is shown only where a catalogue in
 * view counts how often its servers are called -- see `counted` below --
 * because an order over a figure nobody publishes would be a control that
 * rearranges nothing and says nothing about why.
 */
const ORDERS: { key: CatalogSort; label: string }[] = [
  { key: "", label: "A bit of each" },
  { key: "most-used", label: "Most used" },
  { key: "recently-updated", label: "Recently updated" },
  { key: "name", label: "Name" },
];

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
  //
  // The order and the scope are in here rather than beside it because all four
  // decide which entries a page holds, and a change to any of them starts the
  // listing again -- a cursor is a position in one particular question.
  const [asked, setAsked] = useState<{
    search: string;
    sort: CatalogSort;
    source: string;
    refresh: boolean;
    nonce: number;
  }>({ search: "", sort: "", source: "", refresh: false, nonce: 0 });
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

  // The catalogues this host browses, learned from the first unscoped answer.
  //
  // Held rather than read from the page in hand, because a scoped page reports
  // only the catalogue it covers -- rightly, since the others were not asked --
  // and the control that undoes the scope has to keep offering them.
  const [catalogues, setCatalogues] = useState<CatalogSource[]>([]);

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
      setAsked((a) => (a.search === next ? a : { ...a, search: next, refresh: false, nonce: a.nonce + 1 }));
    }, DEBOUNCE_MS);
    return () => clearTimeout(t);
  }, [search]);

  // A new question replaces the list rather than appending to it.
  useEffect(() => {
    let current = true;
    prefetched.current = null;
    setBusy("page");
    load({
      search: asked.search, refresh: asked.refresh, limit: size,
      sort: asked.sort, source: asked.source,
    }).then(
      (answer) => {
        if (!current) return;
        setPage(answer);
        setEntries(answer.entries);
        // The figure belongs to what is in view, so a scoped browse replaces
        // it rather than leaving the whole catalogue's number over one
        // catalogue's list.
        if (!asked.search) setCatalogueSize(answer.addable_estimate);
        // Only an unscoped answer knows the whole set.
        if (!asked.source && answer.sources.length > 0) setCatalogues(answer.sources);
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
  const nextKey = cursor
    ? JSON.stringify([asked.search, asked.sort, asked.source, cursor, size])
    : "";

  useEffect(() => {
    if (!cursor || busy || !nextKey) return;
    if (typeof window.requestIdleCallback !== "function") return;
    if (prefetched.current?.key === nextKey) return;
    const id = window.requestIdleCallback(() => {
      const answer = load({
        search: asked.search, cursor, limit: size,
        sort: asked.sort, source: asked.source,
      });
      // Attached now so a rejected prefetch is never an unhandled rejection.
      answer.catch(() => {});
      prefetched.current = { key: nextKey, answer };
    }, { timeout: 2_000 });
    return () => window.cancelIdleCallback?.(id);
  }, [asked.search, asked.sort, asked.source, busy, cursor, load, nextKey, size]);

  const more = useCallback(async () => {
    if (!cursor || busy) return;
    setBusy("more");
    try {
      const held = prefetched.current;
      prefetched.current = null;
      const answer = held?.key === nextKey
        ? await held.answer
        : await load({
          search: asked.search, cursor, limit: size,
          sort: asked.sort, source: asked.source,
        });
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
  }, [asked.search, asked.sort, asked.source, busy, cursor, load, nextKey, size]);

  const refresh = useCallback(() => {
    prefetched.current = null;
    setAsked((a) => ({ ...a, refresh: true, nonce: a.nonce + 1 }));
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

  const rows = useMemo(() => orderEntries(entries, asked.sort).map((entry) => ({
    entry,
    addedAs: entry.url ? installed.byAddress.get(entry.url) : undefined,
  })), [asked.sort, entries, installed]);

  // Which catalogues in view count how often a server is called. Read from the
  // response rather than named here, because which of them does is this
  // deployment's configuration and not the dashboard's business.
  const counted = useMemo(
    () => catalogues
      .filter((c) => c.uses && (!asked.source || c.source === asked.source))
      .map((c) => c.source),
    [asked.source, catalogues],
  );
  const orders = useMemo(
    () => ORDERS.filter((o) => o.key !== "most-used" || counted.length > 0),
    [counted],
  );

  const reorder = useCallback((sort: CatalogSort) => {
    setAsked((a) => (a.sort === sort ? a : { ...a, sort, refresh: false, nonce: a.nonce + 1 }));
  }, []);

  /**
   * Scoping to one catalogue, which can withdraw the order in force: only some
   * of them count calls. The order changes with it rather than becoming a
   * request the server would refuse, and the control shows the change.
   */
  const scope = useCallback((source: string) => {
    setAsked((a) => {
      if (a.source === source) return a;
      const stillCounted = catalogues.some(
        (c) => c.uses && (!source || c.source === source),
      );
      const sort = a.sort === "most-used" && !stillCounted ? "" : a.sort;
      return { ...a, source, sort, refresh: false, nonce: a.nonce + 1 };
    });
  }, [catalogues]);

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
          {catalogues.length > 1 && (
            <NativeSelect
              aria-label="Which catalogue"
              className="h-9 w-auto"
              value={asked.source}
              onChange={(e) => scope(e.target.value)}
            >
              <option value="">All catalogues</option>
              {catalogues.map((c) => (
                <option key={c.source} value={c.source}>{c.source}</option>
              ))}
            </NativeSelect>
          )}
          <NativeSelect
            aria-label="Order"
            className="h-9 w-auto"
            value={asked.sort}
            onChange={(e) => reorder(e.target.value as CatalogSort)}
          >
            {orders.map((o) => (
              <option key={o.key || "default"} value={o.key}>{o.label}</option>
            ))}
          </NativeSelect>
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
        <Ordering sort={asked.sort} counted={counted} scoped={asked.source !== ""} />
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
 * What the order in force actually covers.
 *
 * The line exists because none of these orders covers the whole catalogue, and
 * a list that looks sorted with nothing saying how far the sorting reaches is
 * the thing this feature must not be. Two different truths, so two sentences.
 *
 * Most used is narrowed rather than approximated: only some catalogues count
 * calls, and the ones that do not are left out rather than ranked at zero. So
 * the line names the one being counted, which is also why the number on the row
 * is a number and not a badge -- both are checkable.
 *
 * By name and by date reach as far as what has been loaded and no further.
 * Ordering twenty-four thousand entries held behind four catalogues' own
 * cursors is not something this host can do, and saying "sorted" flat would
 * claim it had.
 */
function Ordering({ sort, counted, scoped }: {
  sort: CatalogSort;
  counted: string[];
  scoped: boolean;
}) {
  if (!sort) return null;
  if (sort === "most-used") {
    return (
      <p className="text-xs text-muted-foreground">
        Calls counted by {counted.join(" and ") || "the catalogue"}.
        {!scoped && counted.length > 0 && " The other catalogues don't count them, so they aren't in this list."}
      </p>
    );
  }
  return (
    <p className="text-xs text-muted-foreground">
      In order within what you have loaded, not across the whole catalogue.
    </p>
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
 * A generated monogram, and only that.
 *
 * The catalogues offer an icon URL and this used to render it. It never
 * reached the page: the dashboard's own `img-src 'self' data:` forbids a
 * third-party image, so every row has always drawn the monogram. Relaxing the
 * header so a catalogue entry could make an operator's browser call out to an
 * address a third party chose -- telling that party which servers are being
 * looked at -- buys a picture and costs more than one. See
 * docs/architecture.md.
 */
function EntryIcon({ name, label, className }: {
  name: string;
  label: string;
  className?: string;
}) {
  const mark = monogram(name, label);

  return (
    <span
      aria-hidden="true"
      className={cn(
        "flex shrink-0 items-center justify-center overflow-hidden rounded-md border",
        className,
      )}
    >
      {/* SVG so the letters scale with the box, which is 24px in the compact
          list and 36px on a card. */}
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
    </span>
  );
}

/**
 * How many times the entry's catalogue has been asked to call the server.
 *
 * The number itself, because a number is checkable and a badge is not: "87,579
 * calls" says what was counted and by whom, where a star or a flame asks to be
 * believed. It is also what explains the most-used order without a legend.
 *
 * Nothing at all where the catalogue publishes no figure. Zero would say the
 * server was measured and never called, which would be this host making a
 * number up.
 */
function Uses({ entry }: { entry: CatalogEntry }) {
  if (entry.uses === undefined) return null;
  return (
    <span
      className="shrink-0 text-xs whitespace-nowrap text-muted-foreground tabular-nums"
      title={`Calls to this server, counted by ${entry.source}`}
    >
      {entry.uses.toLocaleString()} calls
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
        <EntryIcon name={entry.name} label={entry.title} className="size-9" />
        <div className="min-w-0 flex-1">
          <div className="flex items-baseline gap-2">
            <h3 className="truncate font-medium" title={entry.title || entry.name}>
              {entry.title || entry.name}
            </h3>
            <Uses entry={entry} />
          </div>
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
      <EntryIcon name={entry.name} label={entry.title} className="size-6" />
      <span className="w-44 shrink-0 truncate text-sm font-medium" title={entry.title || entry.name}>
        {entry.title || entry.name}
      </span>
      <span className="min-w-0 flex-1 truncate text-sm text-muted-foreground" title={entry.description}>
        {entry.description}
      </span>
      <Uses entry={entry} />
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
