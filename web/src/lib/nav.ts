import {
  Activity, Boxes, ChartColumn, ClipboardCheck, Cog, Gauge, ScrollText,
  ShieldCheck, Store, Terminal, UserRound, Waypoints,
  type LucideIcon,
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
        // The rules that decide the queue above, beside it. It sat under
        // Administer, which is where a setting lives rather than where a
        // decision does -- and reading "can this run without asking anyone"
        // as configuration is how a policy gets widened by somebody tidying.
        //
        // Read to see, admin to change. The path is unchanged: a label is not
        // an address, and links already point at this one.
        path: "/settings/policy",
        label: "Policies",
        lede: "Which changes can run without asking anyone.",
        icon: ShieldCheck,
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
        // Authentication, Users & Groups, API Keys, Certificates, ChatGPT and
        // Backup & Restore are tabs on this page rather than entries of their
        // own. Each is visited rarely -- a certificate when an upstream first
        // fails a handshake, a key when somebody writes a script, the
        // providers once, an account per workspace, an account or a group when
        // somebody joins -- and a sidebar that is read constantly should not
        // spend a permanent line on each.
        path: "/settings",
        label: "Settings",
        lede: "How this host is configured.",
        icon: Cog,
        capability: "read",
      },
      {
        path: "/system",
        label: "System",
        lede: "What this host is running, what it is using, and how to restart it.",
        icon: Activity,
        capability: "read",
      },
      {
        path: "/performance",
        label: "Performance",
        lede: "How long this host's tools take, and how much they send back.",
        icon: ChartColumn,
        capability: "read",
      },
      {
        // Not read: the log carries every request this host served, which
        // systems were called and by whom. That is a wider view than any one
        // account's own work.
        path: "/logs",
        label: "Logs",
        lede: "What this host is doing, as it does it.",
        icon: Terminal,
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
/**
 * What a route needs, for the routes that are not sidebar entries.
 *
 * These three became tabs on the Settings page, which took their entries out
 * of the sidebar -- and the gate read the requirement off that entry. Without
 * this they would fall back to the `/settings` entry that covers them, which
 * asks for `read`: three administrative pages served to anyone who could open
 * the console. A page's requirement is a property of the page, not of whether
 * it earned a line in the menu.
 */
const ROUTE_CAPABILITIES: Record<string, Requirement> = {
  "/settings/authentication": "admin",
  "/settings/certificates": "admin",
  "/settings/keys": "admin",
  // Who may sign in, and what a group hands everyone in it. These were two
  // sidebar entries beside Settings while also being one tab on it, so the
  // same page had two ways in that highlighted differently. The tab is the
  // way in; both paths stay routed because links and bookmarks point at them.
  "/settings/users": "admin",
  "/settings/groups": "admin",
  // A backup carries this host's database and the key that opens its secrets,
  // and a restore replaces both. Nothing on this page is less than admin.
  "/settings/backup": "admin",
  // An account carries a credential, an identity and a grant, so adding one
  // hands a whole ChatGPT workspace a way in. Administrator, for the same
  // reason users and groups are.
  "/settings/chatgpt": "admin",
  // Both are tabs rather than sidebar entries, so neither has an entry to
  // read a requirement from. Advanced changes how patient the listeners are
  // and what the database trades for speed; Diagnostics decides what leaves
  // this machine.
  "/settings/advanced": "admin",
  "/settings/diagnostics": "admin",
};

export function capabilityFor(path: string): Requirement | null {
  const own = ROUTE_CAPABILITIES[path];
  if (own !== undefined) return own;
  return entryFor(path)?.capability ?? null;
}

/**
 * Which sidebar entry a path belongs to, or null for a path in no section.
 *
 * The longest match, not the first. /settings covers every settings tab, and
 * /plugins covers a plugin's own page -- so an entry that merely covers the
 * path is not always the entry the person is on, and highlighting every one
 * that does would light up half the menu.
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
