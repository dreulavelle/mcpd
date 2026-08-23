import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import { api, type HealthReport } from "@/lib/api";
import { renderWith } from "@/test/render";
import { Overview } from "./Overview";

function stub(health: HealthReport | null) {
  vi.spyOn(api, "operations").mockResolvedValue({ operations: [], count: 0 });
  vi.spyOn(api, "plugins").mockResolvedValue({ plugins: [], count: 0 });
  vi.spyOn(api, "instances").mockResolvedValue({ instances: [], count: 0 });
  vi.spyOn(api, "tunnel").mockResolvedValue({
    tunnels: [], can_manage: false, plugins: [], workspaces: [], assignments: {},
  });
  vi.spyOn(api, "audit").mockResolvedValue({ records: [], count: 0 });
  vi.spyOn(api, "endpoints").mockResolvedValue({
    aggregate: "http://127.0.0.1:18080/mcp", per_plugin_example: "/mcp/{plugin}",
  });
  if (health === null) {
    vi.spyOn(api, "health").mockRejectedValue(new Error("down"));
  } else {
    vi.spyOn(api, "health").mockResolvedValue(health);
  }
}

/**
 * Health is content here, because it stopped being a pill in the sidebar.
 *
 * "All good" beside the navigation could not say which check, or what the
 * check complained about, and the detail was in a tooltip -- which is not
 * somewhere a person on a phone can reach. The endpoint has always returned a
 * list with a message on each entry; the list is what gets rendered.
 */
describe("the host's health on the overview", () => {
  beforeEach(() => {
    window.history.replaceState(null, "", "/");
  });

  it("names every check and what it last said", async () => {
    stub({
      status: "up",
      checks: [
        { name: "database", status: "up", critical: true },
        { name: "tunnel", status: "up", critical: false, message: "connected" },
      ],
    });
    renderWith(<Overview />);

    expect(await screen.findByText("database")).toBeInTheDocument();
    expect(screen.getByText("tunnel")).toBeInTheDocument();
    expect(screen.getByText("connected")).toBeInTheDocument();
  });

  // The failing check is why anybody is reading this table, so it does not
  // have to be found among the passing ones.
  it("puts a failing check first, with the reason it gave", async () => {
    stub({
      status: "degraded",
      checks: [
        { name: "database", status: "up", critical: true },
        { name: "upstream", status: "degraded", critical: false, message: "slow to answer" },
      ],
    });
    renderWith(<Overview />);

    const rows = await screen.findAllByRole("row");
    // Row 0 is the header.
    expect(rows[1]).toHaveTextContent("upstream");
    expect(rows[1]).toHaveTextContent("Degraded");
    expect(rows[1]).toHaveTextContent("slow to answer");
    expect(screen.getByText(/1 of 2 checks is not passing/)).toBeInTheDocument();
  });

  // A check that is down and not critical is a different fact from a host that
  // is down, and saying which is the whole reason the field exists.
  it("marks a failing check that is not critical", async () => {
    stub({
      status: "degraded",
      checks: [{ name: "upstream", status: "down", critical: false }],
    });
    renderWith(<Overview />);

    expect(await screen.findByText("not critical")).toBeInTheDocument();
  });

  it("does not say a check passed when it never got an answer", async () => {
    stub(null);
    renderWith(<Overview />);

    expect(await screen.findByText(/says nothing either way/)).toBeInTheDocument();
    expect(screen.queryByText("Passing")).not.toBeInTheDocument();
  });
});

/**
 * The host's own address belongs to the host.
 *
 * It used to sit under the plugin list, where it read as something to do with
 * the plugin above it -- but it is the aggregate endpoint, one URL carrying
 * every plugin the caller is scoped to, so it describes the deployment.
 */
describe("connecting directly", () => {
  beforeEach(() => {
    window.history.replaceState(null, "", "/");
  });

  it("shows the aggregate address and the header a client sends", async () => {
    stub({ status: "up", checks: [] });
    renderWith(<Overview />);

    expect(await screen.findByText("Connecting directly")).toBeInTheDocument();
    expect(screen.getByText("http://127.0.0.1:18080/mcp")).toBeInTheDocument();
    expect(screen.getByText(/Authorization: Bearer YOUR_KEY/)).toBeInTheDocument();
  });

  // The card used to render nothing at all when the call failed, which looks
  // exactly like a host with no address.
  it("says the address could not be read rather than disappearing", async () => {
    stub({ status: "up", checks: [] });
    vi.spyOn(api, "endpoints").mockRejectedValue(new Error("down"));
    renderWith(<Overview />);

    expect(await screen.findByText("Connecting directly")).toBeInTheDocument();
    expect(
      screen.getByText(/Couldn't read this host's address/),
    ).toBeInTheDocument();
  });
});
