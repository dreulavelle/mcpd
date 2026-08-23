import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach, vi } from "vitest";

// Every test gets a fresh document and fresh stubs. Without the cleanup a
// query that should find one element finds three left over from earlier tests,
// and the failure points at the wrong place.
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

// jsdom implements none of these, and all of them are reached by ordinary
// components: Radix measures with ResizeObserver, its dialog captures the
// pointer, and the router scrolls to the top on every navigation.
globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
} as unknown as typeof ResizeObserver;

Element.prototype.hasPointerCapture ??= () => false;
Element.prototype.setPointerCapture ??= () => {};
Element.prototype.releasePointerCapture ??= () => {};
Element.prototype.scrollIntoView ??= () => {};

window.scrollTo = () => {};

window.matchMedia ??= ((query: string) => ({
  matches: false,
  media: query,
  onchange: null,
  addListener: () => {},
  removeListener: () => {},
  addEventListener: () => {},
  removeEventListener: () => {},
  dispatchEvent: () => false,
})) as typeof window.matchMedia;
