import { useEffect, useRef } from "react";

/**
 * Keyboard shortcuts, in two shapes.
 *
 * A chord is a modifier and a key, pressed together: ⌘K opens the search.
 * A sequence is two keys in a row -- "g" then "a" goes to Approvals -- which
 * is what makes a whole console reachable from the home row without taking
 * every single letter away from the page. The gap between the two keys is
 * bounded, so a stray "g" typed a minute ago cannot turn the next keypress
 * into a jump.
 */
export interface Shortcut {
  /** "mod+k" for a chord, "g a" for a sequence, "?" for a single key. */
  keys: string;
  label: string;
  run: () => void;
}

/** How long the first key of a sequence waits for the second. */
const SEQUENCE_MS = 1500;

/** Where a keystroke belongs to the page and not to the console. */
function typing(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  if (target.isContentEditable) return true;
  const tag = target.tagName;
  if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") return true;
  // A dialog has its own keyboard: Escape closes it, Enter submits it, and a
  // sequence started inside one would jump out from under it.
  return target.closest("[role=dialog]") !== null;
}

/** "mod" is ⌘ on a Mac and Ctrl everywhere else, which is what people expect. */
function chordMatches(keys: string, e: KeyboardEvent): boolean {
  const parts = keys.toLowerCase().split("+");
  const key = parts[parts.length - 1]!;
  const wantMod = parts.includes("mod");
  const wantShift = parts.includes("shift");
  const mod = e.metaKey || e.ctrlKey;
  return e.key.toLowerCase() === key && mod === wantMod && e.shiftKey === wantShift && !e.altKey;
}

export function useShortcuts(shortcuts: Shortcut[]): void {
  const latest = useRef(shortcuts);
  latest.current = shortcuts;
  const pending = useRef<{ key: string; at: number } | null>(null);

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.defaultPrevented || e.isComposing) return;
      const list = latest.current;

      // Chords first, because they work from inside an input too: ⌘K from a
      // search box is still a request for the palette.
      for (const s of list) {
        if (s.keys.includes("+") && chordMatches(s.keys, e)) {
          e.preventDefault();
          s.run();
          return;
        }
      }

      if (typing(e.target) || e.metaKey || e.ctrlKey || e.altKey) return;

      const now = Date.now();
      const first = pending.current;
      pending.current = null;
      if (first && now - first.at <= SEQUENCE_MS) {
        const seq = `${first.key} ${e.key}`;
        const hit = list.find((s) => s.keys === seq);
        if (hit) {
          e.preventDefault();
          hit.run();
          return;
        }
      }

      const single = list.find((s) => s.keys === e.key);
      if (single) {
        e.preventDefault();
        single.run();
        return;
      }
      // A key that begins some sequence is remembered for one more press.
      if (list.some((s) => s.keys.startsWith(e.key + " "))) {
        pending.current = { key: e.key, at: now };
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
}

/** Whether this is a Mac, for drawing ⌘ rather than Ctrl. */
export function isMac(): boolean {
  return typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform ?? "");
}

/** "mod+k" as the reader will see it on their keyboard. */
export function describeKeys(keys: string): string[] {
  if (keys.includes("+")) {
    return keys.split("+").map((k) => {
      if (k === "mod") return isMac() ? "⌘" : "Ctrl";
      if (k === "shift") return "⇧";
      return k.toUpperCase();
    });
  }
  return keys.split(" ");
}
