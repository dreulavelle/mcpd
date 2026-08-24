import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
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
  auth: "api_key",
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
 * does not run those.
 *
 * They used to be listed, greyed, with the reason. That was the right answer
 * at thirty rows a page and the wrong one at ten: a page of ten that spends
 * five rows explaining refusals is a page of five, and the operator who
 * complained had used it and found the noise worse than the missing answer.
 *
 * So the server leaves them out of a listing, ahead of the paging. Nothing
 * about the machinery changed -- the entry still carries `addable` and its
 * reason, and asking for one by name still explains the refusal in full.
 */
describe("an entry this host cannot accept", () => {
  it("is not something the page has to render, because a listing has none", async () => {
    await render({ load: catalog([WEATHER, TICKETS]) });

    await screen.findByText("Weather");
    // Two entries, two Adds. No row spends itself on a refusal.
    expect(screen.getAllByRole("button", { name: "Add" })).toHaveLength(2);
    expect(screen.queryByText(/does not run packaged servers/)).not.toBeInTheDocument();
  });

  /**
   * Belt and braces. `?include_unaddable=1` is still an endpoint an operator
   * can reach, and an Add button that cannot work is the one thing the server
   * works hardest not to offer -- so if one ever arrives here, it does not get
   * one.
   */
  it("is shown without an Add if one arrives anyway", async () => {
    await render({ load: catalog([WEATHER, LOCAL_ONLY]) });

    expect(await screen.findByText("Notes")).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "Add" })).toHaveLength(1);
    expect(screen.getByText("Can't be added")).toBeInTheDocument();
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
    expect(screen.getByRole("link", { name: /Added as forecast/ }))
      .toHaveAttribute("href", "/plugins/forecast");
    expect(screen.getAllByRole("button", { name: "Add" })).toHaveLength(1);
  });

  /**
   * A name is not an identity. Many registry names end in `/mcp`, so the same
   * suggestion arrives for unrelated servers -- greying those out would hide a
   * server nobody has added because somebody added a different one.
   *
   * The warning about the collision is not here any more. It was a sentence on
   * a card, about a field on a form the operator had not opened yet; the
   * dialog already says the same thing beside the box they would fix it in.
   */
  it("still offers a different server whose suggested name is taken", async () => {
    await render({
      installed: { names: new Set(["weather"]), byAddress: new Map() },
    });

    await screen.findByText("Weather");
    expect(screen.getAllByRole("button", { name: "Add" })).toHaveLength(2);
    expect(screen.queryByText(/taken here/)).not.toBeInTheDocument();
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

    await waitFor(() => expect(onAdd).toHaveBeenCalledWith(expect.objectContaining({
      name: "com.example/weather",
      suggested_name: "weather",
      document: { name: "com.example/weather", version: "1.0.0" },
    })));
    expect(loadDocument).toHaveBeenCalledWith("com.example/weather");

    // And the entry itself, because everything the card stopped showing is
    // shown in the dialog this opens.
    const choice = onAdd.mock.calls[0]![0] as { entry: CatalogEntry };
    expect(choice.entry).toMatchObject({
      version: "1.0.0",
      transport: "streamable-http",
      url: "https://weather.example/mcp",
      auth: "api_key",
      source: "registry.modelcontextprotocol.io",
    });
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

/**
 * The page asks for ten.
 *
 * It used to ask for the server's default of thirty and get ninety, because
 * the limit was applied by each catalogue independently rather than to the
 * merged page. Both halves of that are fixed, and this is the half that lives
 * here: the page says how many it wants, every time it asks.
 */
describe("how much is asked for", () => {
  it("asks for ten at a time", async () => {
    const load = vi.fn<(q: CatalogQuery) => Promise<Catalog>>(async () =>
      answer([WEATHER], { next_cursor: "page-2" }));
    await render({ load });

    await screen.findByText("Weather");
    expect(load).toHaveBeenCalledWith(expect.objectContaining({ limit: 10 }));

    await userEvent.click(screen.getByRole("button", { name: "Show more" }));
    await waitFor(() =>
      expect(load).toHaveBeenCalledWith(expect.objectContaining({ cursor: "page-2", limit: 10 })));
  });

  /**
   * Rows are for scanning, so a page of them is a screenful of lines rather
   * than a screenful of cards. Changing density does not re-ask for what is
   * already on screen -- that would throw away a page somebody is reading to
   * render the same servers differently.
   */
  it("asks for more per page in the compact view, without re-asking for this one", async () => {
    const load = vi.fn<(q: CatalogQuery) => Promise<Catalog>>(async () =>
      answer([WEATHER], { next_cursor: "page-2" }));
    await render({ load });

    await screen.findByText("Weather");
    expect(load).toHaveBeenCalledTimes(1);

    await userEvent.click(screen.getByRole("button", { name: "Rows" }));
    expect(load).toHaveBeenCalledTimes(1);

    await userEvent.click(screen.getByRole("button", { name: "Show more" }));
    await waitFor(() =>
      expect(load).toHaveBeenCalledWith(expect.objectContaining({ limit: 40 })));
  });

  it("keeps Show more in both densities rather than scrolling forever", async () => {
    await render({ load: catalog([WEATHER], { next_cursor: "page-2" }) });

    await screen.findByText("Weather");
    expect(screen.getByRole("button", { name: "Show more" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Rows" }));
    expect(screen.getByRole("button", { name: "Show more" })).toBeInTheDocument();
  });
});

/**
 * A card is a monogram, a name and a description.
 *
 * Everything else -- version, transport, endpoint, credential, catalogue, date
 * -- was six facts to a card and most of a card, and it answered a question
 * nobody asks while scrolling. It is all in the dialog Add opens.
 *
 * The catalogue's name is gone rather than moved: an operator picking a server
 * does not care which of four public lists this host read it from.
 */
describe("what a card shows", () => {
  it("shows the name and what it does, and nothing else", async () => {
    await render({ load: catalog([WEATHER]) });

    expect(await screen.findByText("Weather")).toBeInTheDocument();
    expect(screen.getByText("Forecasts and observations.")).toBeInTheDocument();

    expect(screen.queryByText("registry.modelcontextprotocol.io")).not.toBeInTheDocument();
    expect(screen.queryByText("streamable-http")).not.toBeInTheDocument();
    expect(screen.queryByText("https://weather.example/mcp")).not.toBeInTheDocument();
    expect(screen.queryByText("1.0.0")).not.toBeInTheDocument();
  });

  /**
   * The picture is a generated monogram and nothing else.
   *
   * The catalogues offer an icon URL and this used to render one. It never
   * reached the page: the dashboard sends `img-src 'self' data:`, so a
   * third-party image has always been blocked and every row has always drawn
   * the monogram. Relaxing the header for decoration would tell whichever
   * host the entry named which servers an operator is looking at.
   */
  it("draws a monogram for every entry, and asks no third party for a picture", async () => {
    await render({ load: catalog([WEATHER, TICKETS]) });

    await screen.findByText("Tickets");
    expect(document.querySelector("img")).toBeNull();
    const marks = [...document.querySelectorAll("svg text")].map((t) => t.textContent);
    expect(marks).toEqual(["WE", "TI"]);
  });

  // One box per entry, the same size, so nothing on the row moves.
  it("gives every entry the same box", async () => {
    await render({ load: catalog([WEATHER, TICKETS]) });

    await screen.findByText("Tickets");
    expect([...document.querySelectorAll("span.size-9")]).toHaveLength(2);
  });
});

/**
 * How big the catalogue is.
 *
 * The other half of the "only 90 here" bug. Ten rows out of twelve thousand
 * look exactly like a catalogue of ten, and the operator who read thirty rows
 * as ninety servers was reading the page correctly -- the page was wrong.
 *
 * The figure is an estimate and says so, because it has to be: two of the four
 * catalogues report no size at all, and none of them says how many of its
 * servers this host would accept. The "+" is doing real work.
 */
describe("the size of the catalogue", () => {
  it("says roughly how many can be added, as a floor", async () => {
    await render({ load: catalog([WEATHER], { addable_estimate: 7900 }) });

    expect(await screen.findByText("7,900+")).toBeInTheDocument();
    expect(screen.getByText(/an estimate, and a low one/)).toBeInTheDocument();
  });

  it("says nothing rather than guessing when nothing could be said", async () => {
    await render({ load: catalog([WEATHER]) });

    await screen.findByText("Weather");
    expect(screen.queryByText(/servers you can add/)).not.toBeInTheDocument();
  });

  /**
   * A total that does not move when a catalogue goes down is worse than a
   * smaller one, so a source that did not answer is not counted -- and the
   * line says as much, rather than quietly shrinking.
   */
  it("says when it is short a catalogue that did not answer", async () => {
    await render({
      load: catalog([WEATHER], {
        addable_estimate: 140,
        sources: [
          { source: "docker/mcp-registry", ok: true, stale: false, entries: 1 },
          { source: "registry.smithery.ai", ok: false, stale: false, entries: 0, error: "timeout" },
        ],
      }),
    });

    expect(await screen.findByText(/not counted here at all/)).toBeInTheDocument();
  });

  /**
   * It is a fact about the catalogues rather than about the answer, and it is
   * most useful while somebody is typing -- which is exactly when the server
   * is reporting the size of the *match* instead.
   */
  it("keeps the browse figure while a search is running", async () => {
    const load = vi.fn<(q: CatalogQuery) => Promise<Catalog>>(async (q) =>
      q.search
        ? answer([TICKETS], { addable_estimate: 3 })
        : answer([WEATHER], { addable_estimate: 7900 }));
    await render({ load });

    await screen.findByText("7,900+");
    await userEvent.type(screen.getByLabelText("Search the catalogue"), "tickets");

    await screen.findByText("Tickets");
    expect(screen.getByText("7,900+")).toBeInTheDocument();
  });
});

/**
 * The next page, fetched while nothing else is happening.
 *
 * Cheap in a way worth knowing: the server asks each source for twice what a
 * page needs, so the page after this one is almost always the same upstream
 * answer read from a different offset -- a cache read, no third party
 * involved.
 *
 * Only where `requestIdleCallback` exists. A browser without one gets the
 * ordinary fetch on click rather than a timer racing the page it is meant to
 * be staying out of the way of, which is also what keeps this deterministic.
 */
describe("prefetching the next page", () => {
  function withIdleCallback() {
    const pending: (() => void)[] = [];
    vi.stubGlobal("requestIdleCallback", (fn: () => void) => {
      pending.push(fn);
      return pending.length;
    });
    vi.stubGlobal("cancelIdleCallback", () => {});
    return {
      // Waits for the page to have asked for idle time before granting it.
      // Running whatever happens to be pending at the moment of the call is a
      // race: the effect that registers the callback runs after the render
      // that `findByText` waits for, usually within the same tick and not
      // always -- which is a test that passes on a quiet machine and fails on
      // a loaded CI runner.
      run: async () => {
        await waitFor(() => expect(pending.length).toBeGreaterThan(0));
        pending.splice(0).forEach((fn) => fn());
      },
      get count() { return pending.length; },
    };
  }

  afterEach(() => { vi.unstubAllGlobals(); });

  it("has the next page in hand before Show more is pressed", async () => {
    const idle = withIdleCallback();
    const load = vi.fn<(q: CatalogQuery) => Promise<Catalog>>(async (q) =>
      q.cursor ? answer([TICKETS]) : answer([WEATHER], { next_cursor: "page-2" }));
    await render({ load });

    await screen.findByText("Weather");
    await idle.run();
    await waitFor(() =>
      expect(load).toHaveBeenCalledWith(expect.objectContaining({ cursor: "page-2" })));
    expect(load).toHaveBeenCalledTimes(2);

    // Pressing it spends what was already fetched rather than asking again.
    await userEvent.click(screen.getByRole("button", { name: "Show more" }));
    expect(await screen.findByText("Tickets")).toBeInTheDocument();
    expect(load).toHaveBeenCalledTimes(2);
  });

  it("does not spend a held page on a different question", async () => {
    const idle = withIdleCallback();
    const load = vi.fn<(q: CatalogQuery) => Promise<Catalog>>(async (q) =>
      q.search
        ? answer([TICKETS], { next_cursor: "search-2" })
        : answer([WEATHER], { next_cursor: "page-2" }));
    await render({ load });

    await screen.findByText("Weather");
    await idle.run();
    await waitFor(() => expect(load).toHaveBeenCalledTimes(2));

    await userEvent.type(screen.getByLabelText("Search the catalogue"), "tickets");
    await screen.findByText("Tickets");

    await userEvent.click(screen.getByRole("button", { name: "Show more" }));
    await waitFor(() =>
      expect(load).toHaveBeenCalledWith(expect.objectContaining({
        search: "tickets", cursor: "search-2",
      })));
  });
});

/**
 * Ordering and scoping.
 *
 * Everything here is one rule seen from different sides: a list may only claim
 * the order it actually has. The number on a row is checkable, the catalogues
 * that publish no number are named as absent rather than ranked at the bottom,
 * and an order that reaches only as far as what has been loaded says so.
 */

/** A Smithery-shaped row: hosted, keyed, and carrying a call count. */
function counted(name: string, uses: number, extra: Partial<CatalogEntry> = {}): CatalogEntry {
  return {
    name,
    suggested_name: name,
    title: name,
    description: "Hosted by Smithery.",
    version: "",
    url: `https://server.smithery.ai/${name}/mcp`,
    updated_at: "2026-08-01T10:00:00Z",
    uses,
    addable: true,
    auth: "api_key",
    source: "registry.smithery.ai",
    ...extra,
  };
}

/** Three catalogues, one of which counts calls. */
const THREE_CATALOGUES: Catalog["sources"] = [
  { source: "registry.modelcontextprotocol.io", ok: true, stale: false, entries: 1 },
  { source: "docker/mcp-registry", ok: true, stale: false, entries: 1 },
  { source: "registry.smithery.ai", ok: true, stale: false, entries: 1, uses: true },
];

describe("ordering the catalogue", () => {
  it("shows the count itself, so the order is something an operator can check", async () => {
    await render({ load: catalog([counted("brave", 87_579), WEATHER]) });

    expect(await screen.findByText("87,579 calls")).toBeInTheDocument();
    // And nothing invented for the catalogue that publishes no figure.
    expect(screen.queryByText("0 calls")).not.toBeInTheDocument();
  });

  it("offers most used only where a catalogue counts calls", async () => {
    const { unmount } = await render({
      load: catalog([WEATHER], { sources: THREE_CATALOGUES }),
    });
    expect(await screen.findByRole("option", { name: "Most used" })).toBeInTheDocument();
    unmount();

    await render({ load: catalog([WEATHER]) });
    await screen.findByText("Weather");
    expect(screen.queryByRole("option", { name: "Most used" })).not.toBeInTheDocument();
    // The orders that need nothing from a catalogue are always there.
    expect(screen.getByRole("option", { name: "Name" })).toBeInTheDocument();
  });

  it("asks the server for the order rather than rearranging one page", async () => {
    const load = vi.fn<(q: CatalogQuery) => Promise<Catalog>>()
      .mockResolvedValue(answer([WEATHER], { sources: THREE_CATALOGUES }));
    await render({ load });
    await screen.findByText("Weather");

    fireEvent.change(screen.getByLabelText("Order"), { target: { value: "most-used" } });
    await waitFor(() =>
      expect(load).toHaveBeenCalledWith(expect.objectContaining({ sort: "most-used" })));
  });

  it("says what a most-used list covers, and what it leaves out", async () => {
    await render({ load: catalog([counted("brave", 87_579)], { sources: THREE_CATALOGUES }) });
    await screen.findByText("87,579 calls");

    fireEvent.change(screen.getByLabelText("Order"), { target: { value: "most-used" } });
    expect(await screen.findByText(/Calls counted by registry\.smithery\.ai/)).toBeInTheDocument();
    expect(screen.getByText(/other catalogues don't count them/)).toBeInTheDocument();
  });

  it("does not claim an order it does not have", async () => {
    await render({ load: catalog([WEATHER, TICKETS]) });
    await screen.findByText("Weather");

    fireEvent.change(screen.getByLabelText("Order"), { target: { value: "name" } });
    expect(await screen.findByText(/not across the whole catalogue/)).toBeInTheDocument();
  });

  it("keeps a second page in order rather than starting the alphabet again", async () => {
    const load = vi.fn<(q: CatalogQuery) => Promise<Catalog>>()
      .mockResolvedValueOnce(answer([TICKETS, WEATHER], { next_cursor: "page-2" }))
      .mockResolvedValueOnce(answer([TICKETS, WEATHER], { next_cursor: "page-2" }))
      .mockResolvedValue(answer([
        counted("atlas", 4, { title: "Atlas", source: "docker/mcp-registry" }),
      ]));
    await render({ load });
    await screen.findByText("Tickets");

    fireEvent.change(screen.getByLabelText("Order"), { target: { value: "name" } });
    await screen.findByText(/not across the whole catalogue/);
    await userEvent.click(await screen.findByRole("button", { name: "Show more" }));

    await screen.findByText("Atlas");
    const shown = screen.getAllByRole("heading", { level: 3 }).map((h) => h.textContent);
    expect(shown).toEqual(["Atlas", "Tickets", "Weather"]);
  });
});

describe("scoping to one catalogue", () => {
  it("asks for the one catalogue the operator picked", async () => {
    const load = vi.fn<(q: CatalogQuery) => Promise<Catalog>>()
      .mockResolvedValue(answer([WEATHER], { sources: THREE_CATALOGUES }));
    await render({ load });
    await screen.findByText("Weather");

    fireEvent.change(screen.getByLabelText("Which catalogue"),
      { target: { value: "docker/mcp-registry" } });
    await waitFor(() =>
      expect(load).toHaveBeenCalledWith(expect.objectContaining({ source: "docker/mcp-registry" })));
  });

  it("keeps offering every catalogue once a scoped page reports only one", async () => {
    const load = vi.fn<(q: CatalogQuery) => Promise<Catalog>>()
      .mockResolvedValueOnce(answer([WEATHER], { sources: THREE_CATALOGUES }))
      .mockResolvedValue(answer([TICKETS], {
        sources: [{ source: "docker/mcp-registry", ok: true, stale: false, entries: 1 }],
      }));
    await render({ load });
    await screen.findByText("Weather");

    fireEvent.change(screen.getByLabelText("Which catalogue"),
      { target: { value: "docker/mcp-registry" } });
    await screen.findByText("Tickets");
    // The way back has to stay on the page, and so do the others.
    expect(screen.getByRole("option", { name: "All catalogues" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "registry.smithery.ai" })).toBeInTheDocument();
  });

  it("withdraws most used when the catalogue in view does not count calls", async () => {
    const load = vi.fn<(q: CatalogQuery) => Promise<Catalog>>()
      .mockResolvedValue(answer([counted("brave", 87_579)], { sources: THREE_CATALOGUES }));
    await render({ load });
    await screen.findByText("87,579 calls");

    fireEvent.change(screen.getByLabelText("Order"), { target: { value: "most-used" } });
    await waitFor(() =>
      expect(load).toHaveBeenCalledWith(expect.objectContaining({ sort: "most-used" })));

    fireEvent.change(screen.getByLabelText("Which catalogue"),
      { target: { value: "docker/mcp-registry" } });
    // The order goes with it rather than becoming a request the server would
    // refuse, and the control shows that it has.
    await waitFor(() =>
      expect(load).toHaveBeenCalledWith(expect.objectContaining({
        source: "docker/mcp-registry", sort: "",
      })));
    expect(screen.queryByRole("option", { name: "Most used" })).not.toBeInTheDocument();
  });
});
