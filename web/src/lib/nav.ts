import {
  Boxes, ClipboardCheck, Cog, Gauge, KeyRound, ScrollText, ShieldCheck, Store,
  Ticket, UserRound, Users, UsersRound, Waypoints, type LucideIcon,
} from "lucide-react";
import type { Capability } from "./capabilities";

/**
 * What it takes to reach a destination. `"signed-in"` has to be typed out, so
 * an entry cannot become ungated by leaving the field off.
 */
export type Requirement = Capability | "signed-in";

/** One destination in the console. */
export interface NavItem {
  path: string;
  label: string;
  /** One line under the heading of the page it leads to. */
  lede: string;
  icon: LucideIcon;
  capability: Requirement;
  /** Whether the sidebar lists it. Defaults to true; a hidden entry is still routed. */
  inSidebar?: boolean;
}

export interface NavGroup {
  /** Absent on the first group, which needs no heading over a single entry. */
  title?: string;
  items: NavItem[];
}

/**
 * The console's sections, as data. The one place a route's capability is
 * decided; the server authorises again on every call regardless.
 */
export const NAV: NavGroup[] = [
  {
    items: [
      {
        path: "/",
        label: "Overview",
        lede: "What this host is doing, and what is waiting on somebody.",
        icon: Gauge,
        capability: "read",
      },
    ],
  },
  {
    title: "Govern",
    items: [
      {
        path: "/approvals",
        label: "Approvals",
        lede: "Changes an assistant has proposed, and what happened to them.",
        icon: ClipboardCheck,
        capability: "read",
      },
      {
        path: "/audit",
        label: "Audit",
        lede: "Append-only, and hash-chained. mcpd notices if anything is altered.",
        icon: ScrollText,
        capability: "read",
      },
    ],
  },
  {
    title: "Connect",
    items: [
      {
        path: "/plugins",
        label: "Plugins",
        lede: "Everything mcpd serves, built in or somebody else's, and how each one is doing.",
        icon: Boxes,
        capability: "read",
      },
      {
        path: "/marketplace",
        label: "Marketplace",
        lede: "Remote MCP servers you could add. Adding one makes it a plugin.",
        icon: Store,
        capability: "admin",
      },
      {
        path: "/tunnels",
        label: "Tunnels",
        lede: "One tunnel is one connector in ChatGPT.",
        icon: Waypoints,
        capability: "read",
      },
    ],
  },
  {
    // Flat, not a section that opens. Six destinations behind one word is a
    // menu that hides most of itself: nothing under Administer is reachable
    // until Settings is clicked, and the sidebar spends that space on nothing.
    // The paths are unchanged -- a label is not an address, and bookmarks and
    // links already point at these.
    title: "Administer",
    items: [
      {
        path: "/settings",
        label: "Settings",
        lede: "How this host is configured.",
        icon: Cog,
        capability: "read",
      },
      {
        // Read to see, admin to change, as with Settings above it.
        path: "/settings/policy",
        label: "Approval policy",
        lede: "Which changes can run without asking anyone.",
        icon: ShieldCheck,
        capability: "read",
      },
      {
        path: "/settings/authentication",
        label: "Authentication",
        lede: "How people sign in, and who is allowed to.",
        icon: KeyRound,
        capability: "admin",
      },
      {
        path: "/settings/users",
        label: "Users",
        lede: "Who can sign in, what they may do, and what they can reach.",
        icon: Users,
        capability: "admin",
      },
      {
        path: "/settings/groups",
        label: "Groups",
        lede: "A group hands its systems to everyone in it.",
        icon: UsersRound,
        capability: "admin",
      },
      {
        path: "/settings/keys",
        label: "API Keys",
        lede: "Credentials for scripts and agents. Each one acts as itself.",
        icon: Ticket,
        capability: "admin",
      },
    ],
  },
  {
    items: [
      {
        path: "/profile",
        label: "Profile",
        lede: "The account you are signed in as.",
        icon: UserRound,
        capability: "signed-in",
        // Reached by clicking your own name in the sidebar footer.
        inSidebar: false,
      },
    ],
  },
];

/** Paths that moved, and are redirected because somebody has them bookmarked. */
export function redirectFor(path: string): string | null {
  if (path === "/settings/account") return "/profile";

  // Segments stay encoded: "a%20b" has to leave as it arrived.
  const segments = path.split("/").filter(Boolean);
  if (segments.length === 2 && segments[0] === "marketplace") {
    return `/plugins/${segments[1]}`;
  }
  return null;
}

/**
 * The navigation a principal may see. An empty group disappears rather than
 * heading nothing.
 */
export function visibleNav(
  can: (capability: Capability) => boolean,
): NavGroup[] {
  const keep = (item: NavItem): boolean => {
    if (item.inSidebar === false) return false;
    return item.capability === "signed-in" || can(item.capability);
  };

  return NAV
    .map((group) => ({ ...group, items: group.items.filter(keep) }))
    .filter((group) => group.items.length > 0);
}

/**
 * A prefix match on segment boundaries, so /approvals covers /approvals/op-7
 * but not /approvalsomething. It decides the highlight and the capability both.
 */
export function covers(entryPath: string, path: string): boolean {
  if (entryPath === "/") return path === "/";
  return path === entryPath || path.startsWith(entryPath + "/");
}

/**
 * What a path requires. Longest match wins, so a child overrides its section.
 * Null means "nothing here to render", never "anyone may".
 */
export function capabilityFor(path: string): Requirement | null {
  return entryFor(path)?.capability ?? null;
}

/**
 * Which sidebar entry a path belongs to, or null for a path in no section.
 *
 * The longest match, not the first. /settings and /settings/users are siblings
 * now, and the first covers the second -- so an entry that merely covers the
 * path is not the entry the person is on, and highlighting every one that does
 * would light up half the menu.
 */
export function entryFor(path: string): NavItem | null {
  let matched: NavItem | null = null;

  for (const group of NAV) {
    for (const item of group.items) {
      if (covers(item.path, path) && item.path.length > (matched?.path.length ?? -1)) {
        matched = item;
      }
    }
  }
  return matched;
}
