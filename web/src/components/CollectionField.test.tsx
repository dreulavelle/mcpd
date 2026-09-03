import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, type SettingField, type SettingRow } from "@/lib/api";
import { renderWith } from "@/test/render";
import { CollectionField } from "./CollectionField";

const FIELD: SettingField = {
  key: "plugins.pbx.customers",
  label: "Customers",
  kind: "collection",
  group: "plugin:pbx",
  apply: "live",
  required: true,
  help: "One row per business.",
  columns: [
    { key: "name", label: "Business name", kind: "string", group: "", apply: "live", required: true },
    { key: "aliases", label: "Aliases", kind: "list", group: "", apply: "live" },
    { key: "host", label: "Address", kind: "string", group: "", apply: "live", required: true },
    { key: "password", label: "Password", kind: "secret", group: "", apply: "live", required: true },
  ],
};

const ROWS: SettingRow[] = [
  {
    id: "row_1", values: { name: "Acme", aliases: ["acme", "ACME Inc"], host: "acme.example" },
    secrets_set: ["password"], updated_at: "2026-09-03T10:00:00Z", updated_by: "user:alice",
  },
  {
    id: "row_2", values: { name: "Globex", aliases: [], host: "globex.example" },
    secrets_set: [], updated_at: "2026-09-03T10:00:00Z", updated_by: "user:alice",
  },
];

describe("CollectionField", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    vi.spyOn(api, "settingRows").mockResolvedValue({ field: FIELD, rows: ROWS, count: 2 });
  });

  // The table shows the non-secret columns as text and a secret column only as
  // whether it holds something. A credential never reaches the page.
  it("lists rows with secrets shown only as set or missing", async () => {
    renderWith(<CollectionField field={FIELD} readOnly={false} />);
    const acme = await screen.findByText("Acme");
    const row = acme.closest("tr")!;
    expect(within(row).getByText("acme, ACME Inc")).toBeInTheDocument();
    expect(within(row).getByText("set")).toBeInTheDocument();
    const globex = screen.getByText("Globex").closest("tr")!;
    expect(within(globex).getByText("missing")).toBeInTheDocument();
    // An empty list reads as a dash rather than as nothing at all.
    expect(within(globex).getByText("—")).toBeInTheDocument();
  });

  // Adding a row submits every column as a string, the way the settings form
  // does, and reloads the list.
  it("adds a row through the row endpoint", async () => {
    const add = vi.spyOn(api, "addSettingRow").mockResolvedValue(ROWS[0]!);
    renderWith(<CollectionField field={FIELD} readOnly={false} />);
    await screen.findByText("Acme");
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Add" }));

    const dialog = await screen.findByRole("dialog");
    await user.type(within(dialog).getByLabelText(/Business name/), "Initech");
    await user.type(within(dialog).getByLabelText(/Aliases/), "initech, init");
    await user.type(within(dialog).getByLabelText(/Address/), "initech.example");
    await user.type(within(dialog).getByLabelText(/Password/), "pw");
    await user.click(within(dialog).getByRole("button", { name: "Save" }));

    await waitFor(() => expect(add).toHaveBeenCalledWith("plugins.pbx.customers", {
      name: "Initech", aliases: "initech, init", host: "initech.example", password: "pw",
    }));
    expect(api.settingRows).toHaveBeenCalledTimes(2);
  });

  // Editing shows the stored secret as saved rather than as a value, and a
  // blank secret on save means keep.
  it("edits a row without asking for the secret again", async () => {
    const update = vi.spyOn(api, "updateSettingRow").mockResolvedValue(ROWS[0]!);
    renderWith(<CollectionField field={FIELD} readOnly={false} />);
    const acme = await screen.findByText("Acme");
    const user = userEvent.setup();
    await user.click(within(acme.closest("tr")!).getByRole("button", { name: "Edit" }));

    const dialog = await screen.findByRole("dialog");
    const password = within(dialog).getByLabelText(/Password/) as HTMLInputElement;
    expect(password.placeholder).toMatch(/Saved/);
    expect(password.value).toBe("");
    const host = within(dialog).getByLabelText(/Address/) as HTMLInputElement;
    await user.clear(host);
    await user.type(host, "new.example");
    await user.click(within(dialog).getByRole("button", { name: "Save" }));

    await waitFor(() => expect(update).toHaveBeenCalledWith("plugins.pbx.customers", "row_1", {
      name: "Acme", aliases: "acme, ACME Inc", host: "new.example", password: "",
    }, []));
  });

  // Removing asks first, in the console's own dialog, and only then calls the
  // endpoint.
  it("asks before removing a row", async () => {
    const remove = vi.spyOn(api, "removeSettingRow").mockResolvedValue(undefined);
    renderWith(<CollectionField field={FIELD} readOnly={false} />);
    const globex = await screen.findByText("Globex");
    const user = userEvent.setup();
    await user.click(within(globex.closest("tr")!).getByRole("button", { name: "Remove" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText(/Remove Globex\?/)).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(remove).toHaveBeenCalledWith("plugins.pbx.customers", "row_2"));
  });

  // Somebody who cannot write settings sees the rows and no buttons.
  it("hides every control when read-only", async () => {
    renderWith(<CollectionField field={FIELD} readOnly />);
    await screen.findByText("Acme");
    expect(screen.queryByRole("button", { name: "Add" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Edit" })).toBeNull();
  });
});
