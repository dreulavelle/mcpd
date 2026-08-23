import { describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ApiError } from "@/lib/api";
import { renderWith } from "@/test/render";
import { CatalogList, type Installed } from "./CatalogList";
import type { Catalog, CatalogDetail, CatalogEntry, CatalogQuery } from "./catalog";

const WEATHER: CatalogEntry = {
  name: "com.example/weather",
  suggested_name: "weather",
  title: "Weather",
  description: "Forecasts and observations.",
  version: "1.0.0",
  transport: "streamable-http",
  url: "https://weather.example/mcp",
  updated_at: "2026-08-01T10:00:00Z",
  addable: true,
  source: "registry.modelcontextprotocol.io",
};

const TICKETS: CatalogEntry = {
  name: "com.example/tickets",
  suggested_name: "tickets",
  title: "Tickets",
  description: "Raises and reads support tickets.",
  version: "2.0.0",
  transport: "streamable-http",
  url: "https://tickets.example/mcp",
  updated_at: "2026-08-02T10:00:00Z",
  addable: true,
  source: "docker/mcp-registry",
};

/** Published only as something to run locally, which this host does not run. */
const LOCAL_ONLY: CatalogEntry = {
  name: "com.example/notes",
  suggested_name: "notes",
  title: "Notes",
  description: "Reads a notes directory.",
  version: "0.3.0",
  updated_at: "2026-08-03T10:00:00Z",
  addable: false,
  reason: "published only as an npm package, and this host does not run packaged servers",
  source: "registry.modelcontextprotocol.io",
};

function answer(entries: CatalogEntry[], extra: Partial<Catalog> = {}): Catalog {
  return {
    available: true,
    entries,
    source: "registry.modelcontextprotocol.io",
    sources: [{
      source: "registry.modelcontextprotocol.io",
      ok: true, stale: false, entries: entries.length,
    }],
    stale: false,
    retrieved_at: "2026-08-22T09:00:00Z",
    ...extra,
  };
}

function catalog(entries: CatalogEntry[], extra: Partial<Catalog> = {}) {
  return async () => answer(entries, extra);
}

function nothingInstalled(): Installed {
  return { names: new Set(), byAddress: new Map() };
}

function documentFor(entry: CatalogEntry): CatalogDetail {
  return {
    ...entry,
    document: { name: entry.name, version: entry.version },
    stale: false,
    retrieved_at: "2026-08-22T09:00:00Z",
  };
}

async function render(props: Partial<Parameters<typeof CatalogList>[0]> = {}) {
  const view = renderWith(
    <CatalogList
      installed={nothingInstalled()}
      onAdd={() => {}}
      load={catalog([WEATHER, TICKETS])}
      loadDocument={async (name) =>
        documentFor([WEATHER, TICKETS, LOCAL_ONLY].find((e) => e.name === name)!)}
      {...props}
    />,
  );
  return view;
}

describe("browsing the catalogue", () => {
  it("says the catalogue is missing rather than pretending it is empty", async () => {
    await render({
      load: async () => ({
        available: false,
        entries: [],
        source: "",
        sources: [],
        stale: false,
        retrieved_at: "",
        note: "no server catalogue is configured",
      }),
    });

    expect(await screen.findByText("No catalogue here")).toBeInTheDocument();
    expect(screen.getByText("no server catalogue is configured")).toBeInTheDocument();
    expect(screen.queryByLabelText("Search the catalogue")).not.toBeInTheDocument();
  });

  it("distinguishes an empty catalogue from an absent one", async () => {
    await render({ load: catalog([]) });
    expect(await screen.findByText("The catalogue is empty")).toBeInTheDocument();
    // Still browsable: the catalogue answered, it just has nothing today.
    expect(screen.getByLabelText("Search the catalogue")).toBeInTheDocument();
  });

  it("lists what the catalogue offers", async () => {
    await render();
    expect(await screen.findByText("Weather")).toBeInTheDocument();
    expect(screen.getByText("Tickets")).toBeInTheDocument();
  });

  it("reports a catalogue that would not answer, and offers to ask again", async () => {
    const load = vi.fn<(q: CatalogQuery) => Promise<Catalog>>()
      .mockRejectedValueOnce(new ApiError(502, "bad_gateway", "the registry could not be read just now"))
      .mockResolvedValue(answer([WEATHER]));
    await render({ load });

    expect(await screen.findByText(/could not be read just now/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("Weather")).toBeInTheDocument();
  });
});

/**
 * The search is the catalogue's, not this page's.
 *
 * The sources hold thousands of entries between them and answer a page at a
 * time, so a filter over what has already arrived searches one page and calls
 * it the catalogue. And it is debounced, because the alternative is a request
 * to somebody else's server for every keystroke.
 */
describe("searching", () => {
  it("asks the server, rather than filtering what is on screen", async () => {
    const load = vi.fn<(q: CatalogQuery) => Promise<Catalog>>(async (q) =>
      answer(q.search ? [TICKETS] : [WEATHER, TICKETS]));
    await render({ load });

    await screen.findByText("Weather");
    await userEvent.type(screen.getByLabelText("Search the catalogue"), "support");

    await waitFor(() =>
      expect(load).toHaveBeenCalledWith(expect.objectContaining({ search: "support" })));
    await waitFor(() =>
      expect(screen.queryByText("Weather")).not.toBeInTheDocument());
    expect(screen.getByText("Tickets")).toBeInTheDocument();
  });

  it("does not fetch once per keystroke", async () => {
    const load = vi.fn<(q: CatalogQuery) => Promise<Catalog>>(async () => answer([WEATHER]));
    await render({ load });
    await screen.findByText("Weather");

    await userEvent.type(screen.getByLabelText("Search the catalogue"), "weather");
    await waitFor(() =>
      expect(load).toHaveBeenCalledWith(expect.objectContaining({ search: "weather" })));

    // The first browse and one search. Seven keystrokes are not seven requests
    // at a third party.
    expect(load).toHaveBeenCalledTimes(2);
  });
});

/**
 * A page is not the catalogue.
 *
 * The listing is cursored, and an operator who cannot reach the second page
 * cannot reach most of what is published.
 */
describe("paging", () => {
  it("fetches the next page with the cursor and appends it", async () => {
    const load = vi.fn<(q: CatalogQuery) => Promise<Catalog>>(async (q) =>
      q.cursor
        ? answer([TICKETS])
        : answer([WEATHER], { next_cursor: "page-2" }));
    await render({ load });

    await userEvent.click(await screen.findByRole("button", { name: "Show more" }));

    expect(await screen.findByText("Tickets")).toBeInTheDocument();
    // Appended, not replaced.
    expect(screen.getByText("Weather")).toBeInTheDocument();
    expect(load).toHaveBeenCalledWith(expect.objectContaining({ cursor: "page-2" }));
  });

  it("offers nothing more at the end of the listing", async () => {
    await render();
    await screen.findByText("Weather");
    expect(screen.queryByRole("button", { name: "Show more" })).not.toBeInTheDocument();
  });

  /**
   * The sources are merged per page, not across pages, so the same server can
   * arrive twice. Two rows with one React key is a broken list rather than a
   * repeated one.
   */
  it("does not list the same server twice across pages", async () => {
    const load = vi.fn<(q: CatalogQuery) => Promise<Catalog>>(async (q) =>
      q.cursor
        ? answer([WEATHER, TICKETS])
        : answer([WEATHER], { next_cursor: "page-2" }));
    await render({ load });

    await userEvent.click(await screen.findByRole("button", { name: "Show more" }));
    await screen.findByText("Tickets");
    expect(screen.getAllByText("Weather")).toHaveLength(1);
  });
});

/**
 * Roughly half of what the catalogues publish only runs locally, and this host
 * does not run those. Hiding them makes "why isn't the thing I came for here"
 * unanswerable; offering an Add that fails is the thing the server works hard
 * to prevent.
 */
describe("an entry this host cannot accept", () => {
  it("is listed, with the reason, and without an Add", async () => {
    await render({ load: catalog([WEATHER, LOCAL_ONLY]) });

    expect(await screen.findByText("Notes")).toBeInTheDocument();
    expect(screen.getByText(/does not run packaged servers/)).toBeInTheDocument();
    // One Add, for the one entry that can take one.
    expect(screen.getAllByRole("button", { name: "Add" })).toHaveLength(1);
  });
});

/**
 * A catalogue that could not be reached is served from what was last seen,
 * which is the right behaviour and a lie if the page does not mention it.
 */
describe("stale data", () => {
  it("says so, and when it was read", async () => {
    await render({ load: catalog([WEATHER], { stale: true }) });
    expect(await screen.findByText(/what was last seen/i)).toBeInTheDocument();
  });

  it("asks again, bypassing the cache, when refresh is pressed", async () => {
    const load = vi.fn<(q: CatalogQuery) => Promise<Catalog>>(async () =>
      answer([WEATHER], { stale: true }));
    await render({ load });

    await screen.findByText("Weather");
    await userEvent.click(screen.getByRole("button", { name: /Refresh/ }));

    await waitFor(() =>
      expect(load).toHaveBeenCalledWith(expect.objectContaining({ refresh: true })));
  });

  /**
   * A shorter list that does not name the missing catalogue reads as "there is
   * nothing else" rather than as "we could not ask".
   */
  it("names a source that did not answer", async () => {
    await render({
      load: catalog([WEATHER], {
        sources: [
          { source: "registry.modelcontextprotocol.io", ok: true, stale: false, entries: 1 },
          { source: "docker/mcp-registry", ok: false, stale: false, entries: 0, error: "timeout" },
        ],
      }),
    });

    expect(await screen.findByText(/docker\/mcp-registry did not answer/))
      .toBeInTheDocument();
  });
});

/**
 * Discovery, not an inventory.
 *
 * Matched by the address it dials rather than by a name. The official registry
 * calls a server `app.linear/linear` and Docker calls it `linear`, and the
 * local name was chosen here -- so no name identifies an installed server, and
 * the endpoint does.
 */
describe("something already added", () => {
  it("points at where it is managed instead of offering to add it again", async () => {
    const onAdd = vi.fn();
    await render({
      onAdd,
      installed: {
        names: new Set(["forecast"]),
        byAddress: new Map([["https://weather.example/mcp", "forecast"]]),
      },
    });

    await screen.findByText("Weather");
    expect(screen.getByRole("link", { name: /Already added as forecast/ }))
      .toHaveAttribute("href", "/plugins/forecast");
    expect(screen.getAllByRole("button", { name: "Add" })).toHaveLength(1);
  });

  /**
   * A name is not an identity. Many registry names end in `/mcp`, so the same
   * suggestion arrives for unrelated servers -- greying those out would hide a
   * server nobody has added because somebody added a different one.
   */
  it("still offers a different server whose suggested name is taken", async () => {
    await render({
      installed: { names: new Set(["weather"]), byAddress: new Map() },
    });

    await screen.findByText("Weather");
    expect(screen.getAllByRole("button", { name: "Add" })).toHaveLength(2);
    expect(screen.getByText(/is\s+taken here/)).toBeInTheDocument();
  });
});

/**
 * Adding is the caller's job, and the caller runs the same import a pasted
 * document runs. The listing carries no document -- browsing a hundred servers
 * should not mean holding a hundred of them -- so the document is fetched when
 * one is picked and handed straight up.
 */
describe("picking one", () => {
  it("fetches the document and hands it back rather than adding it itself", async () => {
    const onAdd = vi.fn();
    const loadDocument = vi.fn(async () => documentFor(WEATHER));
    await render({ onAdd, load: catalog([WEATHER]), loadDocument });

    await userEvent.click(await screen.findByRole("button", { name: "Add" }));

    await waitFor(() => expect(onAdd).toHaveBeenCalledWith({
      name: "com.example/weather",
      suggested_name: "weather",
      document: { name: "com.example/weather", version: "1.0.0" },
    }));
    expect(loadDocument).toHaveBeenCalledWith("com.example/weather");
  });

  it("says so when the document cannot be read, and adds nothing", async () => {
    const onAdd = vi.fn();
    await render({
      onAdd,
      load: catalog([WEATHER]),
      loadDocument: async () => {
        throw new ApiError(404, "not_found", "the catalogue has no active server by that name");
      },
    });

    await userEvent.click(await screen.findByRole("button", { name: "Add" }));
    expect(await screen.findByText(/no active server by that name/)).toBeInTheDocument();
    expect(onAdd).not.toHaveBeenCalled();
  });
});
