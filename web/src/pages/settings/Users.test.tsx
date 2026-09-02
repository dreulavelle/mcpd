import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import { api, type Group, type User } from "@/lib/api";
import { renderWith } from "@/test/render";
import { Users } from "./Users";

function user(overrides: Partial<User> = {}): User {
  return {
    id: "usr_1", email: "alice@example.com", name: "alice@example.com", display_name: "",
    role: "admin", plugins: ["*"], reaches: ["*"], groups: [], disabled: false,
    status: "active", has_password: true, created_at: "2026-08-01T09:00:00Z", self: false,
    ...overrides,
  };
}

function group(overrides: Partial<Group> = {}): Group {
  return {
    id: "grp_1", name: "Readers", description: "", plugins: ["*"], capabilities: null,
    members: 1, created_by: "", created_at: "", ...overrides,
  };
}

function stub(users: User[], groups: Group[] = []) {
  vi.spyOn(api, "users").mockResolvedValue({ users, count: users.length });
  vi.spyOn(api, "groups").mockResolvedValue({ groups, count: groups.length });
}

describe("the users page", () => {
  beforeEach(() => vi.restoreAllMocks());

  // "Why can't they approve" is the question this page exists to answer, and
  // the role column alone says "admin" of somebody a group has narrowed to
  // reading.
  it("says what an account may actually do, after its groups have had their say", async () => {
    stub(
      [user({ groups: [{ id: "grp_1", name: "Readers", plugins: ["*"] }] })],
      [group({ capabilities: ["read"] })],
    );
    renderWith(<Users />);
    const row = (await screen.findByText("alice@example.com")).closest("tr")!;
    expect(within(row).getByText("Read")).toBeInTheDocument();
    expect(within(row).getByText("narrowed")).toBeInTheDocument();
  });

  it("says an unrestricted administrator may do everything", async () => {
    stub([user({ reaches: ["echo"] })]);
    renderWith(<Users />);
    const row = (await screen.findByText("alice@example.com")).closest("tr")!;
    expect(within(row).getByText("Everything")).toBeInTheDocument();
    expect(within(row).queryByText("narrowed")).not.toBeInTheDocument();
  });

  it("says a pending account holds nothing yet", async () => {
    stub([user({ status: "pending" })]);
    renderWith(<Users />);
    expect(await screen.findByText("Nothing until approved")).toBeInTheDocument();
  });

  it("says so when nobody is here", async () => {
    stub([]);
    renderWith(<Users />);
    expect(await screen.findByText("Nobody here yet")).toBeInTheDocument();
  });
});
