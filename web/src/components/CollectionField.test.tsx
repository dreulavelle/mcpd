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
    id: "row_2", values: { name: "Globex", aliases: [], host: "https://globex.example/" },
    secrets_set: [], updated_at: "2026-09-03T10:00:00Z", updated_by: "user:alice",
  },
  {
    id: "row_3", values: { name: "Initech", aliases: [], host: "http://pbx.internal:5000" },
    secrets_set: [], updated_at: "2026-09-03T10:00:00Z", updated_by: "user:alice",
  },
];

describe("CollectionField", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    vi.spyOn(api, "settingRows").mockResolvedValue({ field: FIELD, rows: ROWS, count: ROWS.length });
  });

  // Each row shows the non-secret columns as labelled text and a secret column
  // only as whether it holds something. A credential never reaches the page.
  it("lists rows with secrets shown only as set or missing", async () => {
    renderWith(<CollectionField field={FIELD} readOnly={false} />);
    const acme = await screen.findByText("Acme");
    const row = acme.closest("li")!;
    expect(within(row).getByText("acme, ACME Inc")).toBeInTheDocument();
    expect(within(row).getByText("set")).toBeInTheDocument();
    const globex = screen.getByText("Globex").closest("li")!;
    expect(within(globex).getByText("missing")).toBeInTheDocument();
  });

  // A column this row has nothing in is left out of it rather than drawn as a
  // dash. An optional column is empty on most rows, and a dash on every one of
  // them is a column of nothing that still has to be read past.
  it("leaves an empty column out of the row rather than showing a dash", async () => {
    renderWith(<CollectionField field={FIELD} readOnly={false} />);
    const globex = (await screen.findByText("Globex")).closest("li")!;
    expect(within(globex).queryByText("—")).not.toBeInTheDocument();
    expect(within(globex).queryByText("Aliases")).not.toBeInTheDocument();
    // The row that does have them still names them.
    const acme = screen.getByText("Acme").closest("li")!;
    expect(within(acme).getByText("Aliases")).toBeInTheDocument();
  });

  // Nothing lays the rows out in a strip that has to be scrolled sideways.
  // A collection is as wide as the plugin declared it, and a table that does
  // not fit hides its last columns behind a gesture nobody makes.
  it("never puts the rows in a horizontally scrolling box", async () => {
    const { container } = renderWith(<CollectionField field={FIELD} readOnly={false} />);
    await screen.findByText("Acme");
    expect(container.querySelector("table")).toBeNull();
    expect(container.querySelector(".overflow-x-auto")).toBeNull();
  });

  // An https address reads without its scheme: in a table it is eight
  // characters repeated on every row. http stays, because that one is the
  // exception, and what was typed is still what is stored.
  it("shows an https address without its scheme, and leaves http alone", async () => {
    renderWith(<CollectionField field={FIELD} readOnly={false} />);
    const globex = (await screen.findByText("Globex")).closest("li")!;
    expect(within(globex).getByText("globex.example")).toBeInTheDocument();
    const initech = screen.getByText("Initech").closest("li")!;
    expect(within(initech).getByText("http://pbx.internal:5000")).toBeInTheDocument();
  });

  // An address pasted out of a browser bar brings a trailing slash that means
  // nothing here, so the form drops it on the way to the store rather than
  // letting it survive into every place the value is shown.
  it("drops a trailing slash from a string column on save", async () => {
    const add = vi.spyOn(api, "addSettingRow").mockResolvedValue(ROWS[0]!);
    renderWith(<CollectionField field={FIELD} readOnly={false} />);
    await screen.findByText("Acme");
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Add" }));

    const dialog = await screen.findByRole("dialog");
    await user.type(within(dialog).getByLabelText(/Business name/), "Initech");
    await user.type(within(dialog).getByLabelText(/Address/), "https://initech.example:5001/");
    await user.type(within(dialog).getByLabelText(/Password/), "pw/");
    await user.click(within(dialog).getByRole("button", { name: "Save" }));

    await waitFor(() => expect(add).toHaveBeenCalledWith("plugins.pbx.customers", {
      name: "Initech",
      aliases: "",
      // The port survives; the slash does not.
      host: "https://initech.example:5001",
      // A secret is bytes and reaches the store as typed.
      password: "pw/",
    }));
  });

  // Editing shows what is stored, scheme included: the tidying is the table's,
  // and a form that quietly rewrote the value would be saving something the
  // operator did not type.
  it("edits against the stored address, not the tidied one", async () => {
    renderWith(<CollectionField field={FIELD} readOnly={false} />);
    const globex = (await screen.findByText("Globex")).closest("li")!;
    const user = userEvent.setup();
    await user.click(within(globex).getByRole("button", { name: "Edit" }));
    const dialog = await screen.findByRole("dialog");
    expect((within(dialog).getByLabelText(/Address/) as HTMLInputElement).value)
      .toBe("https://globex.example/");
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
    await user.click(within(acme.closest("li")!).getByRole("button", { name: "Edit" }));

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
    await user.click(within(globex.closest("li")!).getByRole("button", { name: "Remove" }));
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

// A collection large enough to need finding, shaped the way a phone-system
// table is: the row's own name, the business it belongs to, aliases, an
// address and a secret.
const WIDE: SettingField = {
  key: "plugins.pbx.customers",
  label: "Customers",
  kind: "collection",
  group: "plugin:pbx",
  apply: "live",
  columns: [
    { key: "name", label: "Name", kind: "string", group: "", apply: "live", required: true },
    { key: "customer", label: "Business", kind: "string", group: "", apply: "live" },
    { key: "aliases", label: "Aliases", kind: "list", group: "", apply: "live" },
    { key: "host", label: "Address", kind: "string", group: "", apply: "live", required: true },
    { key: "extension", label: "Extension", kind: "string", group: "", apply: "live" },
    { key: "password", label: "Password", kind: "secret", group: "", apply: "live", required: true },
  ],
};

const MANY: SettingRow[] = [
  {
    id: "row_hq",
    values: {
      name: "Acme HQ", customer: "Acme Dental Group", aliases: ["hq", "main"],
      host: "pbx1.example", extension: "100",
    },
    secrets_set: ["password"], updated_at: "2026-09-18T10:00:00Z", updated_by: "user:alice",
  },
  {
    id: "row_branch",
    values: {
      name: "Acme Branch", customer: "Acme Dental Group", aliases: ["branch"],
      host: "pbx2.example", extension: "100",
    },
    secrets_set: ["password"], updated_at: "2026-09-18T10:00:00Z", updated_by: "user:alice",
  },
  ...Array.from({ length: 10 }, (_, i) => ({
    id: `row_${i}`,
    values: {
      name: `Globex ${i}`, customer: "Globex Roofing", aliases: [`g${i}`],
      host: `pbx${i}.globex.example`, extension: "200",
    },
    secrets_set: ["password"],
    updated_at: "2026-09-18T10:00:00Z",
    updated_by: "user:alice",
  })),
];

describe("finding a row in a large collection", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    vi.spyOn(api, "settingRows").mockResolvedValue({ field: WIDE, rows: MANY, count: MANY.length });
  });

  const names = () =>
    screen.getAllByRole("listitem").map((li) => li.querySelector("p")?.textContent);

  // The thing somebody has to hand is rarely the row's name: it is the address
  // they are looking at, the business a site belongs to, or an alias somebody
  // used in a ticket. Each of those finds the row.
  it("finds a row by its name, its business, its address or an alias", async () => {
    const user = userEvent.setup();
    renderWith(<CollectionField field={WIDE} readOnly={false} />);
    const box = await screen.findByLabelText("Find in customers");

    await user.clear(box);
    await user.type(box, "Acme Branch");
    await waitFor(() => expect(names()).toEqual(["Acme Branch"]));

    // The business, which both Acme rows share and neither is named after.
    await user.clear(box);
    await user.type(box, "Dental");
    await waitFor(() => expect(names()).toEqual(["Acme HQ", "Acme Branch"]));

    // The address.
    await user.clear(box);
    await user.type(box, "pbx2.example");
    await waitFor(() => expect(names()).toEqual(["Acme Branch"]));

    // An alias, which appears nowhere else on the row.
    await user.clear(box);
    await user.type(box, "branch");
    await waitFor(() => expect(names()).toEqual(["Acme Branch"]));
  });

  // Words in any order, because people type what they remember as they
  // remember it -- and every word has to land, so typing more narrows.
  it("takes the words in any order and narrows as more are typed", async () => {
    const user = userEvent.setup();
    renderWith(<CollectionField field={WIDE} readOnly={false} />);
    const box = await screen.findByLabelText("Find in customers");

    await user.type(box, "dental acme");
    await waitFor(() => expect(names()).toEqual(["Acme HQ", "Acme Branch"]));

    await user.type(box, " hq");
    await waitFor(() => expect(names()).toEqual(["Acme HQ"]));
  });

  // The count says what is being looked at, and a search that finds nothing
  // says so and offers the way back rather than looking like an empty table.
  it("counts the matches and offers a way back from none", async () => {
    const user = userEvent.setup();
    renderWith(<CollectionField field={WIDE} readOnly={false} />);
    const box = await screen.findByLabelText("Find in customers");
    expect(await screen.findByText("12")).toBeInTheDocument();

    await user.type(box, "Dental");
    expect(await screen.findByText("2 of 12")).toBeInTheDocument();

    await user.clear(box);
    await user.type(box, "initech");
    expect(await screen.findByText(/Nothing here matches/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Show all 12" }));
    await waitFor(() => expect(screen.getAllByRole("listitem")).toHaveLength(12));
  });

  // A short collection is read by looking, so the box is not there to be read
  // past. The threshold is on how many rows there are, not on the search.
  it("leaves the box out until there are enough rows to need it", async () => {
    vi.spyOn(api, "settingRows").mockResolvedValue({ field: WIDE, rows: MANY.slice(0, 3), count: 3 });
    renderWith(<CollectionField field={WIDE} readOnly={false} />);
    await screen.findByText("Acme HQ");
    expect(screen.queryByLabelText("Find in customers")).not.toBeInTheDocument();
  });

  // The floor is on every word, not on their mean. "globex" matched exactly
  // once carried "pbx2.example" in as scattered letters of pbx2.globex.example
  // -- the false match the floor was written to stop.
  it("does not let one exact word carry a loose one", async () => {
    const user = userEvent.setup();
    renderWith(<CollectionField field={WIDE} readOnly={false} />);
    const box = await screen.findByLabelText("Find in customers");
    await user.type(box, "globex pbx2.example");
    expect(await screen.findByText(/Nothing here matches/)).toBeInTheDocument();
  });

  // An address is most often pasted from a browser, scheme and all. The list
  // shows it without one; the search still has to find it with one.
  it("finds an address pasted with its scheme", async () => {
    const rows = MANY.map((r) => r.id === "row_branch"
      ? { ...r, values: { ...r.values, host: "https://pbx2.example" } }
      : r);
    vi.spyOn(api, "settingRows").mockResolvedValue({ field: WIDE, rows, count: rows.length });
    const user = userEvent.setup();
    renderWith(<CollectionField field={WIDE} readOnly={false} />);
    const box = await screen.findByLabelText("Find in customers");
    await user.type(box, "https://pbx2.example");
    await waitFor(() => expect(names()).toEqual(["Acme Branch"]));
  });

  // A search that arrives in a link applies on a short list too, and there
  // the box and the count have to come with it: rows hidden by a filter
  // nobody can see read as rows that were deleted.
  it("shows the box whenever a search is in force, however short the list", async () => {
    vi.spyOn(api, "settingRows").mockResolvedValue({ field: WIDE, rows: MANY.slice(0, 3), count: 3 });
    renderWith(<CollectionField field={WIDE} readOnly={false} />, { path: "/?customers=branch" });
    await waitFor(() => expect(names()).toEqual(["Acme Branch"]));
    expect(screen.getByLabelText("Find in customers")).toHaveValue("branch");
    expect(screen.getByText("1 of 3")).toBeInTheDocument();
  });

  // A row saved while the list is narrowed stays on it. Otherwise adding one
  // that does not match saves it straight out of sight, and the only sign it
  // worked is the count going up.
  it("keeps a row just saved in view even when it does not match", async () => {
    const added: SettingRow = {
      id: "row_new", values: { name: "Initech", host: "pbx.initech.example" },
      secrets_set: ["password"], updated_at: "2026-09-18T10:00:00Z", updated_by: "user:alice",
    };
    const rows = vi.spyOn(api, "settingRows")
      .mockResolvedValueOnce({ field: WIDE, rows: MANY, count: MANY.length })
      .mockResolvedValue({ field: WIDE, rows: [...MANY, added], count: MANY.length + 1 });
    vi.spyOn(api, "addSettingRow").mockResolvedValue(added);
    const user = userEvent.setup();
    renderWith(<CollectionField field={WIDE} readOnly={false} />, { path: "/?customers=dental" });
    await waitFor(() => expect(names()).toEqual(["Acme HQ", "Acme Branch"]));

    await user.click(screen.getByRole("button", { name: "Add" }));
    const dialog = await screen.findByRole("dialog");
    await user.type(within(dialog).getByLabelText(/^Name/), "Initech");
    await user.type(within(dialog).getByLabelText(/Address/), "pbx.initech.example");
    await user.type(within(dialog).getByLabelText(/Password/), "pw");
    await user.click(within(dialog).getByRole("button", { name: "Save" }));

    await waitFor(() => expect(rows).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(names()).toEqual(["Acme HQ", "Acme Branch", "Initech"]));
  });
});
