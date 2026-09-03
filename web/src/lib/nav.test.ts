import { describe, expect, it } from "vitest";
import type { Permission } from "./permissions";
import {
  capabilityFor, covers, entryFor, NAV, redirectFor, visibleNav, type Requirement,
} from "./nav";

/** A `can` predicate for a fixed set of permissions. */
function holding(...held: Permission[]) {
  return (p: Permission) => held.includes(p);
}

/** Every permission this build knows about, for the tests that need "everything". */
const EVERYTHING: Permission[] = [
  "approvals:read", "approvals:decide",
  "policies:read", "policies:write",
  "plugins:read", "plugins:write",
  "tunnels:read", "tunnels:write",
  "settings:read", "settings:write",
  "access:read", "access:write",
  "history:read", "history:write",
  "system:read", "system:write",
];

describe("navigation gating", () => {
  it("hides the marketplace from anyone without plugins:write", () => {
    const labels = visibleNav(holding("approvals:read", "plugins:read", "history:read"))
      .flatMap((g) => g.items.map((i) => i.label));
    expect(labels).not.toContain("Marketplace");
    expect(labels).toContain("Approvals");
    expect(labels).toContain("Plugins");
  });

  it("shows the marketplace to a holder of plugins:write", () => {
    const labels = visibleNav(holding("plugins:read", "plugins:write"))
      .flatMap((g) => g.items.map((i) => i.label));
    expect(labels).toContain("Marketplace");
  });

  // The approval policy is readable by anyone who may read the settings, for
  // the same reason Settings is: what this host will do without asking
  // anybody is part of understanding the deployment. Changing it needs
  // policies:write, which the page enforces by rendering read-only.
  it("keeps Settings and Policies for a reader but drops the rest", () => {
    const labels = visibleNav(holding("settings:read", "policies:read"))
      .flatMap((g) => g.items.map((i) => i.label));
    expect(labels).toContain("Settings");
    expect(labels).toContain("Policies");
    expect(labels).not.toContain("Plugins");
    expect(labels).not.toContain("Marketplace");
    // Not in the sidebar for anybody now -- they are tabs on Settings. The
    // permission each route needs is asserted separately, and must not have
    // moved with the menu entry.
    expect(labels).not.toContain("Certificates");
    expect(labels).not.toContain("API Keys");
    expect(labels).not.toContain("ChatGPT");
    // Who may sign in, and what a group hands everyone in it, is the same
    // kind of decision as who has an account -- and is gated the same way.
    expect(labels).not.toContain("Authentication");
    expect(labels).not.toContain("Groups");
    expect(labels).not.toContain("Roles");
  });

  // They are siblings, not a section that opens. A destination nobody can see
  // until they click something else is one most people never find.
  it("lists every administrative page beside Settings rather than inside it", () => {
    const administer = visibleNav(holding("settings:read", "system:read", "history:read"))
      .find((g) => g.title === "Administer");
    expect(administer?.items.map((i) => i.label)).toEqual([
      "Settings", "System", "Performance", "Logs",
    ]);
  });

  // /settings covers /settings/policy, and both are in the sidebar. An entry
  // that merely covers the path is not the entry somebody is on, and
  // highlighting every one that does would light up most of the section.
  it("marks one entry current, the deepest that matches", () => {
    expect(entryFor("/settings/policy")?.path).toBe("/settings/policy");
    expect(entryFor("/settings")?.path).toBe("/settings");
    // A page beneath an entry still belongs to it.
    expect(entryFor("/plugins/echo")?.path).toBe("/plugins");
    expect(entryFor("/nowhere")).toBeNull();
  });

  /**
   * The bug this exists for. Users and Groups were tabs on Settings *and* two
   * entries beside it, so the same page had two ways in that highlighted
   * differently, and the sidebar spent two permanent lines on a page visited
   * when somebody joins.
   *
   * Leaving the sidebar must not ungate them. The requirement lives in the
   * route map rather than being read off an entry that no longer exists --
   * without that they would fall back to the `/settings` entry covering them,
   * which asks only for `settings:read`, and two access-administration pages
   * would be served to anybody who could open the console.
   */
  it("keeps users and groups out of the sidebar while keeping them access-gated", () => {
    const labels = visibleNav(holding(...EVERYTHING))
      .flatMap((g) => g.items.map((i) => i.label));
    expect(labels).not.toContain("Users");
    expect(labels).not.toContain("Groups");

    expect(capabilityFor("/settings/users")).toBe("access:read");
    expect(capabilityFor("/settings/groups")).toBe("access:read");
  });

  // It is in the map because the router judges every path against the map.
  // It is out of the sidebar because it already has a way in, and the same
  // destination listed twice is a menu that has stopped being a summary.
  it("keeps the profile out of the sidebar while keeping it in the map", () => {
    const labels = visibleNav(holding(...EVERYTHING))
      .flatMap((g) => g.items.map((i) => i.label));
    expect(labels).not.toContain("Profile");
    expect(capabilityFor("/profile")).toBe("signed-in");
  });

  // "signed-in" entries show regardless of what a principal holds: Overview
  // asks its own questions per card and goes quiet on a refusal, rather than
  // the sidebar deciding on its behalf.
  it("shows only the signed-in entries to a principal holding no permission", () => {
    const labels = visibleNav(() => false).flatMap((g) => g.items.map((i) => i.label));
    expect(labels).toEqual(["Overview"]);
  });

  it("drops a group heading once its last entry is gone", () => {
    const groups = visibleNav(holding("settings:read"));
    for (const group of groups) expect(group.items.length).toBeGreaterThan(0);
  });

  // Nothing in the map may be reachable by default. A section added without a
  // requirement would be visible to everyone, which is the failure the
  // declarative map exists to make impossible to introduce quietly.
  it("gives every entry an explicit, non-empty requirement", () => {
    for (const group of NAV) {
      for (const item of group.items) {
        expect(typeof item.capability).toBe("string");
        expect(item.capability.length).toBeGreaterThan(0);
      }
    }
  });
});

/**
 * Two things moved, and somebody has both addresses bookmarked.
 *
 * An installed remote server was managed under /marketplace though it is a
 * plugin; your own account was managed under /settings though settings are the
 * host's. Neither may 404 for it.
 */
describe("paths that moved", () => {
  it("sends an installed remote server to its plugin page", () => {
    expect(redirectFor("/marketplace/weather")).toBe("/plugins/weather");
  });

  // Encoded once and left that way. Decoding to re-encode is a round trip with
  // an escaping bug to lose and nothing to gain.
  it("carries an awkward name across unchanged", () => {
    expect(redirectFor("/marketplace/one%20two")).toBe("/plugins/one%20two");
  });

  it("leaves the marketplace itself alone", () => {
    expect(redirectFor("/marketplace")).toBeNull();
  });

  it("sends the old account page to the profile", () => {
    expect(redirectFor("/settings/account")).toBe("/profile");
  });

  it("has nothing to say about a path that did not move", () => {
    expect(redirectFor("/plugins/weather")).toBeNull();
    expect(redirectFor("/settings")).toBeNull();
    expect(redirectFor("/")).toBeNull();
  });
});

describe("which entry covers a path", () => {
  it("keeps a section lit on its detail pages", () => {
    expect(covers("/approvals", "/approvals/op-7")).toBe(true);
    expect(covers("/plugins", "/plugins/cnmaestro")).toBe(true);
  });

  // "/plugins" must not match "/pluginsomething", which a bare startsWith
  // would. This decides both the highlight and the requirement now, so a
  // loose match would gate a page on some other section's rule.
  it("does not match a section that merely shares a prefix", () => {
    expect(covers("/plugins", "/pluginsomething")).toBe(false);
  });

  it("matches the root exactly, since every path begins with a slash", () => {
    expect(covers("/", "/")).toBe(true);
    expect(covers("/", "/audit")).toBe(false);
  });
});

/**
 * One table of requirements, not two.
 *
 * Routes used to spell out `required=` per case beside this map. They agreed,
 * and nothing made them -- and Overview had already been missed, rendering
 * with no gate at all.
 */
describe("the permission a path requires", () => {
  const cases: [string, Requirement | null][] = [
    ["/", "signed-in"],
    ["/approvals", "approvals:read"],
    ["/audit", "history:read"],
    // A row names which systems were reached and by whom, which is wider than
    // any one account's own work. Same reasoning as the log.
    ["/activity", "history:read"],
    ["/plugins", "plugins:read"],
    ["/tunnels", "tunnels:read"],
    ["/marketplace", "plugins:write"],
    ["/settings", "settings:read"],
    ["/settings/policy", "policies:read"],
    ["/settings/authentication", "access:write"],
    ["/settings/users", "access:read"],
    // A backup carries this host's database and the key that opens its
    // secrets, and a restore replaces both.
    ["/settings/backup", "system:write"],
    // Groups decide what an account or a key reaches, and keys are
    // credentials that act on this host. Both are the same kind of decision
    // as who has an account, and are gated the same way.
    ["/settings/groups", "access:read"],
    ["/settings/keys", "access:read"],
    ["/settings/roles", "access:read"],
    // Adding a certificate decides what every outbound connection this host
    // makes will accept as proof of identity.
    ["/settings/certificates", "plugins:write"],
    // A ChatGPT account carries a credential, an identity and a grant, so
    // adding one hands a whole workspace a way in.
    ["/settings/chatgpt", "tunnels:write"],
    // Your own profile is not an administrative surface, and gating it on
    // read would be reflex rather than a rule.
    ["/profile", "signed-in"],
  ];

  for (const [path, expected] of cases) {
    it(`says ${path} needs ${expected}`, () => {
      expect(capabilityFor(path)).toBe(expected);
    });
  }

  it("judges a detail page by its section", () => {
    expect(capabilityFor("/approvals/op-7")).toBe("approvals:read");
    expect(capabilityFor("/plugins/cnmaestro")).toBe("plugins:read");
  });

  // The child is the more specific rule and has to win, or /settings/users
  // inherits settings:read from the section it sits in and stops asking for
  // access:read.
  it("lets a child override the section it sits in", () => {
    expect(capabilityFor("/settings")).toBe("settings:read");
    expect(capabilityFor("/settings/users")).toBe("access:read");
  });

  /**
   * The bug this exists for. Several pages became tabs on Settings, which
   * took their sidebar entries away -- and the requirement used to be read
   * off that entry. Without a rule of their own they fall back to the
   * `/settings` entry that covers them, which asks only for `settings:read`:
   * pages that carry a credential or a grant served to anyone who could open
   * the console.
   *
   * A page's requirement is a property of the page, not of whether it earned
   * a line in the menu.
   */
  it("keeps a tab's own requirement after it left the sidebar", () => {
    const tabs: [string, Requirement][] = [
      ["/settings/authentication", "access:write"],
      ["/settings/certificates", "plugins:write"],
      ["/settings/keys", "access:read"],
      ["/settings/roles", "access:read"],
      ["/settings/chatgpt", "tunnels:write"],
    ];
    const sidebar = visibleNav(holding(...EVERYTHING)).flatMap((g) => g.items.map((i) => i.path));

    for (const [path, requirement] of tabs) {
      expect(sidebar).not.toContain(path);
      expect(capabilityFor(path)).toBe(requirement);
    }
  });

  // Null is "nothing to render", never "anyone may".
  it("returns null for a path the map does not cover", () => {
    expect(capabilityFor("/nonsense")).toBeNull();
  });

  it("has an answer for every entry in the map", () => {
    for (const group of NAV) {
      for (const item of group.items) {
        expect(capabilityFor(item.path)).not.toBeNull();
      }
    }
  });
});
