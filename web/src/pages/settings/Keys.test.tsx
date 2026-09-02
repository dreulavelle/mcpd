import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, type ApiKey, type Group } from "@/lib/api";
import { renderWith } from "@/test/render";
import { Keys } from "./Keys";

function keyView(overrides: Partial<ApiKey> = {}): ApiKey {
  return {
    id: "key_1",
    name: "Nightly report",
    role: "user",
    plugins: [],
    reaches: [],
    groups: [],
    status: "active",
    created_by: "user:admin@example.com",
    created_at: "2026-08-01T09:00:00Z",
    ...overrides,
  };
}

function stub({ keys = [] as ApiKey[], groups = [] as Group[] } = {}) {
  vi.spyOn(api, "keys").mockResolvedValue({ keys, count: keys.length });
  vi.spyOn(api, "groups").mockResolvedValue({ groups, count: groups.length });
}

const SECRET = "mcpd_thisIsTheOnlyTimeItIsEverShown0123456789";

describe("the keys page", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    window.localStorage.clear();
    window.sessionStorage.clear();
  });

  // A key with nothing of its own and no groups reaches nothing, and the page
  // says so rather than leaving the column blank.
  it("says a key with no grants reaches nothing", async () => {
    stub({ keys: [keyView()] });
    renderWith(<Keys />);

    await screen.findByText("Nightly report");
    expect(screen.getByText("Nothing")).toBeInTheDocument();
  });

  // What a key reaches is its own grant unioned with its groups', and the page
  // shows the union. Showing only its own would disagree with what the server
  // actually lets it do.
  it("shows what a key reaches, not only what is listed against it", async () => {
    stub({
      keys: [keyView({
        plugins: ["echo"],
        reaches: ["cnmaestro", "echo"],
        groups: [{ id: "grp_1", name: "Field", plugins: ["cnmaestro"] }],
      })],
    });
    renderWith(<Keys />);

    await screen.findByText("Nightly report");
    expect(screen.getByText("cnmaestro, echo")).toBeInTheDocument();
    expect(screen.getByText("Field")).toBeInTheDocument();
  });

  // The secret exists once. The dialog says so, it is easy to copy, and
  // closing takes it out of the document rather than hiding it there.
  it("shows a new secret once and leaves nothing behind when the dialog closes", async () => {
    stub();
    vi.spyOn(api, "createKey").mockResolvedValue({
      key: keyView(), secret: SECRET,
    });
    renderWith(<Keys />);

    await userEvent.click(await screen.findByRole("button", { name: "Add key" }));
    await userEvent.type(screen.getByLabelText("Name"), "Nightly report");
    // Two now: the one that opened the form, and the form's own submit.
    const submit = screen.getAllByRole("button", { name: "Add key" }).at(-1)!;
    await userEvent.click(submit);

    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent(SECRET);
    // The warning has to be on the dialog, not in a tooltip somebody has to
    // find: "I will copy it later" is the mistake this exists to prevent.
    expect(dialog).toHaveTextContent(/only time/i);
    expect(screen.getByRole("button", { name: /copy/i })).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "I have copied it" }));

    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(document.body.textContent).not.toContain(SECRET);
    expect(document.body.innerHTML).not.toContain(SECRET);
    // And nothing stashed it somewhere a later page could read it back.
    expect(JSON.stringify(window.localStorage)).not.toContain(SECRET);
    expect(JSON.stringify(window.sessionStorage)).not.toContain(SECRET);
  });

  // Revoking is not deleting: the row stays so the history can still say which
  // key acted, and the page shows what happened to it.
  it("marks a revoked key rather than dropping it", async () => {
    stub({ keys: [keyView({ status: "revoked", revoked_at: "2026-08-10T09:00:00Z" })] });
    renderWith(<Keys />);

    await screen.findByText("Nightly report");
    expect(screen.getByText("revoked")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Revoke" })).toBeDisabled();
  });

  // Expired and revoked are different facts, and an operator chasing a
  // connector that stopped working needs to know which.
  it("tells an expired key apart from a revoked one", async () => {
    stub({ keys: [keyView({ status: "expired", expires_at: "2026-08-10T09:00:00Z" })] });
    renderWith(<Keys />);

    await screen.findByText("Nightly report");
    expect(screen.getByText("expired")).toBeInTheDocument();
    expect(screen.queryByText("revoked")).toBeNull();
  });

  // Tokens in the configuration file keep working, and an operator standing in
  // front of an empty page needs to know that before they conclude otherwise.
  it("says file tokens still work when there are no keys", async () => {
    stub();
    renderWith(<Keys />);

    expect(await screen.findByText(/configuration file keep working/i))
      .toBeInTheDocument();
  });

  // Only what changed is sent, so renaming a key does not also rewrite its
  // grant with whatever the form happened to hold.
  it("re-scopes a key without reissuing it, sending only what changed", async () => {
    stub({ keys: [keyView({ plugins: ["echo"] })] });
    const update = vi.spyOn(api, "updateKey").mockResolvedValue(keyView({ name: "Weekly" }));
    renderWith(<Keys />);

    await userEvent.click(await screen.findByRole("button", { name: "Edit" }));
    const name = await screen.findByLabelText("Name");
    await userEvent.clear(name);
    await userEvent.type(name, "Weekly report");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(update).toHaveBeenCalledWith("key_1", { name: "Weekly report" }));
  });

  it("offers nothing to save until something changes", async () => {
    stub({ keys: [keyView()] });
    renderWith(<Keys />);
    await userEvent.click(await screen.findByRole("button", { name: "Edit" }));
    expect(await screen.findByRole("button", { name: "Nothing to save" })).toBeDisabled();
  });

  it("narrows a long list by name, id or group", async () => {
    const keys = Array.from({ length: 10 }, (_, i) =>
      keyView({ id: `key_${i}`, name: i === 3 ? "Backup runner" : `Script ${i}` }));
    stub({ keys });
    renderWith(<Keys />);
    await screen.findByText("Backup runner");
    await userEvent.type(screen.getByLabelText("Find a key"), "backup");
    await waitFor(() => expect(screen.queryByText("Script 1")).not.toBeInTheDocument());
    expect(screen.getByText("Backup runner")).toBeInTheDocument();
  });
});
