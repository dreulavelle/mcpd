import type { Capability, Role } from "./api";

export type { Capability };

// Mirrors `roleCapabilities` in internal/auth/principal.go, and is the only
// place in the dashboard that knows what a role means. Advisory: the server
// authorises every call again.
//
// This is what a *role* carries, which is not the same as what a signed-in
// person holds: a group can take capabilities away, and the session reports
// the result. Nothing that decides whether to draw a control should read
// this; it is for describing a role, and for a test fixture that needs a
// plausible session for one.
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
