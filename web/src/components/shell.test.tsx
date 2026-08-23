import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import { api } from "@/lib/api";
import { renderWith, sessionFor } from "@/test/render";
import { Shell } from "./shell";

function mount(role: "user" | "admin", path = "/") {
  vi.spyOn(api, "health").mockResolvedValue({ status: "up", checks: [] });
  return renderWith(<Shell badges={{}} onSignOut={() => {}}>page</Shell>, {
    session: sessionFor(role),
    path,
  });
}

/**
 * The sidebar is gated on capabilities, not on the role.
 *
 * Hiding a link is not access control -- the server refuses every call again
 * -- but a console that offers a page which can only answer 403 is a console
 * that lies about what the account can do.
 */
describe("the sidebar", () => {
  it("hides the marketplace from a user", () => {
    mount("user");
    expect(screen.queryByRole("link", { name: /Marketplace/ })).not.toBeInTheDocument();
  });

  it("shows the marketplace to an administrator", () => {
    mount("admin");
    expect(screen.getByRole("link", { name: /Marketplace/ })).toBeInTheDocument();
  });

  it("shows every read-level section to a user", () => {
    mount("user");
    for (const label of ["Overview", "Approvals", "Audit", "Plugins", "Tunnels", "Settings"]) {
      expect(screen.getByRole("link", { name: new RegExp(label) })).toBeInTheDocument();
    }
  });

  it("hides Users inside Settings from a user, and keeps the rest", () => {
    mount("user", "/settings");
    expect(screen.getByRole("link", { name: "General" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Account" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Users" })).not.toBeInTheDocument();
  });

  it("shows Users inside Settings to an administrator", () => {
    mount("admin", "/settings");
    expect(screen.getByRole("link", { name: "Users" })).toBeInTheDocument();
  });

  it("only expands the section that is open", () => {
    mount("admin", "/approvals");
    expect(screen.queryByRole("link", { name: "General" })).not.toBeInTheDocument();
  });

  it("marks the open section for assistive technology", () => {
    mount("admin", "/plugins");
    expect(screen.getByRole("link", { name: /Plugins/ }))
      .toHaveAttribute("aria-current", "page");
  });
});

/**
 * The health of the host, who is signed in, and the way out.
 *
 * They used to sit in the top-right corner and now sit under the navigation.
 * Two things about that are worth defending: the footer is outside the part of
 * the sidebar that scrolls, so a long list of sections cannot push the state of
 * the host off the bottom; and the narrow layout -- which collapses the sidebar
 * behind a button -- keeps its own copy of the health and the sign-out rather
 * than putting both a drawer away.
 */
describe("the sidebar footer", () => {
  it("names the signed-in person", () => {
    mount("admin");
    expect(screen.getByText("An Admin")).toBeInTheDocument();
  });

  it("falls back to the email when no display name is set", () => {
    vi.spyOn(api, "health").mockResolvedValue({ status: "up", checks: [] });
    renderWith(<Shell badges={{}} onSignOut={() => {}}>page</Shell>, {
      session: sessionFor("user", { display_name: "" }),
    });
    expect(screen.getByText("user@example.com")).toBeInTheDocument();
  });

  it("offers a way out whether or not the sidebar is showing", () => {
    mount("user");
    expect(screen.getAllByRole("button", { name: "Sign out" })).toHaveLength(2);
  });

  it("says what /api/health said, in both layouts", async () => {
    mount("user");
    expect(await screen.findAllByText("All good")).toHaveLength(2);
  });

  it("does not call a degraded host well", async () => {
    vi.spyOn(api, "health").mockResolvedValue({
      status: "degraded",
      checks: [{ name: "sqlite", status: "degraded", critical: true, message: "slow" }],
    });
    renderWith(<Shell badges={{}} onSignOut={() => {}}>page</Shell>, {
      session: sessionFor("user"),
    });
    expect(await screen.findAllByText("Degraded")).toHaveLength(2);
    expect(screen.queryByText("All good")).not.toBeInTheDocument();
  });

  it("lets the nav scroll rather than the footer move", () => {
    mount("admin");
    // A layout behaviour, so the assertion is on the two declarations that
    // produce it: a flex child only scrolls if it is allowed to shrink.
    const nav = screen.getByRole("navigation");
    expect(nav.className).toContain("min-h-0");
    expect(nav.className).toContain("overflow-y-auto");
  });
});
