/**
 * The seam between the marketplace page and the public catalogues.
 *
 * Two functions and one extra fact. The functions are the endpoint, mapped as
 * thinly as it can be mapped; the fact is `available`, which the wire does not
 * carry and the page cannot do without.
 *
 * Three rules keep this a seam rather than a layer:
 *
 *   - `available` is a fact about the catalogue, not about the result. An
 *     empty list from a working catalogue and a build with no catalogue
 *     configured are different things to say to an operator, and one boolean
 *     is what keeps them apart. The server says the second with 503; every
 *     other refusal is an error and is reported as one.
 *   - A listing carries no document. Choosing an entry fetches its
 *     `server.json` and hands that to the same import dialog "Add Custom MCP"
 *     opens, so a catalogued server goes through exactly the validation,
 *     settings derivation and classification flow a pasted one does. There is
 *     no second add path to keep in step, and there must not be.
 *   - Search and paging belong to the server. The catalogues hold thousands of
 *     entries between them and return a page at a time, so filtering an array
 *     the browser already has would search one page of several and call it
 *     the catalogue.
 */

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
  /**
   * How many entries to ask for. The server bounds the merged page to this
   * across every catalogue, which it did not always do: the limit used to be
   * applied by each source independently, so asking for ten returned thirty.
   */
  limit?: number;
}

/** What the catalogue answered. */
export interface Catalog {
  /**
   * Whether this build has a catalogue to consult at all. False for a
   * deployment that switched every source off, which is a sentence rather
   * than an error.
   */
  available: boolean;
  entries: CatalogEntry[];
  /** Absent at the end of the listing. */
  next_cursor?: string;
  /**
   * Roughly how many servers can be added from these catalogues, as a floor.
   * Rendered with a "+" and never as a precise figure — see the field's note
   * in `api.ts`. Absent when nothing could be said.
   */
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

/**
 * What the page hands back when an operator picks an entry.
 *
 * Deliberately three fields. The marketplace does not need to know that a
 * catalogue exists — only that something suggested a name and produced a
 * document, which is the same pair a paste produces.
 */
export interface CatalogChoice {
  /** The catalogue's own name for it, which keys the dialog. */
  name: string;
  suggested_name: string;
  document: unknown;
  /**
   * Everything about the entry that is not on the card.
   *
   * Version, transport, endpoint, credential, catalogue and date used to sit
   * on the listing, where they cost a row each on every card and answered a
   * question nobody was asking while scrolling. They belong in the dialog: it
   * opens when somebody has decided to look at one server, which is the moment
   * the detail is worth the space.
   *
   * It is shown, not acted on. What is imported is `document`, by the same
   * call a pasted one makes.
   */
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
    // Entries and sources default to empty rather than being trusted: this
    // console has twice been blanked by a list the server sent as null, and
    // the cost of the guard is a pair of coalesces.
    return {
      ...page,
      available: true,
      entries: page.entries ?? [],
      sources: page.sources ?? [],
    };
  } catch (e) {
    // 503 is the one refusal that is not a failure: no catalogue is
    // configured, so there is nothing to be down. Anything else -- a
    // catalogue that would not answer, a refused capability -- is an error
    // and is raised as one.
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
