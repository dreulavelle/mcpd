import type { ReactNode } from "react";
import { Link, useRouter } from "@/lib/router";
import { useCanFn } from "@/lib/session";
import type { Permission } from "@/lib/permissions";
import { NativeSelect } from "@/components/ui/native-select";
import { cn } from "@/lib/utils";

/**
 * The one navigation on the settings pages.
 *
 * A rail down the left on a wide screen, grouped by what the pages are
 * about -- this host, what connects to it, who may use it -- and a single
 * select on a narrow one. Each entry is a real route, so a bookmark, a deep
 * link and the back button work as they did when several of these were
 * sidebar entries.
 *
 * It used to be a strip of ten tabs across the top, which is nine more than
 * a person can hold in view and no grouping at all: ChatGPT sat beside
 * Backup, and Roles beside Certificates, for no reason either page knew.
 */
export interface Tab {
  path: string;
  label: string;
  /** The schema section this tab renders, where it renders one. */
  section?: string;
  /** What it takes to open the tab. Mirrors ROUTE_CAPABILITIES in nav.ts. */
  requires: Permission;
}

export interface TabGroup {
  title: string;
  tabs: Tab[];
}

export const SETTINGS_GROUPS: TabGroup[] = [
  {
    title: "This host",
    tabs: [
      { path: "/settings", label: "General", section: "settings", requires: "settings:read" },
      { path: "/settings/diagnostics", label: "Diagnostics", section: "diagnostics", requires: "settings:write" },
      { path: "/settings/advanced", label: "Advanced", section: "advanced", requires: "settings:write" },
      { path: "/settings/backup", label: "Backup & Restore", requires: "system:write" },
    ],
  },
  {
    title: "Connections",
    tabs: [
      { path: "/settings/chatgpt", label: "ChatGPT", section: "chatgpt", requires: "tunnels:write" },
      { path: "/settings/certificates", label: "Certificates", requires: "plugins:write" },
    ],
  },
  {
    title: "Access",
    tabs: [
      { path: "/settings/users", label: "Users & Groups", requires: "access:read" },
      { path: "/settings/roles", label: "Roles", requires: "access:read" },
      { path: "/settings/keys", label: "API Keys", requires: "access:read" },
      { path: "/settings/authentication", label: "Sign-in", section: "authentication", requires: "access:write" },
    ],
  },
];

/** Every tab, flat, for the palette and the router. */
export const TABS: Tab[] = SETTINGS_GROUPS.flatMap((g) => g.tabs);

/**
 * Where a section's settings live, in words, for a search result found on
 * another tab.
 *
 * The approval settings are the one that is not a tab here: they sit with the
 * rules they time, under Govern.
 */
export function tabForSection(section: string): string {
  if (section === "approvals") return "On Policies, under Govern";
  const tab = TABS.find((t) => t.section === section);
  return tab ? `On the ${tab.label} tab` : "In Settings";
}

/** Whether a path is one of the settings pages this rail belongs to. */
export function isSettingsTab(path: string): boolean {
  return TABS.some((t) => t.path === path) || path === "/settings/groups";
}

/**
 * The rail and the page beside it. Wraps every settings page from the
 * router, so a page renders only what is its own.
 */
export function SettingsLayout({ children }: { children: ReactNode }) {
  return (
    <div className="flex flex-col gap-6 lg:flex-row lg:gap-12">
      <SettingsNav />
      <div className="min-w-0 flex-1">{children}</div>
    </div>
  );
}

function SettingsNav() {
  const { path, navigate } = useRouter();
  const can = useCanFn();
  // An operator who cannot open a tab is not shown it, which is the rule the
  // sidebar applied to these when they were entries in it.
  const groups = SETTINGS_GROUPS
    .map((g) => ({ ...g, tabs: g.tabs.filter((t) => can(t.requires)) }))
    .filter((g) => g.tabs.length > 0);
  // /settings/groups is a way into Users & Groups that links still use.
  const current = path === "/settings/groups" ? "/settings/users" : path;

  return (
    <nav aria-label="Settings sections" className="lg:w-48 lg:shrink-0">
      {/* One select on a phone: the rail would push the page below the fold. */}
      <div className="lg:hidden">
        <NativeSelect
          aria-label="Settings section"
          value={current}
          onChange={(e) => navigate(e.target.value)}
        >
          {groups.map((g) => (
            <optgroup key={g.title} label={g.title}>
              {g.tabs.map((t) => <option key={t.path} value={t.path}>{t.label}</option>)}
            </optgroup>
          ))}
        </NativeSelect>
      </div>
      <div className="hidden space-y-5 lg:block lg:sticky lg:top-6">
        {groups.map((g) => (
          <div key={g.title}>
            <h2 className="mb-1.5 px-2 text-[11px] font-semibold tracking-wider text-muted-foreground uppercase">
              {g.title}
            </h2>
            <ul className="space-y-0.5">
              {g.tabs.map((t) => {
                const on = current === t.path;
                return (
                  <li key={t.path}>
                    <Link
                      to={t.path}
                      current={on}
                      className={cn(
                        "block rounded-md px-2 py-1.5 text-sm transition-colors",
                        on
                          ? "bg-accent font-medium text-accent-foreground"
                          : "text-muted-foreground hover:bg-accent/60 hover:text-foreground",
                      )}
                    >
                      {t.label}
                    </Link>
                  </li>
                );
              })}
            </ul>
          </div>
        ))}
      </div>
    </nav>
  );
}

/**
 * Kept as a no-op for the pages that still call it: the rail is drawn by
 * the router now, so a page rendering it twice would draw two.
 */
export function SettingsTabs() {
  return null;
}
