import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import { api } from "@/lib/api";
import { renderWith, sessionFor } from "@/test/render";
import { isCurrent, Shell } from "./shell";

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

describe("which nav entry is current", () => {
  it("keeps a section lit on its detail pages", () => {
    expect(isCurrent("/approvals", "/approvals/op-7")).toBe(true);
    expect(isCurrent("/plugins", "/plugins/cnmaestro")).toBe(true);
  });

  // "/plugins" must not light for "/pluginsomething", which a bare
  // startsWith would.
  it("does not match a section that merely shares a prefix", () => {
    expect(isCurrent("/plugins", "/pluginsomething")).toBe(false);
  });

  it("matches the root exactly, since every path begins with a slash", () => {
    expect(isCurrent("/", "/")).toBe(true);
    expect(isCurrent("/", "/audit")).toBe(false);
  });
});
