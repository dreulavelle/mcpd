import {
  useCallback, useEffect, useMemo, useRef, useState, type ReactNode,
} from "react";
import {
  Boxes, ClipboardCheck, CornerDownLeft, LogOut, Monitor, Moon, Search, Sun,
  UserRound, Waypoints, type LucideIcon,
} from "lucide-react";
import { Dialog as DialogPrimitive } from "radix-ui";
import { api, type Operation, type Plugin, type TunnelStatus } from "@/lib/api";
import { describeChange, principalWords, riskLabel } from "@/lib/format";
import { NAV } from "@/lib/nav";
import { useRouter } from "@/lib/router";
import { score } from "@/lib/search";
import { useCanFn } from "@/lib/session";
import { useTheme } from "@/lib/theme";
import { cn } from "@/lib/utils";
import { TABS } from "@/pages/settings/SettingsTabs";
import { Chip, healthTone, riskTone, StatusDot } from "./status";

/** One thing the palette can do. */
interface Command {
  id: string;
  /** Which list it sits in. The order here is the order on screen. */
  group: "Go to" | "Plugins" | "Waiting on you" | "Connectors" | "Do";
  label: string;
  hint?: string;
  icon?: LucideIcon;
  /** Extra words a search may match: a plugin's title, an operation's plugin. */
  keywords?: string;
  mark?: ReactNode;
  /** State the row is worth reading before it is opened: a change's risk. */
  chip?: ReactNode;
  run: () => void;
}

const GROUPS: Command["group"][] = ["Go to", "Plugins", "Waiting on you", "Connectors", "Do"];

/**
 * What one line is, in a word.
 *
 * A query ranks across every group at once, so the headings have to go -- a
 * heading inside a ranking puts the best match under the third one. Without
 * them a page, a plugin and a change waiting on somebody are three
 * indistinguishable rows, so each row carries its own kind instead.
 */
const KIND: Record<Command["group"], string> = {
  "Go to": "Page",
  Plugins: "Plugin",
  "Waiting on you": "Waiting",
  Connectors: "Connector",
  Do: "Action",
};

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
        id: `op:${op.id}`, group: "Waiting on you",
        // The change, not `label.set`. The palette is the one place somebody
        // reaches for a proposal by name, so the name has to be the one the
        // approvals page taught them.
        label: describeChange(op).headline,
        // The name the record already carries, resolved server-side; equal to
        // the identifier means nothing resolved, and then the words that need
        // no lookup are the better answer.
        hint: `proposed by ${
          op.requested_by_name && op.requested_by_name !== op.requested_by
            ? op.requested_by_name
            : principalWords(op.requested_by)
        }`,
        keywords: `${op.plugin} ${op.action} ${op.requested_by} ${op.id} ${op.risk}`,
        icon: ClipboardCheck,
        // The one fact worth having before you open it. A palette that lists
        // changes without saying which is critical makes you open each in turn
        // to find out, which is slower than the page it was meant to save.
        chip: (
          <Chip tone={riskTone(op.risk)}>{riskLabel(op.risk).toLowerCase()} risk</Chip>
        ),
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
      // A line says what happens when it is chosen, so each of these is the
      // action and not the setting's name.
      { id: "do:light", group: "Do", label: "Switch to the light theme", icon: Sun, keywords: "theme appearance", run: () => { chooseTheme("light"); onOpenChange(false); } },
      { id: "do:dark", group: "Do", label: "Switch to the dark theme", icon: Moon, keywords: "theme appearance", run: () => { chooseTheme("dark"); onOpenChange(false); } },
      { id: "do:system", group: "Do", label: "Match the system theme", icon: Monitor, keywords: "theme appearance", run: () => { chooseTheme("system"); onOpenChange(false); } },
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

  // Grouped only while nothing is typed. A query's answer is a ranking, and
  // headings inside a ranking would put the best match under the third one.
  const grouped = query.trim() === "";

  // Sections first, then the flat order derived from them, so what Enter opens
  // is the row the eye is on. Reading the two orders independently is how the
  // highlight and the action drift apart.
  const sections = useMemo(() => (grouped
    ? GROUPS
      .map((heading) => ({ heading, items: shown.filter((c) => c.group === heading) }))
      .filter((s) => s.items.length > 0)
    : [{ heading: null, items: shown }]
  ), [grouped, shown]);
  const ordered = useMemo(() => sections.flatMap((s) => s.items), [sections]);

  useEffect(() => setActive(0), [query]);

  // Keep the highlighted line in view as the arrows move it.
  useEffect(() => {
    const el = list.current?.querySelector<HTMLElement>(`[data-index="${active}"]`);
    el?.scrollIntoView({ block: "nearest" });
  }, [active]);

  function onKey(e: React.KeyboardEvent) {
    const last = ordered.length - 1;
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setActive((i) => Math.min(i + 1, last));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setActive((i) => Math.max(i - 1, 0));
    } else if (e.key === "Home") {
      e.preventDefault();
      setActive(0);
    } else if (e.key === "End") {
      e.preventDefault();
      setActive(Math.max(0, last));
    } else if (e.key === "Enter") {
      e.preventDefault();
      ordered[active]?.run();
    }
  }

  /**
   * Where the pointer last was, so the mouse stops overruling the keyboard.
   *
   * Arrowing scrolls the highlighted row into view, which slides the list
   * under a cursor that has not moved -- and the browser reports that as a
   * pointer event over whatever is now beneath it. Taking every such event as
   * an intention snapped the selection back on every second keypress. Only a
   * pointer at a new position is somebody choosing.
   */
  const point = useRef({ x: -1, y: -1 });
  function chooseByPointer(i: number, e: React.PointerEvent) {
    if (e.clientX === point.current.x && e.clientY === point.current.y) return;
    point.current = { x: e.clientX, y: e.clientY };
    setActive(i);
  }

  return (
    <DialogPrimitive.Root open={open} onOpenChange={onOpenChange}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="fixed inset-0 z-50 bg-black/50 data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:animate-in data-[state=open]:fade-in-0" />
        <DialogPrimitive.Content
          aria-describedby={undefined}
          className="fixed top-[12vh] left-1/2 z-50 w-[min(40rem,calc(100vw-2rem))] -translate-x-1/2 overflow-hidden rounded-lg border bg-popover text-popover-foreground shadow-xl duration-200 outline-none data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=closed]:zoom-out-95 data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=open]:zoom-in-95"
          onKeyDown={onKey}
        >
          <DialogPrimitive.Title className="sr-only">Search the dashboard</DialogPrimitive.Title>
          <div className="flex items-center gap-2 border-b px-3">
            <Search className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
            <input
              autoFocus
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Search pages, plugins and changes"
              aria-label="Search the dashboard"
              aria-activedescendant={ordered[active] ? `cmd-${ordered[active].id}` : undefined}
              role="combobox"
              aria-expanded={ordered.length > 0}
              aria-controls="command-results"
              aria-autocomplete="list"
              // The one place the global focus ring is deliberately not drawn.
              // That rule exists because a ring is a keyboard's only clue to
              // where it is; here focus never moves -- Radix traps it on this
              // input for as long as the box is open -- so a permanent ring
              // says nothing, while the row it would point at is the wrong
              // one. What the keyboard is actually on is the highlighted
              // option, which `aria-activedescendant` names and `bg-accent`
              // draws. The ring also sat on `ring-offset-background`, so
              // against the popover it read as a halo rather than an outline.
              className={cn(
                "h-12 w-full bg-transparent text-sm placeholder:text-muted-foreground",
                "outline-none focus-visible:ring-0 focus-visible:ring-offset-0",
              )}
            />
          </div>

          <div
            ref={list}
            id="command-results"
            role="listbox"
            aria-label="Results"
            className="scroll-panel max-h-[min(28rem,60vh)] p-2"
          >
            {ordered.length === 0 && (
              <p className="px-2 py-8 text-center text-sm text-muted-foreground">
                Nothing matches “{query.trim()}”.
              </p>
            )}
            {(() => {
              let index = -1;
              return sections.map((section) => (
                <div key={section.heading ?? "ranked"} className="mb-1 last:mb-0">
                  {section.heading && (
                    <p className="px-2 pt-2 pb-1 text-xs font-medium text-muted-foreground">
                      {section.heading}
                    </p>
                  )}
                  {section.items.map((c) => {
                    index += 1;
                    const i = index;
                    const Icon = c.icon;
                    return (
                      <div
                        key={c.id}
                        id={`cmd-${c.id}`}
                        role="option"
                        aria-selected={i === active}
                        data-index={i}
                        onPointerMove={(e) => chooseByPointer(i, e)}
                        onClick={() => c.run()}
                        className={cn(
                          "flex cursor-pointer items-center gap-2.5 rounded-md px-2 py-2 text-left text-sm",
                          i === active ? "bg-accent text-accent-foreground" : "text-foreground",
                        )}
                      >
                        <span className="flex size-4 shrink-0 items-center justify-center">
                          {c.mark ?? (Icon
                            ? <Icon className="size-4 text-muted-foreground" aria-hidden="true" />
                            : null)}
                        </span>

                        {/* One line, not two. Every page carries its lede as
                            a hint, so stacking them made a list of five
                            destinations as tall as a paragraph and pushed
                            everything else under the fold. The hint takes what
                            room is left and gives it back first. */}
                        <span className="flex min-w-0 flex-1 items-baseline gap-2">
                          <span className="min-w-0 truncate">{c.label}</span>
                          {c.hint && (
                            <span className="hidden min-w-0 flex-1 truncate text-xs text-muted-foreground sm:block">
                              {c.hint}
                            </span>
                          )}
                        </span>

                        {c.chip}

                        {/* Without headings there is nothing else saying what
                            kind of thing a row is, so the ranked list says it
                            per row and the grouped one does not repeat it. */}
                        {!grouped && (
                          <span className="shrink-0 text-xs text-muted-foreground">
                            {KIND[c.group]}
                          </span>
                        )}

                        <CornerDownLeft
                          className={cn(
                            "size-3.5 shrink-0",
                            i === active ? "text-muted-foreground" : "invisible",
                          )}
                          aria-hidden="true"
                        />
                      </div>
                    );
                  })}
                </div>
              ));
            })()}
          </div>

          {/* What the keys do, rather than one lone esc chip in the input. */}
          <div className="flex items-center justify-between gap-3 border-t px-3 py-2">
            <p className="text-xs text-muted-foreground">
              {ordered.length} {ordered.length === 1 ? "result" : "results"}
            </p>
            <p className="flex items-center gap-3 text-xs text-muted-foreground">
              <span className="flex items-center gap-1"><Key>↑</Key><Key>↓</Key> move</span>
              <span className="flex items-center gap-1"><Key>↵</Key> open</span>
              <span className="hidden items-center gap-1 sm:flex"><Key>esc</Key> close</span>
            </p>
          </div>
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  );
}

/** One key, drawn as a key. */
function Key({ children }: { children: ReactNode }) {
  return (
    <kbd className="rounded border bg-muted/50 px-1.5 py-0.5 font-mono text-[10px] leading-none">
      {children}
    </kbd>
  );
}
