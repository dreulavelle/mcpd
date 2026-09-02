import type { Capability, Group, GroupRef, Role } from "./api";
import { capabilitiesOf } from "./capabilities";

/**
 * What an account or a key may actually do, worked out the way the server
 * does: the role's own set, narrowed by the union of every ceiling its groups
 * declare. Groups declaring none are ignored rather than treated as
 * permitting everything -- the same rule `CeilingFor` applies, so that the
 * page and the server agree.
 *
 * `null` for the ceiling means no group narrows this subject, which the page
 * shows differently from "narrowed to everything the role has anyway".
 */
export function effectiveCapabilities(
  role: Role,
  memberOf: GroupRef[],
  groups: Group[],
): { held: Capability[]; ceiling: Capability[] | null; restrictedBy: string[] } {
  const byId = new Map(groups.map((g) => [g.id, g]));
  let ceiling: Set<Capability> | null = null;
  const restrictedBy: string[] = [];
  for (const ref of memberOf) {
    const g = byId.get(ref.id);
    if (!g || g.capabilities === null) continue;
    ceiling ??= new Set();
    for (const c of g.capabilities) ceiling.add(c);
    restrictedBy.push(g.name);
  }
  const own = capabilitiesOf(role);
  const held = ceiling === null ? [...own] : own.filter((c) => ceiling!.has(c));
  return { held, ceiling: ceiling === null ? null : [...ceiling], restrictedBy };
}

/** "Everything the role allows", or the short list, for a table cell. */
export function heldLabel(role: Role, held: Capability[]): string {
  const own = capabilitiesOf(role);
  if (held.length === own.length) return role === "admin" ? "Everything" : "Read, propose, approve";
  if (held.length === 0) return "Nothing";
  const words = held.join(", ");
  return words[0]!.toUpperCase() + words.slice(1);
}
