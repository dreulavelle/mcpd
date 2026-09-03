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
 *
 * A list on the left selects the role shown on the right, so a single role
 * with no other candidate is chosen automatically once it loads.
 */
describe("the roles page", () => {
  beforeEach(() => vi.restoreAllMocks());

  it("marks a built-in role and offers no way to delete it", async () => {
    stub([role()]);
    renderWith(<Roles />);

    expect(await screen.findByRole("heading", { name: "Reader", level: 2 })).toBeInTheDocument();
    expect(screen.getByText("Built in")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete" })).not.toBeInTheDocument();
    // Read-only, not editable: no name to rename and no select it could act on.
    expect(screen.queryByLabelText("Name")).not.toBeInTheDocument();
    for (const radio of screen.getAllByRole("radio")) expect(radio).toBeDisabled();
  });

  it("offers to delete a custom role nobody holds", async () => {
    stub([role({ id: "role_custom", name: "Tunnel desk", builtin: false, assigned: 0 })]);
    renderWith(<Roles />);

    expect(await screen.findByRole("heading", { name: "Tunnel desk", level: 2 })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Delete" })).toBeEnabled();
    expect(screen.getByLabelText("Name")).toBeInTheDocument();
  });

  // Deleting a role out from under whoever holds it would silently strip
  // them of everything it carried, so the button itself refuses rather than
  // leaving that to a failed request.
  it("refuses to delete a role still assigned to somebody", async () => {
    stub([role({ id: "role_custom", name: "Tunnel desk", builtin: false, assigned: 2 })]);
    renderWith(<Roles />);

    await screen.findByRole("heading", { name: "Tunnel desk", level: 2 });
    expect(screen.getByRole("button", { name: "Delete" })).toBeDisabled();
  });

  it("posts the chosen permissions when a role is added", async () => {
    stub([role()]);
    const create = vi.spyOn(api, "createRole").mockResolvedValue(
      role({ id: "role_new", name: "Tunnel desk", builtin: false, assigned: 0 }),
    );
    renderWith(<Roles />);

    await userEvent.click(await screen.findByRole("button", { name: "New role" }));
    await userEvent.type(screen.getByLabelText("Name"), "Tunnel desk");
    // Starting from nothing, rather than the operator default the dialog
    // opens with, isolates the one permission this test sets by hand.
    await userEvent.selectOptions(screen.getByLabelText("Start from"), "Nothing");
    await userEvent.click(
      within(screen.getByRole("radiogroup", { name: "Tunnels & ChatGPT level" }))
        .getByRole("radio", { name: "Write" }),
    );
    // Two now: the one that opened the form, and the form's own submit.
    await userEvent.click(screen.getAllByRole("button", { name: "Add role" }).at(-1)!);

    await waitFor(() => expect(create).toHaveBeenCalledWith({
      name: "Tunnel desk", description: "", permissions: { tunnels: "write" },
    }));
  });

  // An empty list is a real state -- nothing chosen, nothing to show -- and
  // the page says so rather than rendering a blank pane.
  it("says so when there is no role to choose", async () => {
    stub([]);
    renderWith(<Roles />);
    expect(await screen.findByText("No role chosen.")).toBeInTheDocument();
  });
});
