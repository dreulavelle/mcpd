import { useCallback, useState, type ReactNode } from "react";
import { LogOut, Menu, X } from "lucide-react";
import { api, type HealthReport } from "@/lib/api";
import type { Capability } from "@/lib/capabilities";
import { usePoll } from "@/lib/hooks";
import { covers, visibleNav, type NavItem } from "@/lib/nav";
import { Link, useRouter } from "@/lib/router";
import { useCan, useSession } from "@/lib/session";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { healthTone, StatusDot } from "./status";

function NavLink({ item, badge, onNavigate }: {
  item: NavItem;
  badge?: number;
  onNavigate?: () => void;
}) {
  const { path } = useRouter();
  const current = covers(item.path, path);
  const Icon = item.icon;

  return (
    <Link
      to={item.path}
      onClick={onNavigate}
      current={current}
      className={cn(
        "flex items-center gap-2.5 rounded-md px-2.5 py-1.5 text-sm transition-colors",
        current
          ? "bg-accent font-medium text-accent-foreground"
          : "text-muted-foreground hover:bg-accent/60 hover:text-foreground",
      )}
    >
      <Icon className="size-4 shrink-0" aria-hidden="true" />
      <span className="flex-1 truncate">{item.label}</span>
      {badge !== undefined && badge > 0 && (
        <span className="rounded-full bg-attention-soft px-1.5 py-px text-[11px] font-medium text-attention tabular-nums">
          {badge}
        </span>
      )}
    </Link>
  );
}

/**
 * A child link matches exactly.
 *
 * Prefix matching would light "General" (/settings) on every page beneath it,
 * because every settings path begins with it.
 */
function ChildLink({ item, onNavigate }: { item: NavItem; onNavigate?: () => void }) {
  const { path } = useRouter();
  const current = path === item.path;
  return (
    <Link
      to={item.path}
      onClick={onNavigate}
      current={current}
      className={cn(
        "block rounded-md px-2.5 py-1 text-sm transition-colors",
        current ? "font-medium text-foreground" : "text-muted-foreground hover:text-foreground",
      )}
    >
      {item.label}
    </Link>
  );
}

function Sidebar({ badges, onNavigate }: {
  badges: Record<string, number>;
  onNavigate?: () => void;
}) {
  const { path } = useRouter();
  // Read the capabilities up front rather than from inside the filter: useCan
  // is a hook, and the rules of hooks do not allow calling one from a
  // predicate that may or may not run.
  const held: Record<Capability, boolean> = {
    read: useCan("read"),
    propose: useCan("propose"),
    approve: useCan("approve"),
    admin: useCan("admin"),
  };
  const groups = visibleNav((c) => held[c]);

  return (
    <nav className="flex h-full flex-col gap-5 overflow-y-auto p-3">
      {groups.map((group, i) => (
        <div key={group.title ?? `group-${i}`} className="space-y-1">
          {/* Not the faintest grey available. These headings were decoration
              over a tab row and are now the sidebar's spine -- the thing that
              says where Approvals sits relative to Plugins -- so they are read
              rather than glanced past, and have to be legible. */}
          {group.title && (
            <h2 className="px-2.5 pb-1 text-[11px] font-semibold tracking-wider text-muted-foreground uppercase">
              {group.title}
            </h2>
          )}
          {group.items.map((item) => (
            <div key={item.path}>
              <NavLink item={item} badge={badges[item.path]} onNavigate={onNavigate} />
              {/* Children appear only while their parent is open. A permanently
                  expanded tree makes the sidebar longer than the sections it
                  exists to make findable. */}
              {item.children && covers(item.path, path) && (
                <div className="mt-1 ml-4 space-y-1 border-l pl-2">
                  {item.children.map((child) => (
                    <ChildLink key={child.path} item={child} onNavigate={onNavigate} />
                  ))}
                </div>
              )}
            </div>
          ))}
        </div>
      ))}
    </nav>
  );
}

function HealthPill({ health }: { health: HealthReport | null }) {
  if (!health) return null;
  const tone = healthTone(health.status);
  const label = health.status === "up" ? "All good"
    : health.status === "down" ? "Problem" : "Degraded";
  const failing = (health.checks ?? []).filter((c) => c.status !== "up");

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <button
          type="button"
          className="flex items-center gap-1.5 rounded-md px-2 py-1 text-xs text-muted-foreground hover:bg-accent"
        >
          <StatusDot tone={tone} />
          {label}
        </button>
      </TooltipTrigger>
      <TooltipContent className="max-w-xs">
        {failing.length === 0
          ? `Every check passing (${health.checks?.length ?? 0}).`
          : failing.map((c) => (
            <div key={c.name}>
              {c.name}: {c.status}{c.message ? ` — ${c.message}` : ""}
            </div>
          ))}
      </TooltipContent>
    </Tooltip>
  );
}

export function Brand({ compact }: { compact?: boolean }) {
  return (
    <div className={cn("flex items-center gap-2", compact ? "" : "h-14 px-4")}>
      <span
        aria-hidden="true"
        className="grid size-6 place-items-center rounded-md bg-primary font-mono text-sm font-bold text-primary-foreground"
      >
        m
      </span>
      <span className="font-semibold tracking-tight">mcpd</span>
    </div>
  );
}

/**
 * The console's frame.
 *
 * A sidebar rather than a row of tabs, because the sections stopped fitting a
 * row the moment approvals, audit and the marketplace each had a page -- and
 * because a sidebar has room to say which of them is waiting on somebody
 * without abbreviating a label to make space for it.
 */
export function Shell({ badges, onSignOut, children }: {
  badges: Record<string, number>;
  onSignOut: () => void;
  children: ReactNode;
}) {
  const session = useSession();
  const [health, setHealth] = useState<HealthReport | null>(null);
  const [drawerOpen, setDrawerOpen] = useState(false);

  const pollHealth = useCallback(() => {
    api.health().then(setHealth).catch(() => setHealth(null));
  }, []);
  usePoll(pollHealth, 20_000);

  const closeDrawer = useCallback(() => setDrawerOpen(false), []);

  return (
    <div className="min-h-screen lg:grid lg:grid-cols-[15rem_1fr]">
      {/* A wide window keeps the sidebar. A narrow one gets it as a drawer over
          the page, because 15rem out of a phone's width is most of it. */}
      <aside className="sticky top-0 hidden h-screen border-r bg-card lg:flex lg:flex-col">
        <Brand />
        <Separator />
        <Sidebar badges={badges} />
      </aside>

      {drawerOpen && (
        <div className="fixed inset-0 z-40 lg:hidden">
          <button
            type="button"
            aria-label="Close navigation"
            className="absolute inset-0 bg-foreground/30"
            onClick={closeDrawer}
          />
          <aside className="relative flex h-full w-60 flex-col border-r bg-card">
            <Brand />
            <Separator />
            <Sidebar badges={badges} onNavigate={closeDrawer} />
          </aside>
        </div>
      )}

      <div className="flex min-w-0 flex-col">
        <header className="sticky top-0 z-30 flex h-14 items-center gap-2 border-b bg-background/85 px-4 backdrop-blur">
          <Button
            variant="ghost" size="icon-sm" className="lg:hidden"
            aria-label={drawerOpen ? "Close navigation" : "Open navigation"}
            onClick={() => setDrawerOpen((open) => !open)}
          >
            {drawerOpen
              ? <X className="size-4" aria-hidden="true" />
              : <Menu className="size-4" aria-hidden="true" />}
          </Button>
          <span className="lg:hidden"><Brand compact /></span>

          <span className="flex-1" />

          <HealthPill health={health} />
          <span className="hidden text-xs text-muted-foreground sm:inline">
            {session?.display_name || session?.email}
          </span>
          <Button variant="ghost" size="sm" onClick={onSignOut}>
            <LogOut className="size-3.5" aria-hidden="true" />
            <span className="hidden sm:inline">Sign out</span>
          </Button>
        </header>

        <main className="min-w-0 flex-1 px-4 py-6 lg:px-8">
          <div className="mx-auto max-w-6xl">{children}</div>
        </main>
      </div>
    </div>
  );
}
