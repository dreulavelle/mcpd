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

// jsdom has no canvas: its getContext logs "not implemented" and returns
// nothing. The sign-in field asks for a context and draws only if it gets
// one, so a null here is the supported path, minus the noise.
HTMLCanvasElement.prototype.getContext = (() => null) as typeof HTMLCanvasElement.prototype.getContext;
