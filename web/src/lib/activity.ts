/**
 * When somebody last did something, as opposed to when this page last spoke.
 *
 * The two are not the same and the difference is the whole point. The console
 * refreshes several pages on a timer -- tunnels every eight seconds, the
 * overview every fifteen -- so a session renewed by traffic would be renewed
 * by a window left open on a page nobody is looking at, and the idle timeout
 * would never once fire. Only the browser can tell a click from its own
 * polling, so it is the browser that says.
 *
 * A pointer down, a key, or a navigation counts. Mouse movement deliberately
 * does not: a sleeping laptop with a cat on the desk is not somebody working,
 * and a jittery mouse would hold a session open all night.
 */

/** Epoch milliseconds of the last thing a person did. */
let last = Date.now();

/** How recent an interaction has to be for a request to carry the marker. */
const WINDOW_MS = 60_000;

/** Records that somebody did something. Called by the router on navigation. */
export function noteInteraction(): void {
  last = Date.now();
}

/** Whether a person has done something recently enough to count as present. */
export function recentlyInteracted(now = Date.now()): boolean {
  return now - last < WINDOW_MS;
}

/** For tests, which must not inherit a timestamp from the one before. */
export function resetInteraction(at = Date.now()): void {
  last = at;
}

/**
 * Starts listening. Idempotent, and safe where there is no DOM.
 *
 * Capture phase, because a handler that stops propagation is still somebody
 * having done something.
 */
export function watchInteraction(): () => void {
  if (typeof document === "undefined") return () => undefined;

  const note = () => noteInteraction();
  const events = ["pointerdown", "keydown"] as const;
  for (const e of events) document.addEventListener(e, note, { capture: true, passive: true });

  return () => {
    for (const e of events) document.removeEventListener(e, note, { capture: true });
  };
}
