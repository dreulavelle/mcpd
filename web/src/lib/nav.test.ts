import { describe, expect, it } from "vitest";
import { capabilitiesOf, roleCan, type Capability } from "./capabilities";
import { capabilityFor, covers, NAV, visibleNav } from "./nav";

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

  it("keeps Settings for a user but drops the Users page inside it", () => {
    const settings = visibleNav(holding("read"))
      .flatMap((g) => g.items)
      .find((i) => i.path === "/settings");
    expect(settings).toBeDefined();
    expect(settings?.children?.map((c) => c.label)).toEqual(["General", "Account"]);
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
  // declarative map exists to make impossible to introduce quietly.
  it("gives every entry an explicit capability", () => {
    for (const group of NAV) {
      for (const item of group.items) {
        expect(item.capability).toBeTruthy();
        for (const child of item.children ?? []) {
          expect(child.capability).toBeTruthy();
        }
      }
    }
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
  const cases: [string, string | null][] = [
    ["/", "read"],
    ["/approvals", "read"],
    ["/audit", "read"],
    ["/plugins", "read"],
    ["/tunnels", "read"],
    ["/marketplace", "admin"],
    ["/settings", "read"],
    ["/settings/account", "read"],
    ["/settings/users", "admin"],
  ];

  for (const [path, expected] of cases) {
    it(`says ${path} needs ${expected}`, () => {
      expect(capabilityFor(path)).toBe(expected);
    });
  }

  it("judges a detail page by its section", () => {
    expect(capabilityFor("/approvals/op-7")).toBe("read");
    expect(capabilityFor("/marketplace/weather")).toBe("admin");
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
