import { useCallback, useEffect, useState } from "react";

/**
 * Appearance: the system's choice, or one of the two palettes by name.
 *
 * "system" is the default and what everybody had before this existed, so an
 * account that never touches the control sees exactly what it always did.
 * The choice lives in this browser rather than on the account, because it
 * is a fact about the screen being looked at -- a dark laptop and a light
 * monitor are two different answers, not one setting.
 */
export type Theme = "system" | "light" | "dark";

export const THEMES: Theme[] = ["system", "light", "dark"];

/** Shared with public/theme.js, which applies the choice before first paint. */
const KEY = "mcpd.theme";

const MEDIA = "(prefers-color-scheme: dark)";

function stored(): Theme {
  try {
    const v = localStorage.getItem(KEY);
    return v === "light" || v === "dark" ? v : "system";
  } catch {
    return "system";
  }
}

/** Which palette a choice comes out as, right now. */
export function resolve(theme: Theme): "light" | "dark" {
  if (theme !== "system") return theme;
  return typeof window !== "undefined" && window.matchMedia?.(MEDIA).matches
    ? "dark"
    : "light";
}

/** Writes the palette onto the root, where index.css reads it. */
export function apply(theme: Theme): void {
  document.documentElement.dataset.theme = resolve(theme);
}

/**
 * The current choice and a way to change it. Following the system means
 * following it as it changes, so a laptop that turns dark at sunset takes the
 * console with it -- but only while nobody has chosen otherwise.
 */
export function useTheme(): [Theme, (next: Theme) => void] {
  const [theme, setTheme] = useState<Theme>(stored);

  useEffect(() => {
    apply(theme);
    if (theme !== "system" || typeof window === "undefined" || !window.matchMedia) return;
    const media = window.matchMedia(MEDIA);
    const follow = () => apply("system");
    media.addEventListener("change", follow);
    return () => media.removeEventListener("change", follow);
  }, [theme]);

  const choose = useCallback((next: Theme) => {
    try {
      if (next === "system") localStorage.removeItem(KEY);
      else localStorage.setItem(KEY, next);
    } catch {
      // Private mode: the choice lasts for this page and no longer.
    }
    setTheme(next);
  }, []);

  return [theme, choose];
}
