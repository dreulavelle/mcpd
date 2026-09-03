/**
 * The permission vocabulary, mirrored from internal/auth/permissions.go.
 *
 * A permission is an area at a level, written "area:level". Write includes
 * read, decide includes read. This file describes the vocabulary -- the
 * areas, the levels each can be held at, and what the built-in roles carry --
 * so the matrix editor and the test fixtures have something to draw from.
 * Nothing that decides whether to draw a control reads the built-ins here:
 * the session reports `permissions`, computed by the server, and `useCan`
 * reads that.
 */

export type Area =
  | "approvals" | "policies" | "plugins" | "tunnels"
  | "settings" | "access" | "history" | "system";

export type Level = "none" | "read" | "write" | "decide";

/** "area:level", or "" for anybody signed in. */
export type Permission = string;

/** Every area, in the order the matrix shows them. */
export const AREAS: Area[] = [
  "approvals", "policies", "plugins", "tunnels", "settings", "access", "history", "system",
];

export const AREA_LABELS: Record<Area, string> = {
  approvals: "Approvals",
  policies: "Policies",
  plugins: "Plugins",
  tunnels: "Tunnels & ChatGPT",
  settings: "Settings",
  access: "Access",
  history: "History",
  system: "System",
};

/** What each area covers, for the matrix's row hints. */
export const AREA_HINTS: Record<Area, string> = {
  approvals: "The queue of proposed changes; deciding on them",
  policies: "Standing rules and the bypass window",
  plugins: "Instances, remote servers, the marketplace, certificates",
  tunnels: "Tunnels and the ChatGPT accounts they connect with",
  settings: "This host's own configuration",
  access: "Users, groups, roles, keys, registrations, sign-in providers",
  history: "Activity, audit, logs, performance; write is clearing",
  system: "What the host is running; restart, backup and restore",
};

export const LEVEL_LABELS: Record<Level, string> = {
  none: "None",
  read: "Read",
  write: "Write",
  decide: "Decide",
};

/** The levels an area can be held at, lowest first. */
export function levelsOf(area: Area): Level[] {
  return area === "approvals" ? ["read", "decide"] : ["read", "write"];
}

function rank(level: Level | undefined): number {
  switch (level) {
    case "read": return 1;
    case "write":
    case "decide": return 2;
    default: return 0;
  }
}

/** Whether holding `held` satisfies a requirement of `need`. */
export function includes(held: Level | undefined, need: Level): boolean {
  if (need === "none") return true;
  return rank(held) >= rank(need) && rank(need) > 0;
}

/** A role's permissions: the highest level held in each area. */
export type PermissionSet = Partial<Record<Area, Level>>;

/** Expands a set into every "area:level" it satisfies, in display order. */
export function expand(set: PermissionSet): Permission[] {
  const out: Permission[] = [];
  for (const area of AREAS) {
    for (const level of levelsOf(area)) {
      if (includes(set[area], level)) out.push(`${area}:${level}`);
    }
  }
  return out;
}

/** The higher level in every area. */
export function merge(a: PermissionSet, b: PermissionSet): PermissionSet {
  const out: PermissionSet = { ...a };
  for (const area of AREAS) {
    if (rank(b[area]) > rank(out[area])) out[area] = b[area];
  }
  return out;
}

/**
 * What the three built-in roles carry, as internal/auth/roles.go defines
 * them. For test fixtures and for describing a role; the server's answer is
 * what decides.
 */
export const BUILTIN_ROLES: Record<string, { name: string; permissions: PermissionSet }> = {
  role_reader: {
    name: "Reader",
    permissions: {
      approvals: "read", policies: "read", plugins: "read", tunnels: "read",
      settings: "read", history: "read", system: "read",
    },
  },
  role_operator: {
    name: "Operator",
    permissions: {
      approvals: "decide", policies: "read", plugins: "read", tunnels: "read",
      settings: "read", history: "read", system: "read",
    },
  },
  role_administrator: {
    name: "Administrator",
    permissions: {
      approvals: "decide", policies: "write", plugins: "write", tunnels: "write",
      settings: "write", access: "write", history: "write", system: "write",
    },
  },
};

/** The permissions a built-in role carries, expanded. Unknown ids carry none. */
export function builtinPermissions(roleId: string): Permission[] {
  const role = BUILTIN_ROLES[roleId];
  return role ? expand(role.permissions) : [];
}

/**
 * A short sentence for a permission set: "Everything", "Reads everything",
 * "Decides approvals; writes tunnels; reads the rest", or "Nothing".
 */
export function describe(set: PermissionSet): string {
  const held = AREAS.filter((a) => rank(set[a]) > 0);
  if (held.length === 0) return "Nothing";
  const top = AREAS.filter((a) => set[a] === levelsOf(a)[1]);
  const readOnly = AREAS.filter((a) => set[a] === "read");
  if (top.length === AREAS.length) return "Everything";
  if (top.length === 0) {
    return readOnly.length === AREAS.length - 1 && !set.access
      ? "Reads everything"
      : `Reads ${readOnly.map((a) => AREA_LABELS[a].toLowerCase()).join(", ")}`;
  }
  const parts = top.map((a) => `${a === "approvals" ? "decides" : "writes"} ${AREA_LABELS[a].toLowerCase()}`);
  if (readOnly.length > 0) {
    parts.push(readOnly.length >= 3 ? "reads the rest" : `reads ${readOnly.map((a) => AREA_LABELS[a].toLowerCase()).join(", ")}`);
  }
  const text = parts.join("; ");
  return text.charAt(0).toUpperCase() + text.slice(1);
}

/** Turns a list of "area:level" back into a set, for describing a subject. */
export function collect(list: Permission[]): PermissionSet {
  const out: PermissionSet = {};
  for (const p of list) {
    const [area, level] = p.split(":") as [Area, Level];
    if (!AREAS.includes(area)) continue;
    if (rank(level) > rank(out[area])) out[area] = level;
  }
  return out;
}
