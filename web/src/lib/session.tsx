import { createContext, useContext, useMemo, type ReactNode } from "react";
import type { Session } from "./api";
import { capabilitiesOf, type Capability } from "./capabilities";

interface SessionValue {
  session: Session | null;
  /** Whether the signed-in principal carries a capability. */
  can: (capability: Capability) => boolean;
  /** Pushes a renamed session out, so the sidebar follows without a reload. */
  adopt: (session: Session) => void;
}

// Defaults to "nobody, and nothing", so a component outside the provider hides
// its controls rather than offering ones the API will refuse.
const SessionContext = createContext<SessionValue>({
  session: null,
  can: () => false,
  adopt: () => undefined,
});

export function SessionProvider({ session, onSession, children }: {
  session: Session | null;
  /** Where a changed session goes. Absent in a test that only reads one. */
  onSession?: (session: Session) => void;
  children: ReactNode;
}) {
  const value = useMemo<SessionValue>(() => {
    const held = new Set(session ? capabilitiesOf(session.role) : []);
    return {
      session,
      can: (c) => held.has(c),
      adopt: (next) => onSession?.(next),
    };
  }, [session, onSession]);
  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>;
}

/** The signed-in person, or null. */
export function useSession(): Session | null {
  return useContext(SessionContext).session;
}

/** Pushes a changed session back to whoever holds it. */
export function useAdoptSession(): (session: Session) => void {
  return useContext(SessionContext).adopt;
}

/** The only question a component may ask. There is deliberately no `useRole`. */
export function useCan(capability: Capability): boolean {
  return useContext(SessionContext).can(capability);
}

/**
 * What to call the signed-in person: always `name`, never `display_name`. The
 * raw column may be empty, and rendering it saves the fallback as a name.
 */
export function signedInAs(session: Session | null): string {
  return session?.name?.trim() || session?.email || "";
}

/** Whether the account has a name of its own rather than its address. */
export function hasOwnName(session: Session | null): boolean {
  return Boolean(session?.display_name?.trim());
}
