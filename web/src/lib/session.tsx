import { createContext, useContext, useMemo, type ReactNode } from "react";
import type { Session } from "./api";
import { capabilitiesOf, type Capability } from "./capabilities";

interface SessionValue {
  session: Session | null;
  /** Whether the signed-in principal carries a capability. */
  can: (capability: Capability) => boolean;
  /**
   * Replaces what the console holds about the signed-in person.
   *
   * One thing about a session can now change without a new one being issued:
   * its holder can rename themselves. The sidebar reads the name from here, so
   * without a way to push the new value the page would have to tell somebody
   * to reload in order to see their own edit.
   */
  adopt: (session: Session) => void;
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

/**
 * What to call the signed-in person, wherever the console names them.
 *
 * `name` and not `display_name`. The server resolves one from the other and
 * re-checks it on the way out, so `name` is never empty and never a value the
 * current rules would refuse; `display_name` is the raw column and belongs
 * only in the field somebody edits. Rendering the raw one is how a fallback
 * ends up in the box a person then saves, writing the address into the name.
 *
 * The old fallback stays as the last resort, for a session shape that predates
 * the field rather than for an empty one.
 */
export function signedInAs(session: Session | null): string {
  return session?.name?.trim() || session?.email || "";
}

/**
 * Whether this account has a name of its own, as opposed to being called by
 * its address.
 *
 * The one question `name` alone cannot answer, and greeting somebody by their
 * email address is worse than not greeting them.
 */
export function hasOwnName(session: Session | null): boolean {
  return Boolean(session?.display_name?.trim());
}
