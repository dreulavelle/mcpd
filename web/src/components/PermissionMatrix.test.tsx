import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { PermissionSet } from "@/lib/permissions";
import { PermissionMatrix } from "./PermissionMatrix";

/** Renders the matrix and reports what it last handed back. */
function mount(value: PermissionSet = {}, extra: { disabled?: boolean; readOnly?: boolean } = {}) {
  const onChange = vi.fn();
  render(<PermissionMatrix id="perm" value={value} onChange={onChange} {...extra} />);
  return onChange;
}

/**
 * Eight rows, each a level in one area -- the vocabulary the server actually
 * enforces, so a typo here is a refused save rather than a stored permission
 * nobody holds.
 */
describe("editing a permission set", () => {
  it("starts every area at none when the set holds nothing", () => {
    mount();
    expect(screen.getByLabelText("Settings level")).toHaveValue("none");
    expect(screen.getByLabelText("Access level")).toHaveValue("none");
  });

  it("shows what the set already holds", () => {
    mount({ settings: "write", history: "read" });
    expect(screen.getByLabelText("Settings level")).toHaveValue("write");
    expect(screen.getByLabelText("History level")).toHaveValue("read");
    expect(screen.getByLabelText("Access level")).toHaveValue("none");
  });

  it("raises one area's level without touching the others", async () => {
    const onChange = mount({ history: "read" });

    await userEvent.selectOptions(screen.getByLabelText("Settings level"), "write");
    expect(onChange).toHaveBeenCalledWith({ history: "read", settings: "write" });
  });

  // "None" is a real choice -- taking a permission away -- and has to remove
  // the key rather than leave a "none" value sitting in the set the server
  // would have to also know how to ignore.
  it("drops the area from the set when it is put back to none", async () => {
    const onChange = mount({ settings: "write", history: "read" });

    await userEvent.selectOptions(screen.getByLabelText("Settings level"), "none");
    expect(onChange).toHaveBeenCalledWith({ history: "read" });
  });

  // Approvals is decided, not written -- the row offers "read" and "decide",
  // never "write", because the server has no such level for that area.
  it("offers approvals read-or-decide rather than read-or-write", () => {
    mount();
    const select = screen.getByLabelText("Approvals level");
    const options = Array.from(select.querySelectorAll("option")).map((o) => o.textContent);
    expect(options).toEqual(["None", "Read", "Read and decide"]);
  });

  it("offers every other area read-or-write", () => {
    mount();
    const select = screen.getByLabelText("Settings level");
    const options = Array.from(select.querySelectorAll("option")).map((o) => o.textContent);
    expect(options).toEqual(["None", "Read", "Read and write"]);
  });

  it("sets approvals to decide, not write", async () => {
    const onChange = mount();

    await userEvent.selectOptions(screen.getByLabelText("Approvals level"), "decide");
    expect(onChange).toHaveBeenCalledWith({ approvals: "decide" });
  });

  // Access at more than read can hand out any role, including one that
  // reaches this very matrix -- a warning belongs beside the row that can
  // grant it, not in a document nobody reads before they check the box.
  it("warns beside access once it is held at more than read", () => {
    const { rerender } = render(
      <PermissionMatrix id="perm" value={{ access: "read" }} onChange={() => undefined} />,
    );
    expect(screen.queryByText(/hand out any role/i)).not.toBeInTheDocument();

    rerender(<PermissionMatrix id="perm" value={{ access: "write" }} onChange={() => undefined} />);
    expect(screen.getByText(/hand out any role/i)).toBeInTheDocument();
  });
});

/**
 * A built-in role's matrix, or one shown to somebody who may read roles but
 * not write them, draws the same eight rows as a description rather than a
 * form -- no select to operate, no `onChange` it could ever call.
 */
describe("showing a permission set read-only", () => {
  it("draws a chip rather than a select for every area", () => {
    mount({ settings: "write" }, { readOnly: true });
    expect(screen.queryByLabelText("Settings level")).not.toBeInTheDocument();
    expect(screen.getByText("Write")).toBeInTheDocument();
    // Every other area is held at none, and says so rather than sitting blank.
    expect(screen.getAllByText("None").length).toBeGreaterThan(0);
  });

  it("still names approvals' held level as decide, not write", () => {
    mount({ approvals: "decide" }, { readOnly: true });
    expect(screen.getByText("Decide")).toBeInTheDocument();
  });
});
