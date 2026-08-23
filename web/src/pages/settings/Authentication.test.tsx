import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, type PendingRegistration } from "@/lib/api";
import { renderWith, sessionFor } from "@/test/render";
import { Authentication } from "./Authentication";

function pendingUser(overrides: Partial<PendingRegistration> = {}): PendingRegistration {
  return {
    id: "usr_9",
    email: "newcomer@example.com",
    name: "newcomer@example.com",
    display_name: "",
    role: "user",
    plugins: ["*"],
    reaches: ["*"],
    groups: [],
    disabled: false,
    status: "pending",
    has_password: false,
    created_at: "2026-08-23T09:00:00Z",
    self: false,
    providers: [],
    ...overrides,
  };
}

function stub({ waiting = [] as PendingRegistration[], base = "https://mcpd.example.com" } = {}) {
  vi.spyOn(api, "settings").mockResolvedValue({
    groups: [], values: {}, secrets_set: {}, encryption_available: true,
    bootstrap: [],
  });
  vi.spyOn(api, "registrations").mockResolvedValue({
    registrations: waiting, count: waiting.length,
  });
  vi.spyOn(api, "groups").mockResolvedValue({ groups: [], count: 0 });
  vi.spyOn(api, "redirectURIs").mockResolvedValue({
    base,
    redirect_uris: base
      ? { google: `${base}/api/auth/sso/google/callback` }
      : {},
  });
}

function mount() {
  return renderWith(<Authentication />, { session: sessionFor("admin") });
}

describe("the authentication page", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });

  // Setting a provider up means pasting an exact address into somebody else's
  // console, and getting it wrong fails at the provider with a message that
  // says nothing useful. So the page shows the address rather than describing
  // how to build one.
  it("shows the exact address to register with a provider", async () => {
    stub();
    mount();

    expect(await screen.findByText("https://mcpd.example.com/api/auth/sso/google/callback"))
      .toBeInTheDocument();
  });

  // A URL assembled from the browser's location is right on the machine an
  // operator tested it from and wrong everywhere else, so none is offered.
  it("says so plainly when this host does not know its own address", async () => {
    stub({ base: "" });
    mount();

    expect(await screen.findByText(/does not know its own/i)).toBeInTheDocument();
    expect(screen.queryByText(/api\/auth\/sso/)).toBeNull();
  });

  // A host that has never had a registration should not carry an empty table
  // saying so.
  it("shows no queue when nobody is waiting", async () => {
    stub();
    mount();

    await screen.findByText("Redirect addresses");
    expect(screen.queryByText("Waiting for you")).toBeNull();
  });

  it("lists who is waiting, and says what proved the address", async () => {
    stub({ waiting: [pendingUser()] });
    mount();

    expect(await screen.findByText("Waiting for you")).toBeInTheDocument();
    expect(screen.getByText("newcomer@example.com")).toBeInTheDocument();
    // What proved the address is what an approval turns on, so the row says it
    // rather than leaving an administrator to guess.
    expect(screen.getByText("A password — unchecked")).toBeInTheDocument();
  });

  // "alice@corp.com, proved by your directory" and "alice@corp.com, typed into
  // a form" are the same string and completely different facts. Approving is a
  // privilege grant, so the row says which one it is.
  it("names the provider a registration arrived through", async () => {
    stub({ waiting: [pendingUser({ has_password: false, providers: ["Google"] })] });
    mount();

    await screen.findByText("Waiting for you");
    expect(screen.getByText("Google")).toBeInTheDocument();
    expect(screen.queryByText("A password — unchecked")).toBeNull();
  });

  it("approves a registration through the endpoint that records the grant", async () => {
    stub({ waiting: [pendingUser()] });
    const approve = vi.spyOn(api, "approveRegistration")
      .mockResolvedValue(pendingUser({ status: "active" }));
    mount();

    await screen.findByText("Waiting for you");
    await userEvent.click(screen.getByRole("button", { name: "Approve" }));

    // No group chosen means no group assigned, which is what an empty list
    // says. Approving still grants nothing beyond the account itself.
    await waitFor(() => expect(approve).toHaveBeenCalledWith("usr_9", []));
  });
});
