import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
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
 * A removal that hides the thing it removed is worse than no removal at all.
 *
 * Somebody who removes the wrong plugin has to be able to find it again, so a
 * plugin removed here stays in the list saying what happened, with the way
 * back on the same row.
 */
describe("a plugin removed while the file still declares it", () => {
  beforeEach(() => {
    stub([instance({
      mounted: false, enabled: false, removed: true, removed_by: "user:alice",
      removed_at: "2026-08-20T10:00:00Z",
    })]);
  });

  it("stays in the list rather than vanishing", async () => {
    renderWith(<PluginsList />);

    expect(await screen.findByRole("link", { name: "echo" })).toBeInTheDocument();
    expect(screen.getByText("Removed")).toBeInTheDocument();
    expect(
      screen.getByText("Removed here. The configuration file still declares it."),
    ).toBeInTheDocument();
  });

  it("offers the restore to an administrator and to nobody else", async () => {
    const { unmount } = renderWith(<PluginsList />);
    expect(await screen.findByRole("button", { name: "Restore" })).toBeInTheDocument();
    unmount();

    renderWith(<PluginsList />, { session: sessionFor("user") });
    await screen.findByRole("link", { name: "echo" });
    expect(screen.queryByRole("button", { name: "Restore" })).not.toBeInTheDocument();
  });

  it("restores from the row", async () => {
    const restore = vi.spyOn(api, "restoreInstance")
      .mockResolvedValue({ status: "restored" });

    renderWith(<PluginsList />, { path: "/plugins" });
    await userEvent.click(await screen.findByRole("button", { name: "Restore" }));

    await waitFor(() => expect(restore).toHaveBeenCalledWith("echo"));
  });
});

/**
 * The whole row is the click target.
 *
 * The name alone is a few characters wide, and every row on this page leads
 * somewhere. The mechanism is a stretched link -- an overlay owned by the
 * anchor -- rather than an anchor wrapped around the row, which is invalid
 * inside a table and would swallow the row's own buttons.
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

  it("does not navigate when a control in the row is used", async () => {
    stub([instance({
      mounted: false, enabled: false, removed: true, removed_by: "user:alice",
      removed_at: "2026-08-20T10:00:00Z",
    })]);
    const restore = vi.spyOn(api, "restoreInstance")
      .mockResolvedValue({ status: "restored" });

    renderWith(<PluginsList />, { path: "/plugins" });
    await userEvent.click(await screen.findByRole("button", { name: "Restore" }));

    await waitFor(() => expect(restore).toHaveBeenCalled());
    expect(window.location.pathname).toBe("/plugins");
  });
});

/**
 * A removal outlives the declaration it overrode. Discarding one silently
 * would let a single start against a truncated configuration file forget every
 * removal an operator made.
 */
describe("removals with nothing left to remove", () => {
  it("reports them, and offers to forget one", async () => {
    stub([], [{
      name: "gone", declared_type: "cnmaestro",
      removed_by: "user:alice", removed_at: "2026-08-20T10:00:00Z",
    }]);
    const restore = vi.spyOn(api, "restoreInstance")
      .mockResolvedValue({ status: "restored" });

    renderWith(<PluginsList />, { path: "/plugins" });

    expect(await screen.findByText("gone")).toBeInTheDocument();
    expect(screen.getByText(/no longer lists them/)).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Forget" }));
    await waitFor(() => expect(restore).toHaveBeenCalledWith("gone"));
  });
});
