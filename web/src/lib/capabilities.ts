import type { Role } from "./api";

/** The capability strings `internal/auth` uses. */
export type Capability = "read" | "propose" | "approve" | "admin";

// Mirrors `roleCapabilities` in internal/auth/principal.go, and is the only
// place in the dashboard that knows what a role means. Advisory: the server
// authorises every call again.
const ROLE_CAPABILITIES: Record<Role, readonly Capability[]> = {
  user: ["read", "propose", "approve"],
  admin: ["read", "propose", "approve", "admin"],
};

/** The capabilities a role carries. Unknown roles carry none. */
export function capabilitiesOf(role: string): readonly Capability[] {
  return ROLE_CAPABILITIES[role as Role] ?? [];
}

/** Whether a role carries a capability. */
export function roleCan(role: string, capability: Capability): boolean {
  return capabilitiesOf(role).includes(capability);
}
