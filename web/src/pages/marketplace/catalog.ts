import {
  api, ApiError,
  type CatalogDetail, type CatalogEntry, type CatalogSource,
} from "@/lib/api";

export type { CatalogDetail, CatalogEntry, CatalogSource } from "@/lib/api";

/** One browse or search, as the page asks it. */
export interface CatalogQuery {
  /** Matched by the catalogue, not here. Empty browses everything. */
  search?: string;
  /** Opaque, from a previous answer's `next_cursor`. */
  cursor?: string;
  /** Asks the catalogue again now, rather than reusing what is held. */
  refresh?: boolean;
  limit?: number;
}

/** What the catalogue answered. */
export interface Catalog {
  /** False when every catalogue source is switched off. */
  available: boolean;
  entries: CatalogEntry[];
  /** Absent at the end of the listing. */
  next_cursor?: string;
  /** A floor, not a count. Rendered with a "+". */
  addable_estimate?: number;
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
