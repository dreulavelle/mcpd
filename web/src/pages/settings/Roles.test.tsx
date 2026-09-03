import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, type RoleDef } from "@/lib/api";
import { renderWith } from "@/test/render";
import { Roles } from "./Roles";

function role(overrides: Partial<RoleDef> = {}): RoleDef {
  return {
    id: "role_reader",
    name: "Reader",
    description: "Reads everything.",
    builtin: true,
    permissions: {
      approvals: "read", policies: "read", plugins: "read", tunnels: "read",
      settings: "read", history: "read", system: "read",
    },
    assigned: 3,
    created_by: "",
    created_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

function stub(roles: RoleDef[]) {
  vi.spyOn(api, "roles").mockResolvedValue({ roles, count: roles.length, areas: [] });
}

/**
 * Three roles are defined by this build and cannot change, so "what does
 * Operator mean here" has one answer everywhere -- and nothing else about
 * this page may pretend it can edit or remove one of them.
 */
describe("the roles page", () => {
  beforeEach(() => vi.restoreAllMocks());

  it("marks a built-in role and offers no way to delete it", async () => {
    stub([role()]);
    renderWith(<Roles />);

    const row = (await screen.findByText("Reader")).closest("tr")!;
    expect(within(row).getByText("built in")).toBeInTheDocument();
    expect(within(row).queryByRole("button", { name: "Delete" })).toBeNull();
    // Read-only, not editable: the row offers to look rather than to change.
    expect(within(row).getByRole("button", { name: "Show" })).toBeInTheDocument();
  });

  it("offers to delete a custom role nobody holds", async () => {
    stub([role({ id: "role_custom", name: "Tunnel desk", builtin: false, assigned: 0 })]);
    renderWith(<Roles />);

    const row = (await screen.findByText("Tunnel desk")).closest("tr")!;
    expect(within(row).getByRole("button", { name: "Delete" })).toBeEnabled();
    expect(within(row).getByRole("button", { name: "Edit" })).toBeInTheDocument();
  });

  // Deleting a role out from under whoever holds it would silently strip
  // them of everything it carried, so the button itself refuses rather than
  // leaving that to a failed request.
  it("refuses to delete a role still assigned to somebody", async () => {
    stub([role({ id: "role_custom", name: "Tunnel desk", builtin: false, assigned: 2 })]);
    renderWith(<Roles />);

    const row = (await screen.findByText("Tunnel desk")).closest("tr")!;
    expect(within(row).getByRole("button", { name: "Delete" })).toBeDisabled();
  });

  it("posts the chosen permissions when a role is added", async () => {
    stub([role()]);
    const create = vi.spyOn(api, "createRole").mockResolvedValue(
      role({ id: "role_new", name: "Tunnel desk", builtin: false, assigned: 0 }),
    );
    renderWith(<Roles />);

    await userEvent.click(await screen.findByRole("button", { name: "Add role" }));
    await userEvent.type(screen.getByLabelText("Name"), "Tunnel desk");
    await userEvent.selectOptions(screen.getByLabelText("Tunnels & ChatGPT level"), "write");
    // Two now: the one that opened the form, and the form's own submit.
    await userEvent.click(screen.getAllByRole("button", { name: "Add role" }).at(-1)!);

    await waitFor(() => expect(create).toHaveBeenCalledWith({
      name: "Tunnel desk", description: "", permissions: { tunnels: "write" },
    }));
  });

  it("says so when this host has not written its built-in roles yet", async () => {
    stub([]);
    renderWith(<Roles />);
    expect(await screen.findByText(/has not written its built-in roles/i)).toBeInTheDocument();
  });
});
