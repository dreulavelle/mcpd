import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { SettingField, SettingsPayload } from "@/lib/api";
import { SettingsForm } from "./SettingsForm";

const field = (f: Partial<SettingField> & { key: string; label: string }): SettingField => ({
  kind: "string", group: "g", apply: "live", ...f,
});

/** The shape of a form that selects between two ways of reaching one upstream. */
const fields: SettingField[] = [
  field({ key: "backend", label: "Backend", kind: "enum", options: ["api", "database"] }),
  field({ key: "token", label: "API token", show_when: { field: "backend", equals: ["api"] } }),
  field({ key: "db_host", label: "Database host", show_when: { field: "backend", equals: ["database"] } }),
  field({ key: "max_items", label: "Most items", kind: "int" }),
];

function renderForm(values: Record<string, unknown>) {
  const settings: SettingsPayload = {
    groups: [], values, secrets_set: {}, encryption_available: true, bootstrap: [],
  };
  render(
    <SettingsForm
      groups={[{ name: "g", title: "Observium", section: "plugins", fields }]}
      settings={settings}
      onSaved={() => {}}
    />,
  );
}

// A dropdown should ask the question the operator can answer. The stored
// value says what changes; the label says what they know they have.
describe("option labels", () => {
  it("shows a field's own labels rather than the stored values", () => {
    render(
      <SettingsForm
        groups={[{ name: "g", title: "Observium", section: "plugins", fields: [
          field({
            key: "backend", label: "Which Observium is this", kind: "enum",
            options: ["database", "api"],
            option_labels: { database: "Community Edition", api: "Subscription" },
          }),
        ] }]}
        settings={{ groups: [], values: { backend: "database" }, secrets_set: {},
          encryption_available: true, bootstrap: [] }}
        onSaved={() => {}}
      />,
    );

    expect(screen.getByRole("option", { name: "Community Edition" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "Subscription" })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: "database" })).not.toBeInTheDocument();
  });

  it("falls back to the value when a label is not declared", () => {
    render(
      <SettingsForm
        groups={[{ name: "g", title: "T", section: "plugins", fields: [
          field({ key: "mode", label: "Mode", kind: "enum", options: ["fast", "slow"] }),
        ] }]}
        settings={{ groups: [], values: { mode: "fast" }, secrets_set: {},
          encryption_available: true, bootstrap: [] }}
        onSaved={() => {}}
      />,
    );
    expect(screen.getByRole("option", { name: "fast" })).toBeInTheDocument();
  });
});

// A flat form showing every field for both backends leaves the operator to
// work out which half applies, which is how integrations get misconfigured.
describe("conditional fields", () => {
  it("shows only what the chosen backend needs", () => {
    renderForm({ backend: "api" });

    expect(screen.getByLabelText("API token")).toBeInTheDocument();
    expect(screen.queryByLabelText("Database host")).not.toBeInTheDocument();
    // A field with no rule is always shown.
    expect(screen.getByLabelText("Most items")).toBeInTheDocument();
  });

  it("swaps which fields are shown when the control changes", () => {
    renderForm({ backend: "database" });

    expect(screen.getByLabelText("Database host")).toBeInTheDocument();
    expect(screen.queryByLabelText("API token")).not.toBeInTheDocument();
  });

  it("follows the draft, not only the stored value", async () => {
    renderForm({ backend: "api" });
    expect(screen.getByLabelText("API token")).toBeInTheDocument();

    await userEvent.selectOptions(screen.getByLabelText("Backend"), "database");

    expect(screen.getByLabelText("Database host")).toBeInTheDocument();
    expect(screen.queryByLabelText("API token")).not.toBeInTheDocument();
  });
});
