import {
  Boxes, ClipboardCheck, Cog, Gauge, ScrollText, Store, UserRound, Users,
  Waypoints, type LucideIcon,
} from "lucide-react";
import type { Capability } from "./capabilities";

/**
 * What it takes to reach a destination.
 *
 * Almost always a capability. `"signed-in"` is the deliberate absence of one:
 * your own profile is not an administrative surface, and gating it on `read`
 * would be reflex rather than a rule. It is a value you have to type rather
 * than a field you can leave out, so an entry cannot become ungated by
 * omission -- which is the failure the declarative map exists to prevent.
 */
export type Requirement = Capability | "signed-in";

/**
 * One destination in the console.
 *
 * `capability` is what it takes to see the entry at all. Actions inside a
 * section are gated separately and usually more tightly -- Approvals is
 * readable by anyone who can read, and its buttons need approve or propose.
 */
export interface NavItem {
  path: string;
  label: string;
  /** One line under the heading of the page it leads to. */
  lede: string;
  icon: LucideIcon;
  capability: Requirement;
  /**
   * Whether the sidebar lists it. Defaults to true.
   *
   * False for a destination reached some other way -- the profile hangs off
   * the identity in the sidebar footer. It is still a path the router has to
   * judge, so it belongs in this map rather than in a second one beside it.
   */
  inSidebar?: boolean;
  children?: NavItem[];
}

export interface NavGroup {
  /** Absent on the first group, which needs no heading over a single entry. */
  title?: string;
  items: NavItem[];
}

/**
 * The console's sections, as data.
 *
 * Declarative because the alternative does not survive a second exception. The
 * console this replaces spliced its one admin-only entry into the array inline
 * -- `...(admin ? [["users", "Users"]] : [])` -- which reads as a list with a
 * hole in it, cannot be tested without rendering the whole page, and gets
 * copied the moment a second entry needs the same treatment.
 *
 * Nothing here decides anything. `visibleNav` filters, the sidebar renders, and
 * the server authorises again on every call regardless.
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
        // Discovery, not management. What is already installed is a plugin and
        // is managed on its plugin page; this is where a new one is found.
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
    title: "Administer",
    items: [
      {
        // How the *host* is configured. How *you* are configured is /profile,
        // which is why Account is no longer a child here.
        path: "/settings",
        label: "Settings",
        lede: "How this host is configured.",
        icon: Cog,
        capability: "read",
        children: [
          {
            path: "/settings",
            label: "General",
            lede: "How this host is configured.",
            icon: Cog,
            capability: "read",
          },
          {
            path: "/settings/users",
            label: "Users",
            lede: "Who can sign in, what they may do, and what they can reach.",
            icon: Users,
            capability: "admin",
          },
        ],
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
        // Reached by clicking your own name in the sidebar footer, which is
        // where people look for it. A nav entry as well would be the same
        // destination listed twice.
        inSidebar: false,
      },
    ],
  },
];

/**
 * Paths that used to mean something else.
 *
 * Both of these moved for the same reason: a thing was being managed somewhere
 * that was not where it lived. An installed remote server was managed under
 * /marketplace though it is a plugin, and your own account was managed under
 * /settings though settings are the host's rather than yours. Somebody has
 * both addresses bookmarked, so they redirect rather than 404.
 *
 * A table rather than a branch inside the router: where a path goes is the
 * same kind of fact as what capability it needs, and both are answered here.
 */
export function redirectFor(path: string): string | null {
  if (path === "/settings/account") return "/profile";

  // Segments are left encoded. A server named "a b" arrived as "a%20b" and has
  // to leave the same way; decoding and re-encoding is a round trip with
  // nothing to gain and an escaping bug to lose.
  const segments = path.split("/").filter(Boolean);
  if (segments.length === 2 && segments[0] === "marketplace") {
    return `/plugins/${segments[1]}`;
  }
  return null;
}

/**
 * The navigation a principal may see.
 *
 * A parent whose children are all hidden is hidden with them; a parent that
 * survives keeps only the children that did. An empty group disappears rather
 * than leaving a heading over nothing.
 */
export function visibleNav(
  can: (capability: Capability) => boolean,
): NavGroup[] {
  const keep = (item: NavItem): NavItem | null => {
    if (item.inSidebar === false) return null;
    if (item.capability !== "signed-in" && !can(item.capability)) return null;
    if (!item.children) return item;
    const children = item.children.map(keep).filter((c): c is NavItem => c !== null);
    if (children.length === 0) return null;
    return { ...item, children };
  };

  return NAV
    .map((group) => ({
      ...group,
      items: group.items.map(keep).filter((i): i is NavItem => i !== null),
    }))
    .filter((group) => group.items.length > 0);
}

/**
 * Whether a nav entry's path covers the path being looked at.
 *
 * A prefix match on segment boundaries, so /approvals covers /approvals/op-7
 * but not /approvalsomething. The root is matched exactly, because every path
 * begins with it.
 *
 * Lives here rather than in the sidebar because it is a routing fact rather
 * than a highlighting one: the same rule decides which entry lights up and
 * which capability a URL is judged against, and two copies of it would
 * eventually disagree about a detail page.
 */
export function covers(entryPath: string, path: string): boolean {
  if (entryPath === "/") return path === "/";
  return path === entryPath || path.startsWith(entryPath + "/");
}

/**
 * What a path requires.
 *
 * The single answer to "may this be rendered", derived from the same map the
 * sidebar is built from. `Routes` used to carry its own table of the same
 * facts, spelled out per case. They agreed, and nothing made them: a section
 * added to one and not the other is either invisible or ungated, and only one
 * of those failures announces itself.
 *
 * Longest match wins, so a child overrides the section it sits in --
 * /settings/users needs admin though /settings needs only read. An unknown
 * path returns null, which does not mean "allowed": it means there is nothing
 * here to render. "signed-in" is the one answer that means allowed without a
 * capability, and it has to be written into the map to be given.
 */
export function capabilityFor(path: string): Requirement | null {
  let matched: Requirement | null = null;
  let matchedLength = -1;

  const consider = (item: NavItem) => {
    if (covers(item.path, path) && item.path.length > matchedLength) {
      matched = item.capability;
      matchedLength = item.path.length;
    }
    for (const child of item.children ?? []) consider(child);
  };

  for (const group of NAV) for (const item of group.items) consider(item);
  return matched;
}
