import type { ReactElement, ReactNode } from "react";
import { render, type RenderResult } from "@testing-library/react";
import type { Role, Session } from "@/lib/api";
import { RouterProvider } from "@/lib/router";
import { SessionProvider } from "@/lib/session";
import { ToastProvider } from "@/components/toast";
import { TooltipProvider } from "@/components/ui/tooltip";

/**
 * A signed-in person, with only the parts a test cares about spelled out.
 *
 * `name` is resolved from `display_name` the way the server resolves it, so a
 * test that overrides one gets a session that could actually have come off the
 * wire. Spelling both out by hand is how a fixture ends up with a blank name
 * the real endpoint would never send.
 */
export function sessionFor(role: Role, overrides: Partial<Session> = {}): Session {
  const email = `${role}@example.com`;
  const displayName = role === "admin" ? "An Admin" : "A User";
  const base: Session = {
    email,
    name: displayName,
    display_name: displayName,
    role,
    plugins: ["*"],
    csrf_token: "test-csrf",
    expires_at: new Date(Date.now() + 3_600_000).toISOString(),
    status: "active",
    has_password: true,
  };
  const merged = { ...base, ...overrides };
  if (overrides.display_name !== undefined && overrides.name === undefined) {
    merged.name = overrides.display_name.trim() || merged.email;
  }
  return merged;
}

/**
 * Renders a component inside the providers the console always has.
 *
 * `session` is the whole point: almost everything worth testing here branches
 * on what the signed-in principal may do, and building the provider stack by
 * hand in each test is how one of them ends up rendering outside it and
 * quietly asserting the default.
 */
export function renderWith(
  ui: ReactElement,
  { session = sessionFor("admin"), path = "/", onSession }: {
    session?: Session | null;
    path?: string;
    /** Where a session the page changed goes. Only renaming yourself does. */
    onSession?: (session: Session) => void;
  } = {},
): RenderResult {
  window.history.replaceState(null, "", path);

  function Providers({ children }: { children: ReactNode }) {
    return (
      <SessionProvider session={session} onSession={onSession}>
        <TooltipProvider delayDuration={0}>
          <ToastProvider>
            <RouterProvider>{children}</RouterProvider>
          </ToastProvider>
        </TooltipProvider>
      </SessionProvider>
    );
  }

  return render(ui, { wrapper: Providers });
}
