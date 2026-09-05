import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, type Group, type ProviderDescriptor, type User } from "@/lib/api";
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

function stub(users: User[], groups: Group[] = [], providers: ProviderDescriptor[] = []) {
  vi.spyOn(api, "users").mockResolvedValue({ users, count: users.length });
  vi.spyOn(api, "groups").mockResolvedValue({ groups, count: groups.length });
  // The same list the sign-in page's buttons come from: what this page may
  // offer as a way for a new person to get in.
  vi.spyOn(api, "authOptions").mockResolvedValue({ providers, registration: false });
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

  // An invited account has no password and nobody has arrived at it. "No
  // password" is the wrong thing to say about it; which provider it is
  // waiting for is the right one.
  it("says an invited account is waiting for a first sign-in, and with what", async () => {
    stub([user({
      has_password: false,
      invite_provider: "google",
      invite_label: "Google",
      invite_expires_at: "2026-09-01T09:00:00Z",
    })]);
    renderWith(<Users />);
    const row = (await screen.findByText("alice@example.com")).closest("tr")!;
    expect(within(row).getByText(/Invited — waiting for a first sign-in with Google\./))
      .toBeInTheDocument();
  });

  it("says nothing about invitations for an account nobody invited", async () => {
    stub([user()]);
    renderWith(<Users />);
    await screen.findByText("alice@example.com");
    expect(screen.queryByText(/Invited/)).not.toBeInTheDocument();
  });
});

/**
 * Adding somebody used to mean inventing a password and handing it over. An
 * invitation is the other way, and the dialog has to make it one choice
 * rather than two fields that can disagree.
 */
describe("adding a person", () => {
  beforeEach(() => vi.restoreAllMocks());

  async function openDialog(providers: ProviderDescriptor[]) {
    stub([user()], [], providers);
    renderWith(<Users />);
    await userEvent.click(await screen.findByRole("button", { name: "Add user" }));
  }

  it("offers each configured provider, and never 'any of them'", async () => {
    await openDialog([
      { provider: "google", label: "Google" },
      { provider: "entra", label: "Microsoft" },
    ]);
    const how = screen.getByLabelText("How they sign in");
    expect(within(how).getByRole("option", { name: "With a password you set" })).toBeInTheDocument();
    expect(within(how).getByRole("option", { name: "With Google" })).toBeInTheDocument();
    expect(within(how).getByRole("option", { name: "With Microsoft" })).toBeInTheDocument();
    // An invitation any provider could take up widens with every provider the
    // host ever adds, and the administrator is naming somebody they already
    // know how to reach.
    expect(within(how).queryByRole("option", { name: /any of them/i })).not.toBeInTheDocument();
  });

  // Both fields would let an administrator set a password on an account whose
  // invitation is still claimable by whoever holds the address.
  it("takes the password field away when a provider is chosen", async () => {
    await openDialog([{ provider: "google", label: "Google" }]);
    expect(screen.getByLabelText("Password")).toBeInTheDocument();

    await userEvent.selectOptions(screen.getByLabelText("How they sign in"), "google");
    expect(screen.queryByLabelText("Password")).not.toBeInTheDocument();

    await userEvent.selectOptions(screen.getByLabelText("How they sign in"), "");
    expect(screen.getByLabelText("Password")).toBeInTheDocument();
  });

  it("sends an invitation rather than a password", async () => {
    const created = vi.spyOn(api, "createUser").mockResolvedValue(user());
    await openDialog([{ provider: "google", label: "Google" }]);

    await userEvent.type(screen.getByLabelText("Email"), "newcomer@example.com");
    await userEvent.selectOptions(screen.getByLabelText("How they sign in"), "google");
    await userEvent.click(screen.getByRole("button", { name: "Send the invitation" }));

    expect(created).toHaveBeenCalledTimes(1);
    const body = created.mock.calls[0]![0];
    expect(body.invite_provider).toBe("google");
    expect(body.password).toBeUndefined();
  });

  // Nothing to choose between, so nothing to choose from: a host with no
  // provider set up shows the form it always showed.
  it("does not ask how when this host has no providers", async () => {
    await openDialog([]);
    expect(screen.queryByLabelText("How they sign in")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Password")).toBeInTheDocument();
  });
});
