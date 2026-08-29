import { Link, useRouter } from "@/lib/router";
import { useCan } from "@/lib/session";
import { cn } from "@/lib/utils";

/**
 * The one navigation on the settings pages.
 *
 * There were two: a row of tabs, and under it a row of filter chips over
 * thirty-one settings in a single column. Two navigations stacked is one too
 * many at the best of times, and these were worse than that -- the chips only
 * meant anything once you had already used the tabs, and both were called the
 * same thing by anybody describing the page out loud.
 *
 * So the chips are gone and the grouping they were doing moved into the tabs,
 * where a group declares its own section in Go. Each tab is a real route, so a
 * bookmark, a deep link and the back button work as they did when several of
 * these were sidebar entries.
 */
interface Tab {
  path: string;
  label: string;
  /** The schema section this tab renders, where it renders one. */
  section?: string;
  admin: boolean;
}

const TABS: Tab[] = [
  { path: "/settings", label: "General", section: "settings", admin: false },
  { path: "/settings/chatgpt", label: "ChatGPT", section: "chatgpt", admin: true },
  { path: "/settings/authentication", label: "Authentication", section: "authentication", admin: true },
  { path: "/settings/users", label: "Users & Groups", admin: true },
  { path: "/settings/keys", label: "API Keys", admin: true },
  { path: "/settings/certificates", label: "Certificates", admin: true },
  { path: "/settings/diagnostics", label: "Diagnostics", section: "diagnostics", admin: true },
  { path: "/settings/backup", label: "Backup & Restore", admin: true },
  { path: "/settings/advanced", label: "Advanced", section: "advanced", admin: true },
];

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

export function SettingsTabs() {
  const { path } = useRouter();
  const admin = useCan("admin");

  // An operator who cannot open a tab is not shown it, which is the rule the
  // sidebar applied to these when they were entries in it.
  const shown = TABS.filter((t) => !t.admin || admin);

  return (
    <nav aria-label="Settings sections" className="mb-4 border-b">
      <ul className="scroll-x -mb-px flex gap-1">
        {shown.map((t) => {
          // Exact, not prefix: /settings covers every path below it, and
          // marking it current on all of them would light up two tabs at once.
          const current = path === t.path;
          return (
            <li key={t.path}>
              <Link
                to={t.path}
                current={current}
                className={cn(
                  "inline-block whitespace-nowrap border-b-2 px-3 py-2 text-sm transition-colors",
                  current
                    ? "border-foreground font-medium text-foreground"
                    : "border-transparent text-muted-foreground hover:text-foreground",
                )}
              >
                {t.label}
              </Link>
            </li>
          );
        })}
      </ul>
    </nav>
  );
}
