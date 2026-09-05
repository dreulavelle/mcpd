import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, type PluginInstance } from "@/lib/api";
import { renderWith, sessionFor } from "@/test/render";
import { PluginsList } from "./PluginsList";

function instance(overrides: Partial<PluginInstance> = {}): PluginInstance {
  return {
    name: "echo", type: "echo", runtime: "builtin",
    from_file: true, enabled: true, mounted: true, ...overrides,
  };
}

function stub(
  instances: PluginInstance[],
  stale: { name: string; declared_type: string; removed_by: string; removed_at: string }[] = [],
) {
  vi.spyOn(api, "plugins").mockResolvedValue({ count: 0, plugins: [] });
  vi.spyOn(api, "instances").mockResolvedValue({
    count: instances.length, instances, stale_removals: stale,
  });
  vi.spyOn(api, "pluginTypes").mockResolvedValue({
    types: [{
      name: "echo", title: "Echo", description: "A test integration.",
      configurable: false,
    }],
    count: 1,
  });
}

/**
 * Removing a plugin removes it, however it was defined: its settings and its
 * credentials go, and it is no longer on this host. So it is not in the list of
 * what this host serves, dimmed or otherwise.
 *
 * A plugin the configuration file still lists can be added again, and the place
 * for that is the Add dialog, beside every other plugin that can be added --
 * "remove and restore" was two words for one act, and the restore promised
 * settings that no longer exist.
 */
describe("a plugin removed while the file still lists it", () => {
  beforeEach(() => {
    stub([instance({
      mounted: false, enabled: false, removed: true, removed_by: "user:alice",
      removed_at: "2026-08-20T10:00:00Z",
    })]);
  });

  it("leaves the list rather than staying in it as removed", async () => {
    renderWith(<PluginsList />);

    await screen.findByRole("button", { name: "Add plugin" });
    expect(screen.queryByRole("link", { name: "echo" })).not.toBeInTheDocument();
    expect(screen.queryByText("Removed")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Restore" })).not.toBeInTheDocument();
  });

  it("is offered in the add dialog, saying who took it away", async () => {
    renderWith(<PluginsList />);
    await userEvent.click(await screen.findByRole("button", { name: "Add plugin" }));

    const declared = await screen.findByRole("region", {
      name: "Listed in the configuration file",
    });
    expect(within(declared).getByText("echo")).toBeInTheDocument();
    // A name, not the `user:` id the API sends.
    expect(declared.textContent).toMatch(/— alice removed it/);
    expect(declared.textContent).toMatch(/with nothing entered here/);
  });

  it("adds it back from the dialog", async () => {
    const add = vi.spyOn(api, "restoreInstance")
      .mockResolvedValue({ status: "restored" });

    renderWith(<PluginsList />, { path: "/plugins" });
    await userEvent.click(await screen.findByRole("button", { name: "Add plugin" }));
    const declared = await screen.findByRole("region", {
      name: "Listed in the configuration file",
    });
    await userEvent.click(within(declared).getByRole("button", { name: "Add" }));

    await waitFor(() => expect(add).toHaveBeenCalledWith("echo"));
  });

  // A heading over nothing reads as a section that failed to load.
  it("leaves the section out when the file lists nothing removed", async () => {
    stub([instance()]);

    renderWith(<PluginsList />);
    await userEvent.click(await screen.findByRole("button", { name: "Add plugin" }));

    await screen.findByRole("dialog");
    expect(screen.queryByRole("region", {
      name: "Listed in the configuration file",
    })).not.toBeInTheDocument();
  });

  it("offers it to an administrator and to nobody else", async () => {
    renderWith(<PluginsList />, { session: sessionFor("user") });

    await waitFor(() => expect(api.instances).toHaveBeenCalled());
    expect(screen.queryByRole("button", { name: "Add plugin" })).not.toBeInTheDocument();
  });
});

/**
 * The whole row is the click target.
 *
 * The name alone is a few characters wide, and every row on this page leads
 * somewhere. The mechanism is a stretched link -- an overlay owned by the
 * anchor -- rather than an anchor wrapped around the row, which is invalid
 * inside a table.
 */
describe("the row as a click target", () => {
  it("opens the plugin when the row's surface is clicked", async () => {
    stub([instance()]);
    const { container } = renderWith(<PluginsList />, { path: "/plugins" });
    await screen.findByRole("link", { name: "echo" });

    const surface = container.querySelector(
      'a[href="/plugins/echo"] span[aria-hidden="true"]',
    );
    expect(surface).not.toBeNull();
    await userEvent.click(surface as Element);

    expect(window.location.pathname).toBe("/plugins/echo");
  });

  // The name itself sits above the surface so it can still be selected and
  // copied, and clicking it must still do what a link does.
  it("still opens the plugin when the name is clicked", async () => {
    stub([instance()]);
    renderWith(<PluginsList />, { path: "/plugins" });

    await userEvent.click(await screen.findByRole("link", { name: "echo" }));
    expect(window.location.pathname).toBe("/plugins/echo");
  });
});

/**
 * A removal outlives the declaration it overrode. Discarding one silently
 * would let a single start against a truncated configuration file forget every
 * removal an operator made.
 *
 * "Forget" is a different act from adding a declared plugin back, even though
 * one endpoint serves both: there is no declaration left to add from.
 */
describe("removals with nothing left to remove", () => {
  it("reports them, and offers to forget one", async () => {
    stub([], [{
      name: "gone", declared_type: "cnmaestro",
      removed_by: "user:alice", removed_at: "2026-08-20T10:00:00Z",
    }]);
    const forget = vi.spyOn(api, "restoreInstance")
      .mockResolvedValue({ status: "restored" });

    renderWith(<PluginsList />, { path: "/plugins" });

    expect(await screen.findByText("gone")).toBeInTheDocument();
    expect(screen.getByText(/no longer lists them/)).toBeInTheDocument();
    expect(screen.getByText(/removed by alice on/)).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Forget" }));
    await waitFor(() => expect(forget).toHaveBeenCalledWith("gone"));
  });
});
