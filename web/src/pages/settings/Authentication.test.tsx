import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, ApiError, type PendingRegistration, type ProviderName } from "@/lib/api";
import { renderWith, sessionFor } from "@/test/render";
import { Authentication } from "./Authentication";

function pendingUser(overrides: Partial<PendingRegistration> = {}): PendingRegistration {
  return {
    id: "usr_9",
    email: "newcomer@example.com",
    name: "newcomer@example.com",
    display_name: "",
    role: "role_operator",
    role_name: "Operator",
    grants: [],
    reaches: [],
    permissions: [],
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

/** The Entra group as the server describes it, trimmed to what these tests read. */
const entraGroup = {
  name: "entra", title: "Microsoft Entra", section: "authentication" as const,
  enabled_by: "auth.entra.enabled",
  fields: [
    { key: "auth.entra.enabled", label: "Offer Microsoft", kind: "bool" as const, group: "entra", apply: "live" as const },
    { key: "auth.entra.client_id", label: "Application (client) ID", kind: "string" as const, group: "entra", apply: "live" as const, required: true },
    { key: "auth.entra.client_secret", label: "Client secret", kind: "secret" as const, group: "entra", apply: "live" as const, required: true },
    { key: "auth.entra.tenant_id", label: "Directory (tenant) ID", kind: "string" as const, group: "entra", apply: "live" as const, required: true },
  ],
};

/**
 * A second group, because the form draws a group's title -- and the status
 * beside it -- only when there is more than one.
 */
const sessionsGroup = {
  name: "sessions", title: "Sessions", section: "authentication" as const,
  fields: [
    { key: "auth.accounts.session_ttl_hours", label: "Sign people out regardless after", kind: "duration" as const, group: "sessions", apply: "live" as const },
  ],
};

function stub({
  waiting = [] as PendingRegistration[],
  base = "https://mcpd.example.com",
  values = {} as Record<string, unknown>,
  secrets = {} as Record<string, boolean>,
  offered = [] as ProviderName[],
  refusals = {} as Partial<Record<ProviderName, string>>,
} = {}) {
  vi.spyOn(api, "settings").mockResolvedValue({
    groups: [sessionsGroup, entraGroup], values, secrets_set: secrets, encryption_available: true,
    bootstrap: [],
  });
  vi.spyOn(api, "registrations").mockResolvedValue({
    registrations: waiting, count: waiting.length,
  });
  vi.spyOn(api, "groups").mockResolvedValue({ groups: [], count: 0 });
  vi.spyOn(api, "redirectURIs").mockResolvedValue({
    base,
    redirect_uris: base
      ? {
        google: `${base}/api/auth/sso/google/callback`,
        entra: `${base}/api/auth/sso/entra/callback`,
      }
      : {},
    refusals,
  });
  vi.spyOn(api, "authOptions").mockResolvedValue({
    providers: offered.map((p) => ({ provider: p, label: p })),
    registration: false,
  });
}

async function entraCard(): Promise<HTMLElement> {
  await screen.findByText("Microsoft Entra");
  return document.getElementById("settings-group-entra")!;
}

function mount() {
  return renderWith(<Authentication />, { session: sessionFor("admin") });
}

describe("the authentication page", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });

  // A URL assembled from the browser's location is right on the machine an
  // operator tested it from and wrong everywhere else, so none is shown.
  // What is shown instead is the box that fixes it: this notice used to send
  // people to set a "Dashboard address" that no field on any page was called.
  it("asks for this page's address where it is needed, and shows no address until it is saved", async () => {
    stub({ base: "" });
    mount();

    expect(await screen.findByText(/save the address this page is on first/i)).toBeInTheDocument();
    expect(screen.getByLabelText("Address this page is on")).toBeInTheDocument();
    expect(screen.queryByText(/api\/auth\/sso/)).toBeNull();
  });

  it("saves the address through settings, and offers this page's own only as a suggestion", async () => {
    stub({ base: "" });
    const save = vi.spyOn(api, "saveSettings").mockResolvedValue({ applied: ["server.frontend_public_url"] });
    mount();

    const box = await screen.findByLabelText("Address this page is on");
    // Filling the box is not saving it: what is right from this desk may be
    // wrong through a proxy, so the person still says yes.
    await userEvent.click(screen.getByRole("button", { name: `Use ${window.location.origin}` }));
    expect(box).toHaveValue(window.location.origin);
    expect(save).not.toHaveBeenCalled();

    await userEvent.clear(box);
    await userEvent.type(box, "https://mcpd.example.com");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(save).toHaveBeenCalledWith(
      { "server.frontend_public_url": "https://mcpd.example.com" }));
  });

  it("says what was wrong with an address the server would not take", async () => {
    stub({ base: "" });
    vi.spyOn(api, "saveSettings").mockRejectedValue(new ApiError(400, "invalid_settings", "", undefined, [
      "settings: Address this page is on must start with http:// or https://",
    ]));
    mount();

    await userEvent.type(await screen.findByLabelText("Address this page is on"), "mcpd.example.com");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText("Address this page is on must start with http:// or https://"))
      .toBeInTheDocument();
  });

  // The card used to render nothing when it could not load, and an empty
  // space where the addresses belong reads as a host that has none.
  it("says so when the addresses could not be loaded, rather than leaving a gap", async () => {
    stub();
    vi.spyOn(api, "redirectURIs").mockRejectedValue(new Error("boom"));
    mount();

    expect(await screen.findByText(/couldn't load the addresses/i)).toBeInTheDocument();
  });

  // Setting a provider up means pasting an exact address into somebody else's
  // console, and getting it wrong fails at the provider with a message that
  // says nothing useful, so the address is shown rather than described. It
  // sits in the provider's own card with everything else that provider
  // needs, and it is there before the provider is switched on: registering at
  // the provider is what produces the client ID, so the address comes first.
  it("puts the callback address in the provider's own card, switched on or not", async () => {
    stub();
    mount();

    const card = await entraCard();
    expect(within(card).getByText("https://mcpd.example.com/api/auth/sso/entra/callback"))
      .toBeInTheDocument();
    expect(within(card).getByText("Off")).toBeInTheDocument();
    // The steps are for when somebody has decided to set it up.
    expect(within(card).queryByText(/Single-page application/)).toBeNull();
  });

  it("says in the card when a provider will refuse the address", async () => {
    stub({ refusals: { entra: "Microsoft accepts only https here, except on localhost." } });
    mount();

    expect(within(await entraCard()).getByText(/only https/)).toBeInTheDocument();
  });

  // Switched on and offered are different facts, and the gap between them was
  // a switch that was on and a button that never appeared.
  it("says a provider switched on is not offered yet, and what it still needs", async () => {
    stub({ values: { "auth.entra.enabled": true, "auth.entra.client_id": "11111111-1111-1111-1111-111111111111" } });
    mount();

    const card = await entraCard();
    expect(within(card).getByText("Not offered yet")).toBeInTheDocument();
    expect(within(card).getByText(/until Client secret and Directory \(tenant\) ID are saved/))
      .toBeInTheDocument();
  });

  it("says a provider is on the sign-in page when the sign-in page offers it", async () => {
    stub({
      values: {
        "auth.entra.enabled": true,
        "auth.entra.client_id": "11111111-1111-1111-1111-111111111111",
        "auth.entra.tenant_id": "22222222-2222-2222-2222-222222222222",
      },
      secrets: { "auth.entra.client_secret": true },
      offered: ["entra"],
    });
    mount();

    expect(within(await entraCard()).getByText("On the sign-in page")).toBeInTheDocument();
  });

  // The two answers people get wrong at Entra are the platform and the
  // secret's Value, and both are named where the boxes are.
  it("walks through the Entra registration once Entra is switched on", async () => {
    stub({ values: { "auth.entra.enabled": true } });
    mount();

    const card = await entraCard();
    expect(within(card).getByText("Register mcpd with Microsoft")).toBeInTheDocument();
    expect(within(card).getByText(/not Single-page/)).toBeInTheDocument();
    expect(within(card).getByText(/not its Secret ID/)).toBeInTheDocument();
  });

  // A host that has never had a registration should not carry an empty table
  // saying so.
  it("shows no queue when nobody is waiting", async () => {
    stub();
    mount();

    await screen.findByText(/callback addresses are built from/i);
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

    const queue = (await screen.findByText("Waiting for you")).closest("div[data-slot=card]");
    // Scoped to the queue: a provider's own card on the same page names it
    // too, and an unscoped match would pass for the wrong reason.
    expect(within(queue as HTMLElement).getByText("Google")).toBeInTheDocument();
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
