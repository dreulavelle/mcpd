import { afterEach, describe, expect, it, vi } from "vitest";
import { renderHook } from "@testing-library/react";
import { useShortcuts, type Shortcut } from "./shortcuts";

function press(key: string, init: KeyboardEventInit = {}, target: EventTarget = window) {
  const e = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init });
  target.dispatchEvent(e);
  return e;
}

describe("keyboard shortcuts", () => {
  afterEach(() => {
    vi.useRealTimers();
    document.body.innerHTML = "";
  });

  function mount() {
    const ran: string[] = [];
    const list: Shortcut[] = [
      { keys: "mod+k", label: "search", run: () => ran.push("search") },
      { keys: "?", label: "help", run: () => ran.push("help") },
      { keys: "g a", label: "approvals", run: () => ran.push("approvals") },
    ];
    renderHook(() => useShortcuts(list));
    return ran;
  }

  it("runs a chord whichever modifier the platform uses", () => {
    const ran = mount();
    press("k", { metaKey: true });
    press("k", { ctrlKey: true });
    expect(ran).toEqual(["search", "search"]);
  });

  it("runs a two-key sequence pressed in order", () => {
    const ran = mount();
    press("g");
    press("a");
    expect(ran).toEqual(["approvals"]);
  });

  // The first key of a sequence is remembered for a moment and no longer: a
  // "g" typed a minute ago must not turn the next "a" into a jump.
  it("forgets the first key of a sequence after a moment", () => {
    vi.useFakeTimers();
    const ran = mount();
    press("g");
    vi.advanceTimersByTime(2000);
    press("a");
    expect(ran).toEqual([]);
  });

  it("stays out of the way while somebody is typing", () => {
    const ran = mount();
    const input = document.createElement("input");
    document.body.appendChild(input);
    press("?", {}, input);
    press("g", {}, input);
    press("a", {}, input);
    expect(ran).toEqual([]);
    // The chord still works from a field: ⌘K in a search box is still a
    // request for the palette.
    press("k", { metaKey: true }, input);
    expect(ran).toEqual(["search"]);
  });

  it("stays out of the way inside a dialog", () => {
    const ran = mount();
    const dialog = document.createElement("div");
    dialog.setAttribute("role", "dialog");
    const button = document.createElement("button");
    dialog.appendChild(button);
    document.body.appendChild(dialog);
    press("?", {}, button);
    expect(ran).toEqual([]);
  });

  it("claims the keystroke it handled and no other", () => {
    mount();
    expect(press("?").defaultPrevented).toBe(true);
    expect(press("z").defaultPrevented).toBe(false);
  });
});
