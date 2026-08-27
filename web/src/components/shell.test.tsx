import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import { api } from "@/lib/api";
import { renderWith, sessionFor } from "@/test/render";
import { Shell } from "./shell";

function mount(role: "user" | "admin", path = "/") {
  return renderWith(<Shell badges={{}} onSignOut={() => {}} version="1.2.3">page</Shell>, {
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

  it("hides the administrative pages from a user, and keeps the rest", () => {
    mount("user", "/settings");
    expect(screen.getByRole("link", { name: "Settings" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Policies" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Users" })).not.toBeInTheDocument();
  });

  // Settings is how the host is configured. Your own account is not, and is
  // reached by clicking your name rather than by a second entry here.
  it("does not offer an Account page", () => {
    mount("admin", "/settings");
    expect(screen.queryByRole("link", { name: "Account" })).not.toBeInTheDocument();
  });

  it("shows Users to an administrator", () => {
    mount("admin", "/settings");
    expect(screen.getByRole("link", { name: "Users" })).toBeInTheDocument();
  });

  // Every administrative page that is in the sidebar is there from anywhere,
  // not only once Settings has been opened. That is the whole point of
  // flattening them.
  //
  // API Keys is deliberately not in the list any more: it, Certificates and
  // Authentication became tabs on Settings, because each is visited rarely and
  // a sidebar read constantly should not spend a line on each.
  it("lists the administrative pages from a page in another section", () => {
    mount("admin", "/approvals");
    for (const label of ["Settings", "Policies", "Users", "Groups"]) {
      expect(screen.getByRole("link", { name: label })).toBeInTheDocument();
    }
    expect(screen.queryByRole("link", { name: "API Keys" })).not.toBeInTheDocument();
  });

  // /settings covers /settings/users, so both entries match the path. Only the
  // one somebody is actually on may be marked current.
  it("marks only the deepest matching entry as current", () => {
    mount("admin", "/settings/users");
    expect(screen.getByRole("link", { name: "Users" }))
      .toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "Settings" }))
      .not.toHaveAttribute("aria-current");
  });

  it("marks the open section for assistive technology", () => {
    mount("admin", "/plugins");
    expect(screen.getByRole("link", { name: /Plugins/ }))
      .toHaveAttribute("aria-current", "page");
  });
});

/**
 * Who is signed in, and the way out.
 *
 * The footer is outside the part of the sidebar that scrolls, so a long list of
 * sections cannot push the identity off the bottom, and the narrow layout --
 * which collapses the sidebar behind a button -- keeps its own sign-out rather
 * than putting it a drawer away.
 */
describe("the sidebar footer", () => {
  it("names the signed-in person", () => {
    mount("admin");
    expect(screen.getByText("An Admin")).toBeInTheDocument();
  });

  it("falls back to the email when no display name is set", () => {
    renderWith(<Shell badges={{}} onSignOut={() => {}} version="1.2.3">page</Shell>, {
      session: sessionFor("user", { display_name: "" }),
    });
    expect(screen.getByText("user@example.com")).toBeInTheDocument();
  });

  // The name is the way to the profile, because that is where people look for
  // it -- there is no nav entry.
  it("makes the name the way to your own profile", () => {
    mount("admin");
    expect(screen.getByRole("link", { name: "An Admin" }))
      .toHaveAttribute("href", "/profile");
  });

  /**
   * Sign-out is beside the link, never inside it.
   *
   * Nesting the button in the anchor makes one target out of two intentions,
   * and the misclick ends the session when the person meant to read their own
   * capabilities.
   */
  it("keeps sign-out out of the profile link", () => {
    mount("admin");
    const profile = screen.getByRole("link", { name: "An Admin" });
    expect(profile.querySelector("button")).toBeNull();
  });

  it("offers a way out whether or not the sidebar is showing", () => {
    mount("user");
    expect(screen.getAllByRole("button", { name: "Sign out" })).toHaveLength(2);
  });

  /**
   * The health pill is gone from the chrome.
   *
   * "All good" beside the navigation was a binary with no context: it could
   * not say which check, or what the check complained about, and the detail
   * was in a tooltip nobody on a phone can open. The checks are content on the
   * Overview now, which is reachable in every layout.
   */
  it("says nothing about the host's health, and does not ask", () => {
    const health = vi.spyOn(api, "health");
    mount("admin");
    expect(screen.queryByText("All good")).not.toBeInTheDocument();
    expect(health).not.toHaveBeenCalled();
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

/**
 * The running version, in the sidebar.
 *
 * It is the first thing asked for in any report of something being wrong, and
 * before this it appeared only on the System page -- which is one click away
 * from wherever the problem actually is.
 */
describe("the running version", () => {
  it("names what this host is running, under the account", () => {
    mount("user");
    expect(screen.getAllByText(/mcpd 1\.2\.3/).length).toBeGreaterThan(0);
  });
});
