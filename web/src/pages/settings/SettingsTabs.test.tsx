import { describe, expect, it } from "vitest";
import { screen, within } from "@testing-library/react";
import { renderWith, sessionFor } from "@/test/render";
import { SettingsLayout } from "./SettingsTabs";

/**
 * The rail is the one navigation for every settings page. An operator who
 * cannot open a tab must not be shown it -- the rule the sidebar applied to
 * these when they were entries in it -- and a group that ends up with no
 * open tab has to disappear rather than sit there as an empty heading.
 */
describe("the settings rail", () => {
  it("lists only the tabs the session may open, grouped", async () => {
    renderWith(
      <SettingsLayout><div /></SettingsLayout>,
      {
        session: sessionFor("user", { permissions: ["settings:read", "access:read"] }),
        path: "/settings/users",
      },
    );

    const nav = await screen.findByRole("navigation", { name: "Settings sections" });

    // Groups with an open tab are shown...
    expect(within(nav).getByRole("heading", { name: "This host" })).toBeInTheDocument();
    expect(within(nav).getByRole("heading", { name: "Access" })).toBeInTheDocument();
    // ...and one whose every tab needs more than this session holds is not.
    expect(within(nav).queryByRole("heading", { name: "Connections" })).not.toBeInTheDocument();

    expect(within(nav).getByRole("link", { name: "General" })).toBeInTheDocument();
    expect(within(nav).getByRole("link", { name: "Users & Groups" })).toBeInTheDocument();
    expect(within(nav).getByRole("link", { name: "Roles" })).toBeInTheDocument();
    expect(within(nav).getByRole("link", { name: "API Keys" })).toBeInTheDocument();

    // Each needs more than settings:read or access:read: Diagnostics and
    // Backup want settings:write and system:write, ChatGPT wants
    // tunnels:write, Sign-in wants access:write.
    expect(within(nav).queryByRole("link", { name: "Diagnostics" })).not.toBeInTheDocument();
    expect(within(nav).queryByRole("link", { name: "Backup & Restore" })).not.toBeInTheDocument();
    expect(within(nav).queryByRole("link", { name: "ChatGPT" })).not.toBeInTheDocument();
    expect(within(nav).queryByRole("link", { name: "Sign-in" })).not.toBeInTheDocument();
  });

  it("holds no rail at all for a session that may open none of it", async () => {
    renderWith(
      <SettingsLayout><div /></SettingsLayout>,
      { session: sessionFor("user", { permissions: [] }), path: "/settings" },
    );

    const nav = await screen.findByRole("navigation", { name: "Settings sections" });
    expect(within(nav).queryAllByRole("link")).toHaveLength(0);
  });
});
