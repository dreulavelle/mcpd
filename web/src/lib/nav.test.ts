import { describe, expect, it } from "vitest";
import { capabilitiesOf, roleCan, type Capability } from "./capabilities";
import { NAV, reachablePaths, visibleNav } from "./nav";

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

  it("does not offer a path the principal cannot reach", () => {
    const user = reachablePaths(holding("read", "propose", "approve"));
    expect(user.has("/marketplace")).toBe(false);
    expect(user.has("/settings/users")).toBe(false);
    expect(user.has("/approvals")).toBe(true);
    expect(user.has("/settings/account")).toBe(true);
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
