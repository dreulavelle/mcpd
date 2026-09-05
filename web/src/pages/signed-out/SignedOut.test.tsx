import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, ApiError, type Meta, type Session } from "@/lib/api";
import { builtinPermissions } from "@/lib/permissions";
import { consumeSSOOutcome, resetSSOOutcome } from "@/lib/sso";
import App from "@/App";
import { AwaitingApproval, SignIn } from "./SignedOut";

const META: Meta = { version: "dev", auth_mode: "static", needs_setup: false };

const SESSION: Session = {
  email: "newcomer@example.com",
  name: "newcomer@example.com",
  display_name: "",
  role: "role_operator",
  role_name: "Operator",
  plugins: ["*"],
  grants: [{ plugin: "*", level: "write" }],
  csrf_token: "test-csrf",
  expires_at: "2026-08-24T00:00:00Z",
  status: "active",
  has_password: true,
  permissions: builtinPermissions("role_operator"),
};

/**
 * What the signed-out screen offers is what this host actually accepts.
 *
 * A button for a provider nobody configured reads as mcpd being broken rather
 * than as it not having been set up, and a sign-up form on a host that refuses
 * sign-ups is a form that only ever produces a refusal.
 */
describe("the sign-in screen", () => {
  it("shows only the password form on a host with nothing else turned on", () => {
    render(<SignIn auth={{ providers: [], registration: false }}
      onDone={() => undefined} />);

    expect(screen.getByLabelText("Password")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Continue with/ })).toBeNull();
    expect(screen.queryByRole("button", { name: "Ask for one" })).toBeNull();
  });

  // Null is what a host that could not be asked looks like. Drawing the
  // password form is the honest fallback; guessing at buttons is not.
  it("shows only the password form when this host could not be asked", () => {
    render(<SignIn auth={null} onDone={() => undefined} />);

    expect(screen.getByLabelText("Password")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Continue with/ })).toBeNull();
  });

  it("offers the providers this host has", () => {
    render(<SignIn
      auth={{
        providers: [
          { provider: "google", label: "Google" },
          { provider: "github", label: "GitHub" },
        ],
        registration: false,
      }}
      onDone={() => undefined}
    />);

    expect(screen.getByRole("button", { name: "Continue with Google" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Continue with GitHub" })).toBeInTheDocument();
  });

  // A provider the operator runs is called whatever they call it. The server
  // decides that name and sends it here; this page must not substitute one of
  // its own, or a button would say something the Authentication page does not.
  it("calls the operator's own provider what they named it", async () => {
    const start = vi.spyOn(api, "ssoStart")
      .mockResolvedValue({ authorization_url: "https://auth.example.com/authorize" });

    render(<SignIn
      auth={{ providers: [{ provider: "oidc", label: "Authentik" }], registration: false }}
      onDone={() => undefined}
    />);

    const button = screen.getByRole("button", { name: "Continue with Authentik" });
    expect(button).toBeInTheDocument();

    await userEvent.click(button);
    // Named by what it is, not by what it is called: two hosts both labelled
    // "Authentik" would start the same flow, and the label is not the address.
    expect(start).toHaveBeenCalledWith("oidc");
  });

  // The password form always waits, whatever the host's approval setting says,
  // and it says so before somebody fills it in rather than afterwards.
  //
  // Nothing between this form and the row has checked that the person can
  // receive mail at the address they typed, which is the difference between it
  // and the provider buttons above it. `approval` is deliberately not on
  // AuthOptions any more: a field saying "false" would have this form promise
  // something that is not true.
  it("says an account will wait for an administrator", async () => {
    render(<SignIn
      auth={{ providers: [], registration: true }}
      onDone={() => undefined}
    />);

    await userEvent.click(screen.getByRole("button", { name: "Ask for one" }));
    expect(screen.getByRole("heading", { name: "Ask for an account" }))
      .toBeInTheDocument();
    expect(screen.getByText(/administrator has to say yes/i)).toBeInTheDocument();
    expect(screen.queryByText(/as soon as this is done/i)).toBeNull();
  });

  // Taken, not read, and this is the case that makes the difference.
  //
  // `address_taken` tells somebody to sign in and link the provider from their
  // profile. Leaving the outcome behind would have that same message reappear
  // on the profile page they were just sent to, as a failure, beside the button
  // it asked them to press.
  it("spends the outcome it shows", async () => {
    resetSSOOutcome();
    window.history.replaceState(null, "", "/?sso_error=address_taken");

    render(<SignIn auth={null} onDone={() => undefined} />);
    expect(screen.getByText(/already uses that email address/i)).toBeInTheDocument();

    expect(consumeSSOOutcome()).toBe("");
  });

  // A refused round trip has nowhere to say so but the address bar, and the
  // sentence has to be the one the person needs: the account exists, and the
  // way to attach the provider to it is to sign in and link it.
  it("carries the reason a provider round trip was refused", () => {
    render(<SignIn
      auth={null} onDone={() => undefined}
      notice="An account here already uses that email address."
    />);

    expect(screen.getByText(/already uses that email address/i)).toBeInTheDocument();
  });
});

/**
 * Connecting a provider to an account that already uses the address.
 *
 * The refusal this replaces was correct and was a dead end: mcpd will not hand
 * an account over because an address matched, and the person meeting that
 * sentence was usually its owner. The password is the proof the provider
 * cannot give.
 */
describe("connecting a provider at sign-in", () => {
  beforeEach(() => {
    resetSSOOutcome();
    window.history.replaceState(null, "", "/");
    vi.restoreAllMocks();
  });

  const OFFER = { provider: "google" as const, label: "Google", email: "alice@example.com" };

  // The code in the address bar is a parameter somebody can type. A password
  // field drawn on the strength of one would be asking for a password against
  // nothing, so the screen asks the server first.
  it("draws the screen only once the server confirms an offer", async () => {
    const pending = vi.spyOn(api, "pendingLink").mockResolvedValue(OFFER);

    render(<SignIn auth={null} outcome="link_password" onDone={() => undefined} />);

    expect(await screen.findByRole("heading", { name: "Connect Google to your account" }))
      .toBeInTheDocument();
    expect(screen.getByText(/alice@example\.com/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Connect and sign in" })).toBeInTheDocument();
    expect(pending).toHaveBeenCalled();
  });

  // Nothing is waiting: the offer expired, it belongs to another browser, or
  // somebody typed the parameter. The sign-in form is the honest answer.
  it("falls back to the sign-in form when nothing is waiting", async () => {
    vi.spyOn(api, "pendingLink").mockRejectedValue(new Error("nothing here"));

    render(<SignIn auth={null} outcome="link_password" onDone={() => undefined} />);

    expect(await screen.findByRole("heading", { name: "Sign in" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Connect and sign in" })).toBeNull();
  });

  it("signs in once the password confirms the account", async () => {
    vi.spyOn(api, "pendingLink").mockResolvedValue(OFFER);
    const connect = vi.spyOn(api, "connectPendingLink").mockResolvedValue(SESSION);
    const onDone = vi.fn();

    render(<SignIn auth={null} outcome="link_password" onDone={onDone} />);
    await screen.findByRole("heading", { name: "Connect Google to your account" });

    await userEvent.type(screen.getByLabelText("Password"), "a-sufficiently-long-passphrase");
    await userEvent.click(screen.getByRole("button", { name: "Connect and sign in" }));

    expect(connect).toHaveBeenCalledWith("a-sufficiently-long-passphrase");
    expect(onDone).toHaveBeenCalledWith(SESSION);
  });

  // One sentence for a wrong password, and the screen stays: a typo must not
  // mean starting the whole round trip again.
  it("says a password did not match and stays on the screen", async () => {
    vi.spyOn(api, "pendingLink").mockResolvedValue(OFFER);
    vi.spyOn(api, "connectPendingLink").mockRejectedValue(new ApiError(401, "unauthorized", "that password did not match"));

    render(<SignIn auth={null} outcome="link_password" onDone={() => undefined} />);
    await screen.findByRole("heading", { name: "Connect Google to your account" });

    await userEvent.type(screen.getByLabelText("Password"), "not-the-password");
    await userEvent.click(screen.getByRole("button", { name: "Connect and sign in" }));

    expect(await screen.findByText("That password did not match.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Connect and sign in" })).toBeInTheDocument();
  });

  // The row is retired after three, and what the person needs at that point is
  // to start again rather than to keep typing into a screen backed by nothing.
  it("sends somebody back to the form once the offer has been retired", async () => {
    vi.spyOn(api, "pendingLink").mockResolvedValue(OFFER);
    vi.spyOn(api, "connectPendingLink").mockRejectedValue(new ApiError(404, "not_found", "there is nothing waiting to be connected"));

    render(<SignIn auth={null} outcome="link_password" onDone={() => undefined} />);
    await screen.findByRole("heading", { name: "Connect Google to your account" });

    await userEvent.type(screen.getByLabelText("Password"), "not-the-password");
    await userEvent.click(screen.getByRole("button", { name: "Connect and sign in" }));

    expect(await screen.findByRole("heading", { name: "Sign in" })).toBeInTheDocument();
    expect(screen.getByText("Too many attempts. Start the sign-in again."))
      .toBeInTheDocument();
  });

  // "Not now" retires the row rather than merely navigating away from it. An
  // offer left live is one the next person at that browser is holding, and the
  // screen it draws asks for a password.
  it("retires the offer when somebody says not now", async () => {
    vi.spyOn(api, "pendingLink").mockResolvedValue(OFFER);
    const discard = vi.spyOn(api, "discardPendingLink").mockResolvedValue(undefined);

    render(<SignIn auth={null} outcome="link_password" onDone={() => undefined} />);
    await screen.findByRole("heading", { name: "Connect Google to your account" });

    await userEvent.click(screen.getByRole("button", { name: "Not now" }));

    expect(discard).toHaveBeenCalled();
    expect(await screen.findByRole("heading", { name: "Sign in" })).toBeInTheDocument();
  });

  // Every other outcome is a refusal with a sentence, and none of them draws
  // this screen.
  it("is not drawn for an ordinary refusal", () => {
    const pending = vi.spyOn(api, "pendingLink").mockResolvedValue(OFFER);

    render(<SignIn auth={null} outcome="address_taken" onDone={() => undefined} />);

    expect(screen.getByRole("heading", { name: "Sign in" })).toBeInTheDocument();
    expect(pending).not.toHaveBeenCalled();
  });
});

/**
 * An account nobody has approved is signed in and holds nothing.
 *
 * The screen is not what enforces that -- the server refuses every call such
 * an account makes, which internal/admin tests against the API directly. This
 * is about what the person meets instead of a console whose every fetch comes
 * back refused.
 */
describe("an account waiting for approval", () => {
  beforeEach(() => {
    window.history.replaceState(null, "", "/");
    vi.restoreAllMocks();
  });

  it("says who it is signed in as and offers a way out", async () => {
    const onSignOut = vi.fn();
    render(<AwaitingApproval email="newcomer@example.com" onSignOut={onSignOut} />);

    expect(screen.getByText("Waiting for approval")).toBeInTheDocument();
    expect(screen.getByText("newcomer@example.com")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Sign out" }));
    expect(onSignOut).toHaveBeenCalled();
  });

  it("is the whole of the console the app draws for one", async () => {
    vi.spyOn(api, "meta").mockResolvedValue(META);
    vi.spyOn(api, "authOptions").mockResolvedValue({
      providers: [], registration: true,
    });
    vi.spyOn(api, "session").mockResolvedValue({ ...SESSION, status: "pending" });

    render(<App />);

    expect(await screen.findByText("Waiting for approval")).toBeInTheDocument();
    // Not one destination, and the account's role is not what decided that:
    // a pending principal holds nothing whatever its row says.
    expect(screen.queryByRole("link", { name: "Overview" })).toBeNull();
    expect(screen.queryByRole("link", { name: "Settings" })).toBeNull();
  });
});
