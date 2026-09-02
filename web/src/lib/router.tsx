import {
  createContext, useCallback, useContext, useEffect, useMemo, useState,
  type MouseEvent, type ReactNode,
} from "react";

/**
 * The console's routing, hand-rolled. Real paths rather than a hash, which
 * works because `staticHandler` serves index.html for any unknown path.
 */

interface RouterValue {
  /** The current path, always beginning with "/" and never trailing one. */
  path: string;
  /** The query, so a filter can live in the address and survive a reload. */
  search: string;
  navigate: (to: string, options?: NavigateOptions) => void;
}

interface NavigateOptions {
  replace?: boolean;
  /**
   * Whether to scroll to the top. On by default, because opening a detail
   * from the bottom of a long list must not land halfway down the detail;
   * off for a filter change, which is the same page with a narrower view.
   */
  scroll?: boolean;
}

const RouterContext = createContext<RouterValue>({
  path: "/",
  search: "",
  navigate: () => undefined,
});

/** Trims a path to the form the rest of the app compares against. */
export function normalize(raw: string): string {
  const path = raw.split("?")[0]!.split("#")[0]!;
  const trimmed = path.replace(/\/+$/, "");
  return trimmed === "" ? "/" : trimmed;
}

/** The query part of an address, "?" included, or "" when there is none. */
function searchOf(raw: string): string {
  const at = raw.indexOf("?");
  if (at < 0) return "";
  const q = raw.slice(at).split("#")[0]!;
  return q === "?" ? "" : q;
}

export function RouterProvider({ children }: { children: ReactNode }) {
  const [where, setWhere] = useState(() => ({
    path: normalize(window.location.pathname),
    search: searchOf(window.location.search),
  }));

  useEffect(() => {
    const onPop = () => setWhere({
      path: normalize(window.location.pathname),
      search: searchOf(window.location.search),
    });
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  }, []);

  const navigate = useCallback((to: string, options?: NavigateOptions) => {
    const path = normalize(to);
    const search = searchOf(to);
    if (path === normalize(window.location.pathname) &&
        search === searchOf(window.location.search)) return;
    const href = path + search;
    if (options?.replace) window.history.replaceState(null, "", href);
    else window.history.pushState(null, "", href);
    setWhere({ path, search });
    if (options?.scroll !== false) window.scrollTo(0, 0);
  }, []);

  const value = useMemo(
    () => ({ path: where.path, search: where.search, navigate }),
    [where, navigate],
  );
  return <RouterContext.Provider value={value}>{children}</RouterContext.Provider>;
}

export function useRouter(): RouterValue {
  return useContext(RouterContext);
}

/**
 * A filter kept in the address, so a link can arrive with it set and a reload
 * keeps it. Setting a key to "" removes it, so the address for "no filter"
 * is the plain one rather than one carrying an empty parameter.
 */
export function useQueryParam(key: string): [string, (value: string) => void] {
  const { path, search, navigate } = useRouter();
  const value = useMemo(() => new URLSearchParams(search).get(key) ?? "", [search, key]);
  const set = useCallback((next: string) => {
    const params = new URLSearchParams(window.location.search);
    if (next === "") params.delete(key);
    else params.set(key, next);
    const q = params.toString();
    navigate(path + (q ? `?${q}` : ""), { replace: true, scroll: false });
  }, [key, path, navigate]);
  return [value, set];
}

/** "/plugins/weather" is ["plugins", "weather"]; the root is an empty array. */
export function useSegments(): string[] {
  const { path } = useRouter();
  return useMemo(
    () => path.split("/").filter(Boolean).map(decodeURIComponent),
    [path],
  );
}

/**
 * An internal link. A real anchor, intercepting only the plain left click, so
 * middle-click and the status bar still work.
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
