import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, type Meta, type Session } from "@/lib/api";
import App from "@/App";
import { AwaitingApproval, SignIn } from "./SignedOut";

const META: Meta = { version: "dev", auth_mode: "static", needs_setup: false };

const SESSION: Session = {
  email: "newcomer@example.com",
  name: "newcomer@example.com",
  display_name: "",
  role: "user",
  plugins: ["*"],
  csrf_token: "test-csrf",
  expires_at: "2026-08-24T00:00:00Z",
  status: "active",
  has_password: true,
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
    render(<SignIn meta={META} auth={{ providers: [], registration: false, approval: true }}
      onDone={() => undefined} />);

    expect(screen.getByLabelText("Password")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Continue with/ })).toBeNull();
    expect(screen.queryByRole("button", { name: "Ask for one" })).toBeNull();
  });

  // Null is what a host that could not be asked looks like. Drawing the
  // password form is the honest fallback; guessing at buttons is not.
  it("shows only the password form when this host could not be asked", () => {
    render(<SignIn meta={META} auth={null} onDone={() => undefined} />);

    expect(screen.getByLabelText("Password")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Continue with/ })).toBeNull();
  });

  it("offers the providers this host has", () => {
    render(<SignIn
      meta={META}
      auth={{
        providers: [
          { provider: "google", label: "Google" },
          { provider: "github", label: "GitHub" },
        ],
        registration: false,
        approval: true,
      }}
      onDone={() => undefined}
    />);

    expect(screen.getByRole("button", { name: "Continue with Google" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Continue with GitHub" })).toBeInTheDocument();
  });

  // The form says what will happen before it is filled in rather than
  // afterwards, because "your account is waiting" is a surprise at the end and
  // an expectation at the start.
  it("says an account will wait when approval is on", async () => {
    render(<SignIn
      meta={META}
      auth={{ providers: [], registration: true, approval: true }}
      onDone={() => undefined}
    />);

    await userEvent.click(screen.getByRole("button", { name: "Ask for one" }));
    expect(screen.getByRole("heading", { name: "Ask for an account" }))
      .toBeInTheDocument();
    expect(screen.getByText(/administrator has to say yes/i)).toBeInTheDocument();
  });

  it("says an account will work straight away when it will", async () => {
    render(<SignIn
      meta={META}
      auth={{ providers: [], registration: true, approval: false }}
      onDone={() => undefined}
    />);

    await userEvent.click(screen.getByRole("button", { name: "Ask for one" }));
    expect(screen.getByText(/as soon as this is done/i)).toBeInTheDocument();
  });

  // A refused round trip has nowhere to say so but the address bar, and the
  // sentence has to be the one the person needs: the account exists, and the
  // way to attach the provider to it is to sign in and link it.
  it("carries the reason a provider round trip was refused", () => {
    render(<SignIn
      meta={META} auth={null} onDone={() => undefined}
      notice="An account here already uses that email address."
    />);

    expect(screen.getByText(/already uses that email address/i)).toBeInTheDocument();
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
    render(<AwaitingApproval meta={META} email="newcomer@example.com" onSignOut={onSignOut} />);

    expect(screen.getByText("Waiting for approval")).toBeInTheDocument();
    expect(screen.getByText("newcomer@example.com")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Sign out" }));
    expect(onSignOut).toHaveBeenCalled();
  });

  it("is the whole of the console the app draws for one", async () => {
    vi.spyOn(api, "meta").mockResolvedValue(META);
    vi.spyOn(api, "authOptions").mockResolvedValue({
      providers: [], registration: true, approval: true,
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
