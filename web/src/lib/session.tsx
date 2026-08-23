import { createContext, useContext, useMemo, type ReactNode } from "react";
import type { Session } from "./api";
import { capabilitiesOf, type Capability } from "./capabilities";

interface SessionValue {
  session: Session | null;
  /** Whether the signed-in principal carries a capability. */
  can: (capability: Capability) => boolean;
}

/**
 * Who is signed in, and what they may do.
 *
 * The default answers "nobody, and nothing". A component rendered outside the
 * provider therefore hides its controls rather than offering ones the API will
 * refuse -- which is the safe direction for a default to fail in.
 */
const SessionContext = createContext<SessionValue>({
  session: null,
  can: () => false,
});

export function SessionProvider({ session, children }: {
  session: Session | null;
  children: ReactNode;
}) {
  const value = useMemo<SessionValue>(() => {
    const held = new Set(session ? capabilitiesOf(session.role) : []);
    return { session, can: (c) => held.has(c) };
  }, [session]);
  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>;
}

/** The signed-in person, or null. */
export function useSession(): Session | null {
  return useContext(SessionContext).session;
}

/**
 * Asks whether the signed-in principal may do something.
 *
 * Deliberately the only question a component can ask. There is no `useRole`,
 * because every place that wanted one was really asking about a capability and
 * hard-coding the mapping on the way.
 */
export function useCan(capability: Capability): boolean {
  return useContext(SessionContext).can(capability);
}
