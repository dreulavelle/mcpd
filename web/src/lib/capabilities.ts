import type { Role } from "./api";

/**
 * What a principal may do.
 *
 * These are the strings `internal/auth` uses. The host checks capabilities and
 * never roles, and so does this: a page that asked `role === "admin"` would be
 * asserting a mapping it does not own, and would answer wrongly the day a
 * third role exists.
 */
export type Capability = "read" | "propose" | "approve" | "admin";

/**
 * The role-to-capability mapping, mirroring `roleCapabilities` in
 * `internal/auth/principal.go`.
 *
 * This is the only place in the dashboard that knows what a role means. The
 * session endpoint sends a role rather than a capability list, so the mapping
 * has to exist somewhere on this side; keeping it to one table is what makes
 * it a thing to update rather than a thing to hunt for.
 *
 * It is advisory. Every call is authorised again on the server, so a mapping
 * that drifts too generous shows a control that then returns 403 -- annoying,
 * not unsafe.
 */
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
