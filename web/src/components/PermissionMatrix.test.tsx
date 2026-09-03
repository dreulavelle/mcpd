import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { PermissionSet } from "@/lib/permissions";
import { PermissionMatrix } from "./PermissionMatrix";

/** Renders the matrix and reports what it last handed back. */
function mount(value: PermissionSet = {}, extra: { disabled?: boolean; readOnly?: boolean } = {}) {
  const onChange = vi.fn();
  render(<PermissionMatrix id="perm" value={value} onChange={onChange} {...extra} />);
  return onChange;
}

/** The radio group for one area's row, found by its "<Area> level" label. */
function group(areaLabel: string) {
  return screen.getByRole("radiogroup", { name: `${areaLabel} level` });
}

/** The option currently checked within an area's row. */
function held(areaLabel: string): string {
  return within(group(areaLabel)).getByRole("radio", { checked: true }).textContent ?? "";
}

/**
 * Eight rows, each a level in one area -- the vocabulary the server actually
 * enforces, so a typo here is a refused save rather than a stored permission
 * nobody holds. Each row is a radiogroup of None / Read / Write (Decide, for
 * approvals) rather than a native select.
 */
describe("editing a permission set", () => {
  it("starts every area at none when the set holds nothing", () => {
    mount();
    expect(held("Settings")).toBe("None");
    expect(held("Access")).toBe("None");
  });

  it("shows what the set already holds", () => {
    mount({ settings: "write", history: "read" });
    expect(held("Settings")).toBe("Write");
    expect(held("History")).toBe("Read");
    expect(held("Access")).toBe("None");
  });

  it("raises one area's level without touching the others", async () => {
    const onChange = mount({ history: "read" });

    await userEvent.click(within(group("Settings")).getByRole("radio", { name: "Write" }));
    expect(onChange).toHaveBeenCalledWith({ history: "read", settings: "write" });
  });

  // "None" is a real choice -- taking a permission away -- and has to remove
  // the key rather than leave a "none" value sitting in the set the server
  // would have to also know how to ignore.
  it("drops the area from the set when it is put back to none", async () => {
    const onChange = mount({ settings: "write", history: "read" });

    await userEvent.click(within(group("Settings")).getByRole("radio", { name: "None" }));
    expect(onChange).toHaveBeenCalledWith({ history: "read" });
  });

  // Approvals is decided, not written -- the row offers "read" and "decide",
  // never "write", because the server has no such level for that area.
  it("offers approvals read-or-decide rather than read-or-write", () => {
    mount();
    const options = within(group("Approvals")).getAllByRole("radio").map((o) => o.textContent);
    expect(options).toEqual(["None", "Read", "Decide"]);
  });

  it("offers every other area read-or-write", () => {
    mount();
    const options = within(group("Settings")).getAllByRole("radio").map((o) => o.textContent);
    expect(options).toEqual(["None", "Read", "Write"]);
  });

  it("sets approvals to decide, not write", async () => {
    const onChange = mount();

    await userEvent.click(within(group("Approvals")).getByRole("radio", { name: "Decide" }));
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
 * form -- disabled radios rather than a select, and nothing that could ever
 * call `onChange`.
 */
describe("showing a permission set read-only", () => {
  it("draws a chip rather than a select for every area", () => {
    mount({ settings: "write" }, { readOnly: true });
    // No select anywhere, and no radio a pointer could act on.
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
    for (const radio of screen.getAllByRole("radio")) expect(radio).toBeDisabled();
    expect(held("Settings")).toBe("Write");
    // Every other area is held at none, and says so rather than sitting blank.
    expect(held("Access")).toBe("None");
  });

  it("still names approvals' held level as decide, not write", () => {
    mount({ approvals: "decide" }, { readOnly: true });
    expect(held("Approvals")).toBe("Decide");
  });
});
