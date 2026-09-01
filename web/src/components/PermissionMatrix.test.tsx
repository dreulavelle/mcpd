import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWith } from "@/test/render";
import { PermissionMatrix } from "./PermissionMatrix";
import type { Capability } from "@/lib/api";

function setup(value: Capability[] | null) {
  const onChange = vi.fn();
  renderWith(<PermissionMatrix id="t" value={value} onChange={onChange} />);
  return onChange;
}

describe("PermissionMatrix", () => {
  // "No ceiling" and "permits nothing" are different states, and the whole
  // control exists to keep them apart. Representing no-ceiling as every box
  // ticked would turn it into permits-everything the moment somebody unticked
  // one, which silently restricts a group nobody meant to restrict.
  it("treats no ceiling as its own state rather than everything ticked", () => {
    setup(null);
    expect(
      screen.getByText(/Members do whatever their own role allows/i),
    ).toBeInTheDocument();
    // No per-capability switches until the group is actually restricted.
    expect(screen.queryByLabelText("Approve changes")).not.toBeInTheDocument();
  });

  it("turning restriction on starts from permitting nothing", async () => {
    const onChange = setup(null);
    await userEvent.click(
      screen.getByLabelText(/Restrict what this group may do/i),
    );
    // Not null, and not everything: an empty list, which the caller can then
    // add to. Starting from everything would mean switching this on grants
    // nothing until something is removed, which reads backwards.
    expect(onChange).toHaveBeenCalledWith([]);
  });

  it("turning restriction off removes the ceiling rather than emptying it", async () => {
    const onChange = setup(["read"]);
    await userEvent.click(
      screen.getByLabelText(/Restrict what this group may do/i),
    );
    expect(onChange).toHaveBeenCalledWith(null);
  });

  it("ticking a capability adds it and unticking removes it", async () => {
    const onChange = setup(["read"]);
    await userEvent.click(screen.getByLabelText("Approve changes"));
    expect(onChange).toHaveBeenCalledWith(["read", "approve"]);

    onChange.mockClear();
    await userEvent.click(screen.getByLabelText("Read"));
    expect(onChange).toHaveBeenCalledWith([]);
  });

  // An empty ceiling is legitimate and easy to reach by accident, so it says
  // out loud what it means rather than looking like an unfinished form.
  it("says plainly that an empty ceiling suspends the group", () => {
    setup([]);
    expect(
      screen.getByText(/may do nothing at all/i),
    ).toBeInTheDocument();
  });

  // The control must not imply it grants anything. An ordinary user in a group
  // permitting "admin" is still not an administrator, and somebody ticking that
  // box needs to know it before they wonder why it did not work.
  it("says it can only take rights away", () => {
    setup(["read"]);
    expect(
      screen.getByText(/can only take rights away/i),
    ).toBeInTheDocument();
  });
});
