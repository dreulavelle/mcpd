import { useCallback, useState, type ReactNode } from "react";
import { LogOut, Menu, X } from "lucide-react";
import type { Capability } from "@/lib/capabilities";
import { entryFor, visibleNav, type NavItem } from "@/lib/nav";
import { Link, useRouter } from "@/lib/router";
import { signedInAs, useCan, useSession } from "@/lib/session";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";

function NavLink({ item, badge, onNavigate }: {
  item: NavItem;
  badge?: number;
  onNavigate?: () => void;
}) {
  const { path } = useRouter();
  // The entry this path belongs to, not merely one that covers it. /settings
  // covers /settings/users, and both are in the sidebar.
  const current = entryFor(path)?.path === item.path;
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

function Sidebar({ badges, onNavigate }: {
  badges: Record<string, number>;
  onNavigate?: () => void;
}) {
  // Up front, not inside the filter: `useCan` is a hook and the predicate may
  // not run.
  const held: Record<Capability, boolean> = {
    read: useCan("read"),
    propose: useCan("propose"),
    approve: useCan("approve"),
    admin: useCan("admin"),
  };
  const groups = visibleNav((c) => held[c]);

  // `min-h-0` is load-bearing: without it a flex child refuses to shrink below
  // its content and `overflow-y-auto` never engages.
  return (
    <nav className="flex min-h-0 flex-1 flex-col gap-5 overflow-y-auto p-3">
      {groups.map((group, i) => (
        <div key={group.title ?? `group-${i}`} className="space-y-1">
          {/* Navigation rather than decoration, so it has to clear contrast. */}
          {group.title && (
            <h2 className="px-2.5 pb-1 text-[11px] font-semibold tracking-wider text-muted-foreground uppercase">
              {group.title}
            </h2>
          )}
          {group.items.map((item) => (
            <NavLink
              key={item.path} item={item}
              badge={badges[item.path]} onNavigate={onNavigate}
            />
          ))}
        </div>
      ))}
    </nav>
  );
}

/**
 * Who is signed in, and the way out. Sign-out is a control of its own beside
 * the profile link, not nested inside it: a misclick should not end the session.
 */
function SidebarFooter({ onSignOut, version, onNavigate }: {
  onSignOut: () => void;
  version: string;
  onNavigate?: () => void;
}) {
  const session = useSession();
  const { path } = useRouter();
  const current = path === "/profile";

  return (
    <div className="shrink-0 border-t p-3">
      <div className="flex items-center gap-1">
        {/* The address as the title, because the visible line may be a name. */}
        <Link
          to="/profile"
          onClick={onNavigate}
          current={current}
          className={cn(
            "min-w-0 flex-1 truncate rounded-md px-2 py-1 text-sm transition-colors",
            current
              ? "bg-accent font-medium text-accent-foreground"
              : "hover:bg-accent/60",
          )}
        >
          <span title={session?.email}>{signedInAs(session)}</span>
        </Link>
        <Button
          variant="ghost" size="icon-sm"
          aria-label="Sign out" onClick={onSignOut}
        >
          <LogOut className="size-4" aria-hidden="true" />
        </Button>
      </div>
      {/* What is actually running, where somebody looks when they are about to
          report something. Quiet on purpose: a fact to find, not one to read
          every time the page opens. Not a link -- the System page it would
          lead to is already an entry in this same sidebar. */}
      <p className="px-2.5 pt-2 font-mono text-[11px] text-muted-foreground">
        mcpd {version}
      </p>
    </div>
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

/** The console's frame. */
export function Shell({ badges, onSignOut, version, children }: {
  badges: Record<string, number>;
  onSignOut: () => void;
  /** What this host is running, shown under the account. */
  version: string;
  children: ReactNode;
}) {
  const [drawerOpen, setDrawerOpen] = useState(false);
  const closeDrawer = useCallback(() => setDrawerOpen(false), []);

  return (
    <div className="min-h-screen lg:grid lg:grid-cols-[15rem_1fr]">
      {/* A drawer on a narrow window: 15rem of a phone's width is most of it. */}
      <aside className="sticky top-0 hidden h-screen border-r bg-card lg:flex lg:flex-col">
        <Brand />
        <Separator />
        <Sidebar badges={badges} />
        <SidebarFooter onSignOut={onSignOut} version={version} />
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
            <SidebarFooter onSignOut={onSignOut} version={version} onNavigate={closeDrawer} />
          </aside>
        </div>
      )}

      <div className="flex min-w-0 flex-col">
        {/* Narrow windows only, where a collapsed sidebar still needs a handle. */}
        <header className="sticky top-0 z-30 flex h-14 items-center gap-2 border-b bg-background/85 px-4 backdrop-blur lg:hidden">
          <Button
            variant="ghost" size="icon-sm"
            aria-label={drawerOpen ? "Close navigation" : "Open navigation"}
            onClick={() => setDrawerOpen((open) => !open)}
          >
            {drawerOpen
              ? <X className="size-4" aria-hidden="true" />
              : <Menu className="size-4" aria-hidden="true" />}
          </Button>
          <Brand compact />

          <span className="flex-1" />

          <Button
            variant="ghost" size="icon-sm"
            aria-label="Sign out" onClick={onSignOut}
          >
            <LogOut className="size-4" aria-hidden="true" />
          </Button>
        </header>

        <main className="min-w-0 flex-1 px-4 py-6 lg:px-8 lg:py-8">
          <div className="mx-auto max-w-6xl">{children}</div>
        </main>
      </div>
    </div>
  );
}
