import { afterEach, describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { resolve, useTheme } from "./theme";

function system(dark: boolean) {
  const listeners = new Set<() => void>();
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: dark && query.includes("dark"),
    media: query,
    addEventListener: (_: string, fn: () => void) => listeners.add(fn),
    removeEventListener: (_: string, fn: () => void) => listeners.delete(fn),
  }));
  return { change: (nowDark: boolean) => { system(nowDark); listeners.forEach((fn) => fn()); } };
}

describe("appearance", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    window.localStorage.clear();
    delete document.documentElement.dataset.theme;
  });

  // Nobody chose anything, so the console looks exactly as it always did.
  it("follows the system until somebody chooses", () => {
    system(true);
    expect(resolve("system")).toBe("dark");
    const { result } = renderHook(() => useTheme());
    expect(result.current[0]).toBe("system");
    expect(document.documentElement.dataset.theme).toBe("dark");
  });

  it("keeps a choice in this browser and applies it at once", () => {
    system(true);
    const { result } = renderHook(() => useTheme());
    act(() => result.current[1]("light"));
    expect(document.documentElement.dataset.theme).toBe("light");
    expect(window.localStorage.getItem("mcpd.theme")).toBe("light");

    // A fresh mount reads the same choice back.
    const again = renderHook(() => useTheme());
    expect(again.result.current[0]).toBe("light");
  });

  it("goes back to the system's choice when asked, and forgets the override", () => {
    window.localStorage.setItem("mcpd.theme", "dark");
    system(false);
    const { result } = renderHook(() => useTheme());
    expect(document.documentElement.dataset.theme).toBe("dark");
    act(() => result.current[1]("system"));
    expect(document.documentElement.dataset.theme).toBe("light");
    expect(window.localStorage.getItem("mcpd.theme")).toBeNull();
  });

  it("ignores a stored value it does not recognise", () => {
    window.localStorage.setItem("mcpd.theme", "sepia");
    system(false);
    const { result } = renderHook(() => useTheme());
    expect(result.current[0]).toBe("system");
  });
});
