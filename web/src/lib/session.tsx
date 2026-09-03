import { createContext, useContext, useMemo, type ReactNode } from "react";
import type { Session } from "./api";
import type { Permission } from "./permissions";

interface SessionValue {
  session: Session | null;
  /** Whether the signed-in principal holds a permission. "" is anybody signed in. */
  can: (permission: Permission) => boolean;
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
    // The server's answer, never the role's: a group can add to a role, and
    // only the server knows by how much.
    const held = new Set<Permission>(session?.permissions ?? []);
    return {
      session,
      can: (p) => (p === "" ? session !== null : held.has(p)),
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
export function useCan(permission: Permission): boolean {
  return useContext(SessionContext).can(permission);
}

/**
 * The predicate itself, for a list that has to ask about many permissions
 * at once -- the sidebar, the palette. A hook per entry would break the
 * rules of hooks the moment the list changed length.
 */
export function useCanFn(): (permission: Permission) => boolean {
  return useContext(SessionContext).can;
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
