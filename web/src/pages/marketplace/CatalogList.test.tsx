import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWith } from "@/test/render";
import { CatalogList } from "./CatalogList";
import { loadCatalog, type Catalog, type CatalogEntry } from "./catalog";

const WEATHER: CatalogEntry = {
  id: "com.example/weather",
  suggestedName: "weather",
  title: "Weather",
  description: "Forecasts and observations.",
  publisher: "example.com",
  categories: ["data"],
  document: { name: "com.example/weather", version: "1.0.0" },
};

const TICKETS: CatalogEntry = {
  id: "com.example/tickets",
  suggestedName: "tickets",
  title: "Tickets",
  description: "Raises and reads support tickets.",
  publisher: "example.com",
  document: { name: "com.example/tickets", version: "2.0.0" },
};

function catalog(entries: CatalogEntry[]): () => Promise<Catalog> {
  return async () => ({ available: true, entries });
}

/**
 * The catalog API is not merged yet, so the seam is what is under test.
 *
 * Everything this page assumes about it lives in `catalog.ts`: a typed entry,
 * a typed answer, and one loader. These tests supply a loader, which is what
 * wiring up the real endpoint will amount to.
 */
describe("the catalog", () => {
  it("says the catalog is missing rather than pretending it is empty", async () => {
    // The shipped loader, deliberately: "this build cannot ask" and "the
    // catalog has nothing" are different sentences and must stay different.
    renderWith(
      <CatalogList installed={new Set()} onAdd={() => {}} load={loadCatalog} />,
    );

    expect(await screen.findByText("No catalog yet")).toBeInTheDocument();
    expect(screen.queryByLabelText("Search the catalog")).not.toBeInTheDocument();
  });

  it("distinguishes an empty catalog from an absent one", async () => {
    renderWith(
      <CatalogList installed={new Set()} onAdd={() => {}} load={catalog([])} />,
    );

    expect(await screen.findByText("The catalog is empty")).toBeInTheDocument();
  });

  it("lists what the catalog offers", async () => {
    renderWith(
      <CatalogList
        installed={new Set()} onAdd={() => {}}
        load={catalog([WEATHER, TICKETS])}
      />,
    );

    expect(await screen.findByText("Weather")).toBeInTheDocument();
    expect(screen.getByText("Tickets")).toBeInTheDocument();
  });

  it("searches across the name, the publisher and what it does", async () => {
    renderWith(
      <CatalogList
        installed={new Set()} onAdd={() => {}}
        load={catalog([WEATHER, TICKETS])}
      />,
    );

    await userEvent.type(await screen.findByLabelText("Search the catalog"), "support");

    expect(screen.getByText("Tickets")).toBeInTheDocument();
    expect(screen.queryByText("Weather")).not.toBeInTheDocument();
  });

  /**
   * Discovery, not an inventory.
   *
   * The whole point of the split is that the marketplace stops listing what is
   * already installed. An entry that is here already says so and points at the
   * page where it is managed, rather than offering an add the import would
   * refuse for a name already taken.
   */
  it("does not offer to add something already added", async () => {
    const onAdd = vi.fn();
    renderWith(
      <CatalogList
        installed={new Set(["weather"])} onAdd={onAdd}
        load={catalog([WEATHER, TICKETS])}
      />,
    );

    await screen.findByText("Weather");
    expect(screen.getByRole("link", { name: /Already added/ }))
      .toHaveAttribute("href", "/plugins/weather");
    // Tickets is not installed, so exactly one Add remains.
    expect(screen.getAllByRole("button", { name: "Add" })).toHaveLength(1);
  });

  /**
   * Adding is the caller's job, and the caller runs the same import a pasted
   * document runs. Handing back the whole entry -- document included -- is
   * what makes that possible without a second add path.
   */
  it("hands the whole entry back rather than adding it itself", async () => {
    const onAdd = vi.fn();
    renderWith(
      <CatalogList installed={new Set()} onAdd={onAdd} load={catalog([WEATHER])} />,
    );

    await userEvent.click(await screen.findByRole("button", { name: "Add" }));
    expect(onAdd).toHaveBeenCalledWith(WEATHER);
  });
});
