import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, type Group, type GroupMember } from "@/lib/api";
import { renderWith } from "@/test/render";
import { Groups, reachLabel } from "./Groups";

function group(overrides: Partial<Group> = {}): Group {
  return {
    id: "grp_1",
    name: "Field engineers",
    description: "",
    plugins: [],
    // null is the ordinary case: this group imposes no ceiling and each
    // member's role stands.
    capabilities: null,
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
    stub({ groups: [group({ plugins: ["cnmaestro", "netbox"], members: 2 })] });
    renderWith(<Groups />);

    await screen.findByText("Field engineers");
    expect(screen.getByText("cnmaestro, netbox")).toBeInTheDocument();
    expect(screen.getByText("2 members")).toBeInTheDocument();
  });

  // Deleting narrows: the members lose what the group listed and keep
  // everything else. The confirmation has to say that, because "delete" on a
  // thing that grants access reads like it might do more.
  it("says who loses what before deleting a group", async () => {
    stub({ groups: [group({ members: 3, plugins: ["echo"] })] });
    const remove = vi.spyOn(api, "deleteGroup").mockResolvedValue(undefined);
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
    renderWith(<Groups />);

    await screen.findByText("Field engineers");
    await userEvent.click(screen.getByRole("button", { name: "Delete" }));

    expect(confirm).toHaveBeenCalledWith(
      expect.stringContaining("3 members"),
    );
    expect(confirm.mock.calls[0]![0]).toMatch(/nothing else about them changes/i);
    await waitFor(() => expect(remove).toHaveBeenCalledWith("grp_1"));
  });

  // The empty state is a first step rather than a statement that nothing is
  // wrong: an operator who has never made a group needs to know what one does.
  it("explains what a group is for when there are none", async () => {
    stub();
    renderWith(<Groups />);

    expect(await screen.findByText(/list the systems it should reach/i))
      .toBeInTheDocument();
  });

  it("lists who is in a group, and tells a key from a person", async () => {
    stub({
      groups: [group({ members: 2, plugins: ["echo"] })],
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
    expect(reachLabel([])).toBe("Nothing");
    expect(reachLabel(["*"])).toBe("Everything");
    expect(reachLabel(["echo", "netbox"])).toBe("echo, netbox");
    // A wildcard absorbs on the server, so the page never has to decide what a
    // mixed list means -- but if one arrived, it means everything.
    expect(reachLabel(["echo", "*"])).toBe("Everything");
  });
});
