import { describe, expect, it } from "vitest";
import { capabilitiesOf, roleCan, type Capability } from "./capabilities";
import {
  capabilityFor, covers, NAV, redirectFor, visibleNav, type Requirement,
} from "./nav";

/** A `can` predicate for a fixed set of capabilities. */
function holding(...held: Capability[]) {
  return (c: Capability) => held.includes(c);
}

describe("the role-to-capability map", () => {
  it("mirrors internal/auth: a user reads, proposes and approves, and no more", () => {
    expect([...capabilitiesOf("user")].sort())
      .toEqual(["approve", "propose", "read"]);
  });

  it("gives an administrator everything a user has, plus admin", () => {
    expect([...capabilitiesOf("admin")].sort())
      .toEqual(["admin", "approve", "propose", "read"]);
  });

  // A role the server grows that this build does not know about must come back
  // powerless. Defaulting an unknown role to a user's rights would hand it
  // approve, which is a decision this table has no business making.
  it("gives an unknown role nothing", () => {
    expect(capabilitiesOf("superuser")).toEqual([]);
    expect(roleCan("superuser", "read")).toBe(false);
  });
});

describe("navigation gating", () => {
  it("hides the marketplace from anyone without admin", () => {
    const labels = visibleNav(holding("read", "propose", "approve"))
      .flatMap((g) => g.items.map((i) => i.label));
    expect(labels).not.toContain("Marketplace");
    expect(labels).toContain("Approvals");
    expect(labels).toContain("Plugins");
  });

  it("shows the marketplace to an administrator", () => {
    const labels = visibleNav(holding("read", "propose", "approve", "admin"))
      .flatMap((g) => g.items.map((i) => i.label));
    expect(labels).toContain("Marketplace");
  });

  // Settings is how the *host* is configured. Your own account moved to
  // /profile, reached by clicking your name, so Account is no longer a child
  // here -- and Settings stopped meaning two different things.
  // The approval policy is readable by anyone who may read, for the same
  // reason General is: what this host will do without asking anybody is part
  // of understanding the deployment, and the people the rules are written
  // about are exactly who should be able to read them. Changing it is admin,
  // which the page enforces by rendering read-only.
  it("keeps Settings and the approval policy for a user but drops Users", () => {
    const settings = visibleNav(holding("read"))
      .flatMap((g) => g.items)
      .find((i) => i.path === "/settings");
    expect(settings).toBeDefined();
    expect(settings?.children?.map((c) => c.label))
      .toEqual(["General", "Approval policy"]);
    expect(settings?.children?.map((c) => c.label)).not.toContain("Users");
    // Who may sign in, and who is waiting to be let in, is the same kind of
    // decision as who has an account -- and is gated the same way.
    expect(settings?.children?.map((c) => c.label)).not.toContain("Authentication");
    expect(settings?.children?.map((c) => c.label)).not.toContain("Groups");
    expect(settings?.children?.map((c) => c.label)).not.toContain("Keys");
  });

  // It is in the map because the router judges every path against the map.
  // It is out of the sidebar because it already has a way in, and the same
  // destination listed twice is a menu that has stopped being a summary.
  it("keeps the profile out of the sidebar while keeping it in the map", () => {
    const labels = visibleNav(holding("read", "propose", "approve", "admin"))
      .flatMap((g) => g.items.map((i) => i.label));
    expect(labels).not.toContain("Profile");
    expect(capabilityFor("/profile")).toBe("signed-in");
  });

  // A parent left standing over nothing is a dead end: it looks like a section
  // and opens onto an empty list.
  it("drops a parent whose children are all hidden", () => {
    const items = visibleNav(() => false).flatMap((g) => g.items);
    expect(items).toEqual([]);
  });

  it("drops a group heading once its last entry is gone", () => {
    const groups = visibleNav(holding("read"));
    for (const group of groups) expect(group.items.length).toBeGreaterThan(0);
  });

  // Nothing in the map may be reachable by default. A section added without a
  // capability would be visible to everyone, which is the failure the
  // declarative map exists to make impossible to introduce quietly. An
  // ungated one has to say "signed-in" out loud, which an omission cannot do.
  it("gives every entry an explicit requirement", () => {
    const known: Requirement[] = ["read", "propose", "approve", "admin", "signed-in"];
    for (const group of NAV) {
      for (const item of group.items) {
        expect(known).toContain(item.capability);
        for (const child of item.children ?? []) {
          expect(known).toContain(child.capability);
        }
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
  // would. This decides both the highlight and the capability now, so a loose
  // match would gate a page on some other section's rule.
  it("does not match a section that merely shares a prefix", () => {
    expect(covers("/plugins", "/pluginsomething")).toBe(false);
  });

  it("matches the root exactly, since every path begins with a slash", () => {
    expect(covers("/", "/")).toBe(true);
    expect(covers("/", "/audit")).toBe(false);
  });
});

/**
 * One table of capabilities, not two.
 *
 * Routes used to spell out `required=` per case beside this map. They agreed,
 * and nothing made them -- and Overview had already been missed, rendering
 * with no gate at all.
 */
describe("the capability a path requires", () => {
  const cases: [string, Requirement | null][] = [
    ["/", "read"],
    ["/approvals", "read"],
    ["/audit", "read"],
    ["/plugins", "read"],
    ["/tunnels", "read"],
    ["/marketplace", "admin"],
    ["/settings", "read"],
    ["/settings/policy", "read"],
    ["/settings/authentication", "admin"],
    ["/settings/users", "admin"],
    // Groups decide what an account or a key reaches, and keys are
    // credentials that act on this host. Both are the same kind of decision
    // as who has an account, and are gated the same way.
    ["/settings/groups", "admin"],
    ["/settings/keys", "admin"],
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
    expect(capabilityFor("/approvals/op-7")).toBe("read");
    expect(capabilityFor("/plugins/cnmaestro")).toBe("read");
  });

  // The child is the more specific rule and has to win, or /settings/users
  // inherits read from the section it sits in and stops being admin-only.
  it("lets a child override the section it sits in", () => {
    expect(capabilityFor("/settings")).toBe("read");
    expect(capabilityFor("/settings/users")).toBe("admin");
  });

  // Null is "nothing to render", never "anyone may".
  it("returns null for a path the map does not cover", () => {
    expect(capabilityFor("/nonsense")).toBeNull();
  });

  it("has an answer for every entry in the map", () => {
    for (const group of NAV) {
      for (const item of group.items) {
        expect(capabilityFor(item.path)).not.toBeNull();
        for (const child of item.children ?? []) {
          expect(capabilityFor(child.path)).not.toBeNull();
        }
      }
    }
  });
});
