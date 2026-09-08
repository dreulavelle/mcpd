/**
 * Telling the host somebody is still here.
 *
 * The idle clock has to be moved by a person, not by traffic. The console
 * refreshes several pages on a timer -- tunnels every eight seconds, the
 * overview every fifteen -- so a session renewed by requests would be renewed
 * by a window left open on a page nobody is looking at, and the timeout would
 * never once fire. Only the browser can tell a click from its own polling.
 *
 * It reports through a request of its own rather than by marking requests the
 * page was making anyway, because some pages make none: somebody typing on
 * Profile was reporting nothing and being signed out while working. The reply
 * carries the deadline the report just moved, which is what the countdown
 * reads -- without it the banner would count to a deadline the host had
 * already extended.
 *
 * A pointer, a key, or a navigation counts. Mouse movement deliberately does
 * not: a sleeping laptop with a cat on the desk is not somebody working, and a
 * jittery pointer would hold a session open all night.
 */

/** How often a report is worth making. The host throttles to a minute too. */
const REPORT_EVERY_MS = 60_000;

/** When the last report was sent, so a burst of clicks is still one request. */
let reported = 0;

/** Who to tell, and what to do with the answer. Set by the app at start. */
let send: (() => void) | null = null;

/**
 * Records that somebody did something, and reports it if it is time.
 *
 * Called by the pointer and key listeners, and by the router: a jump from the
 * command palette is somebody doing something and touches neither of those.
 */
export function noteInteraction(now = Date.now()): void {
  if (!send || now - reported < REPORT_EVERY_MS) return;
  reported = now;
  send();
}

/** For tests, which must not inherit the timer from the one before. */
export function resetActivity(): void {
  reported = 0;
  send = null;
}

/**
 * Starts listening, and returns a function that stops.
 *
 * Calling it twice replaces the first listener rather than adding a second,
 * so a hot reload or a remount cannot end up with two.
 */
let stopPrevious: (() => void) | null = null;

export function watchInteraction(report: () => void): () => void {
  stopPrevious?.();
  send = report;

  if (typeof document === "undefined") {
    stopPrevious = () => { send = null; };
    return stopPrevious;
  }

  // Capture phase: a handler that stops propagation is still somebody having
  // done something.
  const note = () => noteInteraction();
  const events = ["pointerdown", "keydown"] as const;
  for (const e of events) {
    document.addEventListener(e, note, { capture: true, passive: true });
  }

  stopPrevious = () => {
    for (const e of events) document.removeEventListener(e, note, { capture: true });
    send = null;
    stopPrevious = null;
  };
  return stopPrevious;
}
