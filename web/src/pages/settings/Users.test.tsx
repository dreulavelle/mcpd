import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import { api, type Group, type User } from "@/lib/api";
import { builtinPermissions } from "@/lib/permissions";
import { renderWith } from "@/test/render";
import { Users } from "./Users";

function user(overrides: Partial<User> = {}): User {
  return {
    id: "usr_1", email: "alice@example.com", name: "alice@example.com", display_name: "",
    role: "role_operator", role_name: "Operator",
    grants: [{ plugin: "*", level: "write" }], reaches: [{ plugin: "*", level: "write" }],
    permissions: builtinPermissions("role_operator"),
    groups: [], disabled: false,
    status: "active", has_password: true, created_at: "2026-08-01T09:00:00Z", self: false,
    ...overrides,
  };
}

function group(overrides: Partial<Group> = {}): Group {
  return {
    id: "grp_1", name: "Readers", description: "", role: "", role_name: "",
    grants: [{ plugin: "*", level: "write" }],
    members: 1, created_by: "", created_at: "", ...overrides,
  };
}

function stub(users: User[], groups: Group[] = []) {
  vi.spyOn(api, "users").mockResolvedValue({ users, count: users.length });
  vi.spyOn(api, "groups").mockResolvedValue({ groups, count: groups.length });
}

describe("the users page", () => {
  beforeEach(() => vi.restoreAllMocks());

  // "Why can they decide approvals" is the question this page exists to
  // answer, and the role column alone says "Reader" of somebody a group has
  // added Operator to. The union is computed server-side and carried on
  // `permissions`; the page only has to render the sentence it makes.
  it("says what an account may actually do, after its groups have added to it", async () => {
    stub(
      [user({
        role: "role_reader", role_name: "Reader",
        // Reader alone would read everything and decide nothing; the group
        // below adds Operator, so the account can also decide approvals.
        permissions: builtinPermissions("role_operator"),
        groups: [{ id: "grp_1", name: "Readers", role: "role_operator", role_name: "Operator", grants: [] }],
      })],
      [group({ role: "role_operator", role_name: "Operator" })],
    );
    renderWith(<Users />);
    const row = (await screen.findByText("alice@example.com")).closest("tr")!;
    expect(within(row).getByText("Decides approvals; reads the rest")).toBeInTheDocument();
  });

  it("says an unrestricted administrator may do everything", async () => {
    stub([user({
      role: "role_administrator", role_name: "Administrator",
      permissions: builtinPermissions("role_administrator"),
    })]);
    renderWith(<Users />);
    const row = (await screen.findByText("alice@example.com")).closest("tr")!;
    // Shows in both the "Can do" and "Can reach" columns for an unrestricted
    // administrator, which is the point: neither column narrows the other.
    expect(within(row).getAllByText("Everything").length).toBe(2);
  });

  // A group that hands out the same role the account already holds adds
  // nothing new, and the sentence says just what the role itself carries --
  // it is a union with the account's own permissions, not a mention of every
  // membership.
  it("does not mark an account whose groups add nothing new", async () => {
    stub(
      [user({
        role: "role_operator", role_name: "Operator",
        permissions: builtinPermissions("role_operator"),
        groups: [{ id: "grp_1", name: "Readers", role: "role_operator", role_name: "Operator", grants: [] }],
      })],
      [group({ role: "role_operator", role_name: "Operator" })],
    );
    renderWith(<Users />);
    const row = (await screen.findByText("alice@example.com")).closest("tr")!;
    expect(within(row).getByText("Decides approvals; reads the rest")).toBeInTheDocument();
  });

  it("says a pending account holds nothing yet", async () => {
    stub([user({ status: "pending", permissions: [] })]);
    renderWith(<Users />);
    expect(await screen.findByText("Nothing until approved")).toBeInTheDocument();
  });

  it("says so when nobody is here", async () => {
    stub([]);
    renderWith(<Users />);
    expect(await screen.findByText("Nobody here yet")).toBeInTheDocument();
  });
});
