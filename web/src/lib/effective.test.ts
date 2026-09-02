import { describe, expect, it } from "vitest";
import type { Group } from "./api";
import { effectiveCapabilities, heldLabel } from "./effective";

const group = (g: Partial<Group>): Group => ({
  id: "g", name: "G", description: "", plugins: [], capabilities: null,
  members: 0, created_by: "", created_at: "", ...g,
});

describe("what an account may actually do", () => {
  it("is the role's own set when no group narrows it", () => {
    const r = effectiveCapabilities("admin", [], []);
    expect(r.held).toEqual(["read", "propose", "approve", "admin"]);
    expect(r.ceiling).toBeNull();
  });

  // Groups declaring no ceiling are ignored rather than treated as
  // permitting everything; otherwise membership of a general group would undo
  // every restriction.
  it("is narrowed by the groups that declare a ceiling, and only those", () => {
    const everyone = group({ id: "e", name: "Everyone" });
    const readers = group({ id: "r", name: "Readers", capabilities: ["read"] });
    const r = effectiveCapabilities("admin", [{ id: "e", name: "Everyone", plugins: [] }, { id: "r", name: "Readers", plugins: [] }], [everyone, readers]);
    expect(r.held).toEqual(["read"]);
    expect(r.restrictedBy).toEqual(["Readers"]);
  });

  it("unions the ceilings, so a second group cannot take away what the first allowed", () => {
    const readers = group({ id: "r", name: "Readers", capabilities: ["read"] });
    const approvers = group({ id: "a", name: "Approvers", capabilities: ["read", "approve"] });
    const r = effectiveCapabilities("user", [{ id: "r", name: "", plugins: [] }, { id: "a", name: "", plugins: [] }], [readers, approvers]);
    expect(r.held).toEqual(["read", "approve"]);
  });

  it("never widens: a ceiling naming admin gives a user nothing", () => {
    const g = group({ id: "g", capabilities: ["read", "admin"] });
    expect(effectiveCapabilities("user", [{ id: "g", name: "", plugins: [] }], [g]).held).toEqual(["read"]);
  });

  it("says nothing when a group permits nothing", () => {
    const g = group({ id: "g", capabilities: [] });
    const r = effectiveCapabilities("admin", [{ id: "g", name: "", plugins: [] }], [g]);
    expect(r.held).toEqual([]);
    expect(heldLabel("admin", r.held)).toBe("Nothing");
  });

  it("describes the ordinary case in one word and the narrowed one by name", () => {
    expect(heldLabel("admin", ["read", "propose", "approve", "admin"])).toBe("Everything");
    expect(heldLabel("user", ["read", "propose"])).toBe("Read, propose");
  });
});
