import {
  api, ApiError,
  type CatalogDetail, type CatalogEntry, type CatalogSource,
} from "@/lib/api";

export type { CatalogDetail, CatalogEntry, CatalogSource } from "@/lib/api";

/**
 * The orders a listing can be asked for.
 *
 * "" is the default: a few entries from each catalogue, in whatever order each
 * one considers best. The other three are the server's own vocabulary, sent
 * back unchanged.
 */
export type CatalogSort = "" | "most-used" | "recently-updated" | "name";

/** One browse or search, as the page asks it. */
export interface CatalogQuery {
  /** Matched by the catalogue, not here. Empty browses everything. */
  search?: string;
  /** Opaque, from a previous answer's `next_cursor`. */
  cursor?: string;
  /** Asks the catalogue again now, rather than reusing what is held. */
  refresh?: boolean;
  limit?: number;
  /** How the page is ordered. Empty takes the default. */
  sort?: CatalogSort;
  /** One catalogue by name. Empty covers them all. */
  source?: string;
}

/** What the catalogue answered. */
export interface Catalog {
  /** False when every catalogue source is switched off. */
  available: boolean;
  entries: CatalogEntry[];
  /** Absent at the end of the listing. */
  next_cursor?: string;
  /** A floor, not a count. Rendered with a "+". */
  addable?: number;
  /** Every catalogue that answered, as one line. */
  source: string;
  /** How each of them fared, so a shorter page can say why it is shorter. */
  sources: CatalogSource[];
  /** True when this is what was last seen rather than what is there now. */
  stale: boolean;
  retrieved_at: string;
  /** Why there is no catalogue, when there is none. */
  note?: string;
}

/** What the page hands back when an operator picks an entry. */
export interface CatalogChoice {
  /** The catalogue's own name for it, which keys the dialog. */
  name: string;
  suggested_name: string;
  document: unknown;
  /** Everything about the entry that is not on the card. Shown, not acted on. */
  entry: CatalogEntry;
}

/** How the page browses. Injectable so a test can supply a catalogue. */
export type CatalogLoader = (query: CatalogQuery) => Promise<Catalog>;

/** How the page fetches the document behind an entry. */
export type DocumentLoader = (name: string) => Promise<CatalogDetail>;

/** The sentence for a build with no catalogue, when the server sends none. */
const NO_CATALOG =
  "This build has no catalogue to browse. Add a server from its published " +
  "server.json instead.";

export const loadCatalog: CatalogLoader = async (query) => {
  try {
    const page = await api.catalog({
      search: query.search,
      cursor: query.cursor,
      refresh: query.refresh,
      limit: query.limit,
      sort: query.sort,
      source: query.source,
    });
    // The server sends null, not [], when nothing matches.
    return {
      ...page,
      available: true,
      entries: page.entries ?? [],
      sources: page.sources ?? [],
    };
  } catch (e) {
    // 503 is the one refusal that is not a failure: no catalogue is
    // configured, so there is nothing to be down.
    if (e instanceof ApiError && e.status === 503) {
      return {
        available: false,
        entries: [],
        source: "",
        sources: [],
        stale: false,
        retrieved_at: "",
        note: e.detail || NO_CATALOG,
      };
    }
    throw e;
  }
};

export const loadCatalogEntry: DocumentLoader = (name) => api.catalogEntry(name);

/**
 * The same order the server put one page in, applied to every page loaded so
 * far.
 *
 * The server orders the page it assembles, and it cannot do more than that: the
 * catalogues hold twenty-odd thousand entries behind cursors this host cannot
 * sort. So pressing Show more brings back a second page ordered among itself,
 * and without this the list on screen would run A-Z and then start again at A.
 *
 * This does not make the order any more global than it was. What is ordered is
 * what has been loaded, which is what the line under the control says.
 *
 * By name it collates the way the reader's browser does rather than the way
 * the server compares bytes. The two agree on everything ASCII and the list on
 * screen is this one either way, applied whole, so it is never half of each.
 */
export function orderEntries(entries: CatalogEntry[], sort: CatalogSort): CatalogEntry[] {
  if (!sort) return entries;
  const byName = (a: CatalogEntry, b: CatalogEntry) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0);
  const label = (e: CatalogEntry) => e.title || e.name;
  const ordered = [...entries];
  switch (sort) {
    case "most-used":
      ordered.sort((a, b) => {
        // Absent is not zero. A catalogue that publishes no figure has not
        // measured the server at nought calls, so its entries go last rather
        // than among the ones that have been counted and are unused.
        if (a.uses === undefined && b.uses === undefined) return byName(a, b);
        if (a.uses === undefined) return 1;
        if (b.uses === undefined) return -1;
        return b.uses - a.uses || byName(a, b);
      });
      break;
    case "recently-updated":
      ordered.sort((a, b) => {
        // An entry with no date is not an old entry, for the same reason.
        const at = Date.parse(a.updated_at ?? "");
        const bt = Date.parse(b.updated_at ?? "");
        if (Number.isNaN(at) && Number.isNaN(bt)) return byName(a, b);
        if (Number.isNaN(at)) return 1;
        if (Number.isNaN(bt)) return -1;
        return bt - at || byName(a, b);
      });
      break;
    case "name":
      ordered.sort((a, b) => label(a).localeCompare(label(b)) || byName(a, b));
      break;
  }
  return ordered;
}
