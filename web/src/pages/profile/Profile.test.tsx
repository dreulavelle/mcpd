import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, ApiError, type Session, type User } from "@/lib/api";
import { builtinPermissions } from "@/lib/permissions";
import { renderWith, sessionFor } from "@/test/render";
import { Profile } from "./Profile";

function userView(overrides: Partial<User> = {}): User {
  return {
    id: "usr_1",
    email: "user@example.com",
    name: "Alice A.",
    display_name: "Alice A.",
    role: "role_operator",
    role_name: "Operator",
    grants: [{ plugin: "*", level: "write" }],
    reaches: [{ plugin: "*", level: "write" }],
    permissions: builtinPermissions("role_operator"),
    groups: [],
    disabled: false,
    status: "active",
    has_password: true,
    created_at: "2026-01-01T00:00:00Z",
    self: true,
    ...overrides,
  };
}

function mount(session: Session, onSession?: (s: Session) => void) {
  return renderWith(<Profile />, { session, onSession });
}

beforeEach(() => {
  vi.spyOn(api, "meta").mockResolvedValue({
    version: "dev", auth_mode: "static", needs_setup: false,
  });
  vi.spyOn(api, "users").mockResolvedValue({ users: [userView()], count: 1 });
});

/**
 * Naming yourself is the one thing about an account its holder is the
 * authority on, and `PATCH /api/account` takes no capability beyond being
 * signed in. Before it existed this page could only tell a non-administrator
 * to go and ask somebody else to type their name for them.
 */
describe("naming yourself", () => {
  it("offers the form to a plain user, not a sentence about asking an admin", async () => {
    mount(sessionFor("user"));

    expect(await screen.findByLabelText("Display name")).toBeInTheDocument();
    expect(screen.queryByText(/ask an administrator until/i)).not.toBeInTheDocument();
  });

  it("writes through the account endpoint, which carries no identifier", async () => {
    const update = vi.spyOn(api, "updateAccount")
      .mockResolvedValue(userView({ name: "Alice A.", display_name: "Alice A." }));
    const updateUser = vi.spyOn(api, "updateUser");
    mount(sessionFor("user", { display_name: "" }));

    await userEvent.type(await screen.findByLabelText("Display name"), "  Alice A.  ");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(update).toHaveBeenCalledWith("Alice A."));
    // Not the administrator's route, which addresses another account by id.
    expect(updateUser).not.toHaveBeenCalled();
  });

  /**
   * The sidebar names you from the session, so a rename that did not reach it
   * left the console showing the old name until somebody reloaded.
   */
  it("pushes the server's answer back into the session", async () => {
    vi.spyOn(api, "updateAccount")
      .mockResolvedValue(userView({ name: "Alice A.", display_name: "Alice A." }));
    const adopted = vi.fn();
    mount(sessionFor("user", { display_name: "" }), adopted);

    await userEvent.type(await screen.findByLabelText("Display name"), "Alice A.");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(adopted).toHaveBeenCalledWith(
      expect.objectContaining({ name: "Alice A.", display_name: "Alice A." }),
    ));
  });

  /**
   * A name that collides with another account's address is refused, and the
   * server's own sentence is what says so. Paraphrasing it here would mean
   * guessing which of several refusals happened.
   */
  it("shows the server's refusal rather than a sentence of its own", async () => {
    vi.spyOn(api, "updateAccount").mockRejectedValue(
      new ApiError(409, "conflict", "that display name is another account's address"),
    );
    mount(sessionFor("user", { display_name: "" }));

    await userEvent.type(await screen.findByLabelText("Display name"), "bob@example.com");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(await screen.findByText(/another account's address/)).toBeInTheDocument();
  });

  it("lets a name be cleared, which is a real edit", async () => {
    const update = vi.spyOn(api, "updateAccount")
      .mockResolvedValue(userView({ name: "user@example.com", display_name: "" }));
    mount(sessionFor("user", { display_name: "Alice A." }));

    await userEvent.clear(await screen.findByLabelText("Display name"));
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(update).toHaveBeenCalledWith(""));
  });
});

/**
 * `name` is resolved and never empty; `display_name` is stored and may be.
 * Rendering the raw one puts a blank where a heading belongs, and seeding the
 * edit field with the resolved one offers to save the address as a name.
 */
describe("which of the two names is shown where", () => {
  it("heads the page with the resolved name", async () => {
    mount(sessionFor("admin", { display_name: "An Admin" }));
    expect(await screen.findByRole("heading", { name: "An Admin" })).toBeInTheDocument();
  });

  it("falls back to the address in the heading, and leaves the field empty", async () => {
    mount(sessionFor("user", { display_name: "" }));

    expect(await screen.findByRole("heading", { name: "user@example.com" }))
      .toBeInTheDocument();
    // Empty, with the address only as a placeholder: the fallback is not a
    // value somebody should be able to save by pressing the button.
    expect(screen.getByLabelText("Display name")).toHaveValue("");
    expect(screen.getByLabelText("Display name"))
      .toHaveAttribute("placeholder", "user@example.com");
  });
});
