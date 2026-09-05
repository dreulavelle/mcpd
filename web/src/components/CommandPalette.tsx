import {
  useCallback, useEffect, useMemo, useRef, useState, type ReactNode,
} from "react";
import {
  Boxes, ClipboardCheck, CornerDownLeft, LogOut, Monitor, Moon, Search, Sun,
  UserRound, Waypoints, type LucideIcon,
} from "lucide-react";
import { Dialog as DialogPrimitive } from "radix-ui";
import { api, type Operation, type Plugin, type TunnelStatus } from "@/lib/api";
import { NAV } from "@/lib/nav";
import { useRouter } from "@/lib/router";
import { score } from "@/lib/search";
import { useCanFn } from "@/lib/session";
import { useTheme } from "@/lib/theme";
import { cn } from "@/lib/utils";
import { TABS } from "@/pages/settings/SettingsTabs";
import { healthTone } from "./status";
import { StatusDot } from "./status";

/** One thing the palette can do. */
interface Command {
  id: string;
  /** Which list it sits in. The order here is the order on screen. */
  group: "Go to" | "Plugins" | "Waiting on a decision" | "Connectors" | "Do";
  label: string;
  hint?: string;
  icon?: LucideIcon;
  /** Extra words a search may match: a plugin's title, an operation's plugin. */
  keywords?: string;
  mark?: ReactNode;
  run: () => void;
}

const GROUPS: Command["group"][] = ["Go to", "Plugins", "Waiting on a decision", "Connectors", "Do"];

/**
 * Everything in the console, from one box.
 *
 * Pages and settings tabs are known up front. Plugins, waiting changes and
 * connectors are fetched when the box opens and not before -- a palette that
 * polled would be a fourth copy of every list on every page -- and only the
 * ones this account may see, because a result it cannot open is worse than
 * no result. What a line offers is a place to go, never a decision: approving
 * from a search result is exactly the summary-level approval this product
 * exists to prevent.
 */
export function CommandPalette({ open, onOpenChange, onSignOut }: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onSignOut: () => void;
}) {
  const { navigate } = useRouter();
  const [, chooseTheme] = useTheme();
  const can = useCanFn();
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const [plugins, setPlugins] = useState<Plugin[]>([]);
  const [waiting, setWaiting] = useState<Operation[]>([]);
  const [tunnels, setTunnels] = useState<TunnelStatus[]>([]);
  const list = useRef<HTMLDivElement>(null);

  // Fresh each time it opens. The lists are small and a stale one is worse
  // than a fetch: a change decided a minute ago must not still be offered.
  useEffect(() => {
    if (!open) return;
    setQuery("");
    setActive(0);
    if (!can("")) return;
    api.plugins().then((r) => setPlugins(r.plugins ?? [])).catch(() => setPlugins([]));
    api.operations("pending_approval", 50)
      .then((r) => setWaiting(r.operations ?? [])).catch(() => setWaiting([]));
    api.tunnel().then((t) => setTunnels(t.tunnels ?? [])).catch(() => setTunnels([]));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  const go = useCallback((to: string) => {
    onOpenChange(false);
    navigate(to);
  }, [navigate, onOpenChange]);

  const commands = useMemo<Command[]>(() => {
    const out: Command[] = [];
    for (const group of NAV) {
      for (const item of group.items) {
        if (item.capability !== "signed-in" && !can(item.capability)) continue;
        out.push({
          id: `nav:${item.path}`, group: "Go to", label: item.label,
          hint: item.lede, icon: item.icon, keywords: group.title,
          run: () => go(item.path),
        });
      }
    }
    for (const tab of TABS) {
      if (tab.path === "/settings" || !can(tab.requires)) continue;
      out.push({
        id: `tab:${tab.path}`, group: "Go to", label: `Settings › ${tab.label}`,
        keywords: "settings", run: () => go(tab.path),
      });
    }
    for (const p of plugins) {
      out.push({
        id: `plugin:${p.name}`, group: "Plugins", label: p.name,
        hint: p.title !== p.name ? p.title : undefined,
        keywords: `${p.type} ${p.title} ${p.health}`, icon: Boxes,
        mark: <StatusDot tone={healthTone(p.health)} />,
        run: () => go(`/plugins/${encodeURIComponent(p.name)}`),
      });
    }
    for (const op of waiting) {
      out.push({
        id: `op:${op.id}`, group: "Waiting on a decision",
        label: op.action.replace(/[._]/g, " "),
        hint: `${op.plugin} · proposed by ${op.requested_by}`,
        keywords: `${op.plugin} ${op.requested_by} ${op.id} ${op.risk}`,
        icon: ClipboardCheck,
        run: () => go(`/approvals/${encodeURIComponent(op.id)}`),
      });
    }
    for (const t of tunnels) {
      if (!t.tunnel_id) continue;
      out.push({
        id: `tunnel:${t.tunnel_id}`, group: "Connectors",
        label: t.plugin ? `${t.plugin} connector` : "Connector for everything",
        hint: `${t.state}${t.principal ? ` · ${t.principal}` : ""}`,
        keywords: `${t.tunnel_id} ${t.state} ${t.principal ?? ""} tunnel`,
        icon: Waypoints,
        mark: <StatusDot tone={t.state === "connected" ? "good" : t.state === "failed" ? "problem" : "neutral"} />,
        run: () => go("/tunnels"),
      });
    }
    out.push(
      { id: "do:profile", group: "Do", label: "Your profile", icon: UserRound, keywords: "account", run: () => go("/profile") },
      { id: "do:light", group: "Do", label: "Appearance: light", icon: Sun, keywords: "theme", run: () => { chooseTheme("light"); onOpenChange(false); } },
      { id: "do:dark", group: "Do", label: "Appearance: dark", icon: Moon, keywords: "theme", run: () => { chooseTheme("dark"); onOpenChange(false); } },
      { id: "do:system", group: "Do", label: "Appearance: follow the system", icon: Monitor, keywords: "theme", run: () => { chooseTheme("system"); onOpenChange(false); } },
      { id: "do:signout", group: "Do", label: "Sign out", icon: LogOut, run: () => { onOpenChange(false); onSignOut(); } },
    );
    return out;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [plugins, waiting, tunnels, can, go]);

  const shown = useMemo(() => {
    const q = query.trim();
    const ranked = commands
      .map((c) => ({ c, s: Math.max(score(c.label, q), score(c.keywords ?? "", q) - 5) }))
      .filter(({ s }) => s > 0);
    // With nothing typed, the lists keep their own order; a query sorts by
    // how well each line matches, and within a score by group.
    if (q === "") return ranked.map(({ c }) => c);
    return ranked
      .sort((a, b) => b.s - a.s || GROUPS.indexOf(a.c.group) - GROUPS.indexOf(b.c.group))
      .map(({ c }) => c);
  }, [commands, query]);

  useEffect(() => setActive(0), [query]);

  // Keep the highlighted line in view as the arrows move it.
  useEffect(() => {
    const el = list.current?.querySelector<HTMLElement>(`[data-index="${active}"]`);
    el?.scrollIntoView({ block: "nearest" });
  }, [active]);

  function onKey(e: React.KeyboardEvent) {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setActive((i) => Math.min(i + 1, shown.length - 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setActive((i) => Math.max(i - 1, 0));
    } else if (e.key === "Enter") {
      e.preventDefault();
      shown[active]?.run();
    }
  }

  // Grouped only while nothing is typed. A query's answer is a ranking, and
  // headings inside a ranking would put the best match under the third one.
  const grouped = query.trim() === "";
  let index = -1;

  return (
    <DialogPrimitive.Root open={open} onOpenChange={onOpenChange}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="fixed inset-0 z-50 bg-black/40 data-[state=open]:animate-in data-[state=open]:fade-in-0" />
        <DialogPrimitive.Content
          aria-describedby={undefined}
          className="fixed top-[12vh] left-1/2 z-50 w-[min(40rem,calc(100vw-2rem))] -translate-x-1/2 overflow-hidden rounded-lg border bg-popover text-popover-foreground shadow-xl outline-none data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=open]:zoom-in-95"
          onKeyDown={onKey}
        >
          <DialogPrimitive.Title className="sr-only">Search the dashboard</DialogPrimitive.Title>
          <div className="flex items-center gap-2 border-b px-3">
            <Search className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
            <input
              autoFocus
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Go to a page, a plugin, a change waiting on you…"
              aria-label="Search the dashboard"
              aria-activedescendant={shown[active] ? `cmd-${shown[active].id}` : undefined}
              role="combobox"
              aria-expanded="true"
              aria-controls="command-results"
              aria-autocomplete="list"
              className="h-12 w-full bg-transparent text-sm outline-none placeholder:text-muted-foreground"
            />
            <kbd className="hidden rounded border px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground sm:inline">esc</kbd>
          </div>

          <div ref={list} id="command-results" role="listbox" className="max-h-[50vh] overflow-y-auto p-2">
            {shown.length === 0 && (
              <p className="px-2 py-6 text-center text-sm text-muted-foreground">
                Nothing matches. Try a page, a plugin's name, or a word from a change.
              </p>
            )}
            {GROUPS.map((group) => {
              const items = grouped ? shown.filter((c) => c.group === group) : group === GROUPS[0] ? shown : [];
              if (items.length === 0) return null;
              return (
                <div key={group} className="mb-1">
                  {grouped && (
                    <p className="px-2 pt-1 pb-1 text-[11px] font-semibold tracking-wider text-muted-foreground uppercase">
                      {group}
                    </p>
                  )}
                  {items.map((c) => {
                    index += 1;
                    const i = index;
                    const Icon = c.icon;
                    return (
                      <button
                        key={c.id}
                        id={`cmd-${c.id}`}
                        type="button"
                        role="option"
                        aria-selected={i === active}
                        data-index={i}
                        onMouseEnter={() => setActive(i)}
                        onClick={() => c.run()}
                        className={cn(
                          "flex w-full items-center gap-2.5 rounded-md px-2 py-1.5 text-left text-sm",
                          i === active ? "bg-accent text-accent-foreground" : "text-foreground",
                        )}
                      >
                        {c.mark ?? (Icon
                          ? <Icon className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
                          : <span className="size-4 shrink-0" />)}
                        <span className="min-w-0 flex-1">
                          <span className="block truncate">{c.label}</span>
                          {c.hint && (
                            <span className="block truncate text-xs text-muted-foreground">{c.hint}</span>
                          )}
                        </span>
                        {i === active && (
                          <CornerDownLeft className="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
                        )}
                      </button>
                    );
                  })}
                </div>
              );
            })}
          </div>
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  );
}
