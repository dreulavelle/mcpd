import type { ReactElement, ReactNode } from "react";
import { render, type RenderResult } from "@testing-library/react";
import type { Role, Session } from "@/lib/api";
import { RouterProvider } from "@/lib/router";
import { SessionProvider } from "@/lib/session";
import { ToastProvider } from "@/components/toast";
import { TooltipProvider } from "@/components/ui/tooltip";

/** A signed-in person, with only the parts a test cares about spelled out. */
export function sessionFor(role: Role, overrides: Partial<Session> = {}): Session {
  return {
    email: `${role}@example.com`,
    display_name: role === "admin" ? "An Admin" : "A User",
    role,
    plugins: ["*"],
    csrf_token: "test-csrf",
    expires_at: new Date(Date.now() + 3_600_000).toISOString(),
    ...overrides,
  };
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
  { session = sessionFor("admin"), path = "/" }: {
    session?: Session | null;
    path?: string;
  } = {},
): RenderResult {
  window.history.replaceState(null, "", path);

  function Providers({ children }: { children: ReactNode }) {
    return (
      <SessionProvider session={session}>
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
