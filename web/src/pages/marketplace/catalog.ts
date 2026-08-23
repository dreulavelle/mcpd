/**
 * The seam between the marketplace page and the public catalog.
 *
 * THE CATALOG API IS NOT MERGED YET. It is being built in parallel, so this
 * file is the whole of what the page assumes about it: a type for an entry, a
 * type for an answer, and one function that returns the answer. Today that
 * function reports the catalog as unavailable and the page says so.
 *
 * Wiring it up is meant to be a change to `loadCatalog` and nothing else. Two
 * rules keep it that way:
 *
 *   - An entry carries the published `server.json` verbatim, in `document`.
 *     Adding from the catalog hands that document to the same import dialog
 *     "Add Custom MCP" opens, so a catalogued server goes through exactly the
 *     validation, settings derivation and classification flow a pasted one
 *     does. There is no second add path to keep in step, and there must not be.
 *   - `available` is a fact about the catalog, not about the result. An empty
 *     list from a working catalog and a catalog this build cannot reach are
 *     different things to say to an operator, and one boolean is what keeps
 *     them apart.
 */

/** One server the catalog offers. */
export interface CatalogEntry {
  /** Stable within the catalog. Identifies the entry, not the installation. */
  id: string;
  /**
   * What to call it here: its endpoint path, its tool prefix, and its entry in
   * a credential's plugin list. Not the document's reverse-DNS name, which is
   * not a legal path segment.
   */
  suggestedName: string;
  title: string;
  description: string;
  /** Who publishes it, when the catalog says. */
  publisher?: string;
  /** The project's own page, for reading before adding. */
  homepage?: string;
  /** Free-form tags the search matches on as well as the text. */
  categories?: string[];
  /**
   * The published server.json, exactly as the catalog holds it.
   *
   * Unknown rather than a shape: this build validates a document against its
   * own vendored schema at import, and a type here would be a second, weaker
   * opinion about what a valid one looks like.
   */
  document: unknown;
}

/** What the catalog answered. */
export interface Catalog {
  /**
   * Whether this build could consult a catalog at all. False today, because
   * the endpoint does not exist yet.
   */
  available: boolean;
  entries: CatalogEntry[];
  /** Why it is unavailable, in words an operator can act on. */
  note?: string;
}

/** How the page asks. Injectable so a test can supply a catalog. */
export type CatalogLoader = () => Promise<Catalog>;

/**
 * Asks the catalog what it offers.
 *
 * Replace the body with the fetch when the endpoint lands -- something like
 * `request<Catalog>("/api/mcp-catalog")` in `lib/api.ts`, mapped to these
 * types here. Nothing else on the page needs to change.
 */
export const loadCatalog: CatalogLoader = async () => ({
  available: false,
  entries: [],
  note: "This build has no catalog to browse yet. Add a server from its published server.json in the meantime.",
});
