import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, type Grant, type Group, type GroupMember } from "@/lib/api";
import { renderWith } from "@/test/render";
import { Groups, reachLabel } from "./Groups";

function group(overrides: Partial<Group> = {}): Group {
  return {
    id: "grp_1",
    name: "Field engineers",
    description: "",
    // "" is the ordinary case: this group hands out no role, only reach.
    role: "",
    role_name: "",
    grants: [],
    members: 0,
    created_by: "user:admin@example.com",
    created_at: "2026-08-01T09:00:00Z",
    ...overrides,
  };
}

function stub({ groups = [] as Group[], members = [] as GroupMember[] } = {}) {
  vi.spyOn(api, "groups").mockResolvedValue({ groups, count: groups.length });
  vi.spyOn(api, "group").mockResolvedValue({
    group: groups[0] ?? group(), members,
  });
}

describe("the groups page", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });

  // A new group grants nothing, and the page has to say so rather than leaving
  // the column blank -- blank reads as "not loaded", and this is a decision.
  it("says a group with no systems reaches nothing", async () => {
    stub({ groups: [group()] });
    renderWith(<Groups />);

    await screen.findByText("Field engineers");
    expect(screen.getByText("Nothing")).toBeInTheDocument();
  });

  it("shows what a group hands out", async () => {
    stub({
      groups: [group({
        grants: [{ plugin: "cnmaestro", level: "write" }, { plugin: "netbox", level: "write" }],
        members: 2,
      })],
    });
    renderWith(<Groups />);

    await screen.findByText("Field engineers");
    expect(screen.getByText("cnmaestro, netbox")).toBeInTheDocument();
    expect(screen.getByText("2 members")).toBeInTheDocument();
  });

  // A group with no role still shows one, so "no role" is not confused with
  // "not loaded yet".
  it("shows a chip rather than a blank when a group adds no role", async () => {
    stub({ groups: [group()] });
    renderWith(<Groups />);

    await screen.findByText("Field engineers");
    expect(screen.getByText("none")).toBeInTheDocument();
  });

  it("shows the role a group adds", async () => {
    stub({ groups: [group({ role: "role_operator", role_name: "Operator" })] });
    renderWith(<Groups />);

    await screen.findByText("Field engineers");
    expect(screen.getByText("Operator")).toBeInTheDocument();
  });

  // Deleting narrows: the members lose what the group listed and keep
  // everything else. The confirmation has to say that, because "delete" on a
  // thing that grants access reads like it might do more.
  it("says who loses what before deleting a group", async () => {
    stub({ groups: [group({ members: 3, grants: [{ plugin: "echo", level: "write" }] })] });
    const remove = vi.spyOn(api, "deleteGroup").mockResolvedValue(undefined);
    renderWith(<Groups />);

    await screen.findByText("Field engineers");
    await userEvent.click(screen.getByRole("button", { name: "Delete" }));

    const dialog = await screen.findByRole("alertdialog");
    expect(dialog.textContent).toContain("3 members");
    expect(dialog.textContent).toMatch(/nothing else about them changes/i);
    await userEvent.click(within(dialog).getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(remove).toHaveBeenCalledWith("grp_1"));
  });

  // The empty state is a first step rather than a statement that nothing is
  // wrong: an operator who has never made a group needs to know what one does.
  it("explains what a group is for when there are none", async () => {
    stub();
    renderWith(<Groups />);

    expect(await screen.findByText(/the systems it should reach/i))
      .toBeInTheDocument();
  });

  it("lists who is in a group, and tells a key from a person", async () => {
    stub({
      groups: [group({ members: 2, grants: [{ plugin: "echo", level: "write" }] })],
      members: [
        {
          kind: "user", id: "usr_1", label: "alice@example.com",
          added_by: "user:admin@example.com", added_at: "2026-08-02T09:00:00Z",
        },
        {
          kind: "key", id: "key_1", label: "Nightly report",
          added_by: "user:admin@example.com", added_at: "2026-08-02T09:00:00Z",
        },
      ],
    });
    renderWith(<Groups />);

    await screen.findByText("Field engineers");
    await userEvent.click(screen.getByRole("button", { name: "Edit" }));

    expect(await screen.findByText("alice@example.com")).toBeInTheDocument();
    expect(screen.getByText("Nightly report")).toBeInTheDocument();
    expect(screen.getByText("person")).toBeInTheDocument();
    expect(screen.getByText("key")).toBeInTheDocument();
  });
});

describe("rendering a grant", () => {
  it("reads the way every other list of systems here does", () => {
    const grants = (...gs: Grant[]) => gs;
    expect(reachLabel(grants())).toBe("Nothing");
    expect(reachLabel(grants({ plugin: "*", level: "write" }))).toBe("Everything");
    expect(reachLabel(grants(
      { plugin: "echo", level: "write" }, { plugin: "netbox", level: "write" },
    ))).toBe("echo, netbox");
    // A wildcard absorbs on the server, so the page never has to decide what a
    // mixed list means -- but if one arrived at write, it means everything.
    expect(reachLabel(grants(
      { plugin: "echo", level: "write" }, { plugin: "*", level: "write" },
    ))).toBe("Everything");
  });
});
