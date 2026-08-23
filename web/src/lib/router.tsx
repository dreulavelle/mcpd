import {
  createContext, useCallback, useContext, useEffect, useMemo, useState,
  type MouseEvent, type ReactNode,
} from "react";

/**
 * The console's routing.
 *
 * Hand-rolled rather than a router dependency, because what the console needs
 * from routing is a path and a way to change it. There are no nested layouts,
 * no loaders, no route-level data, and no redirect chains: a section, an
 * optional record within it, and the back button working. That is thirty lines
 * against a dependency to keep in step, and this repository's standing
 * preference is the thirty lines.
 *
 * Real paths rather than a hash, because `staticHandler` already serves
 * index.html for any path it has no file for, so a reload on /approvals/op-1
 * resolves. A hash would work too and read worse in a bookmark.
 */

interface RouterValue {
  /** The current path, always beginning with "/" and never trailing one. */
  path: string;
  navigate: (to: string, options?: { replace?: boolean }) => void;
}

const RouterContext = createContext<RouterValue>({
  path: "/",
  navigate: () => undefined,
});

/** Trims a path to the form the rest of the app compares against. */
export function normalize(raw: string): string {
  const path = raw.split("?")[0]!.split("#")[0]!;
  const trimmed = path.replace(/\/+$/, "");
  return trimmed === "" ? "/" : trimmed;
}

export function RouterProvider({ children }: { children: ReactNode }) {
  const [path, setPath] = useState(() => normalize(window.location.pathname));

  useEffect(() => {
    const onPop = () => setPath(normalize(window.location.pathname));
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  }, []);

  const navigate = useCallback((to: string, options?: { replace?: boolean }) => {
    const next = normalize(to);
    if (next === normalize(window.location.pathname)) return;
    if (options?.replace) window.history.replaceState(null, "", next);
    else window.history.pushState(null, "", next);
    setPath(next);
    // A new page starts at the top. Without this, opening a detail from the
    // bottom of a long list lands halfway down the detail.
    window.scrollTo(0, 0);
  }, []);

  const value = useMemo(() => ({ path, navigate }), [path, navigate]);
  return <RouterContext.Provider value={value}>{children}</RouterContext.Provider>;
}

export function useRouter(): RouterValue {
  return useContext(RouterContext);
}

/**
 * Splits the current path into its segments.
 *
 * "/marketplace/weather/tools" is ["marketplace", "weather", "tools"]. The
 * root is an empty array.
 */
export function useSegments(): string[] {
  const { path } = useRouter();
  return useMemo(
    () => path.split("/").filter(Boolean).map(decodeURIComponent),
    [path],
  );
}

/**
 * An internal link.
 *
 * Renders a real anchor so the address is in the status bar and a middle-click
 * opens a tab, and intercepts only the plain left click. Handing every click
 * to the router would break both.
 */
export function Link({ to, className, children, onClick, current }: {
  to: string;
  className?: string;
  children: ReactNode;
  onClick?: () => void;
  /** Marks this as the page being looked at, for assistive technology. */
  current?: boolean;
}) {
  const { navigate } = useRouter();

  function handle(e: MouseEvent<HTMLAnchorElement>) {
    if (e.defaultPrevented) return;
    if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    e.preventDefault();
    onClick?.();
    navigate(to);
  }

  return (
    <a
      href={to} className={className} onClick={handle}
      aria-current={current ? "page" : undefined}
    >
      {children}
    </a>
  );
}
