/**
 * Copies text by whichever route this browser allows, and says whether it
 * worked.
 *
 * `navigator.clipboard` exists only in a secure context -- https, or
 * localhost -- and this dashboard is routinely reached over plain http by a
 * LAN address. There the modern API is simply absent, and every copy button
 * that called it and nothing else did nothing and said nothing. The older
 * `execCommand("copy")` works in any context, from a click, by copying a
 * selection; so the text is put in a textarea that exists only for the copy.
 *
 * `near` is where that textarea goes. Inside a dialog it has to be inside the
 * dialog: a modal keeps focus within itself, and a textarea on the body
 * outside it can be neither focused nor selected, so the copy fails there
 * and nowhere else.
 */
export async function copyText(text: string, near?: Element | null): Promise<boolean> {
  if (window.isSecureContext && navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // Refused despite being there -- a permissions policy, usually. The
      // older route may still be allowed.
    }
  }
  return copyBySelection(text, near?.parentElement ?? document.body);
}

function copyBySelection(text: string, parent: Element): boolean {
  if (typeof document.execCommand !== "function") return false;

  const area = document.createElement("textarea");
  area.value = text;
  area.setAttribute("readonly", "");
  area.setAttribute("aria-hidden", "true");
  area.tabIndex = -1;
  // Off-screen rather than hidden: an element that is display:none or
  // visibility:hidden cannot hold a selection, and the copy copies nothing.
  Object.assign(area.style, {
    position: "fixed", top: "0", left: "-9999px", opacity: "0", pointerEvents: "none",
  });
  const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  parent.appendChild(area);
  area.select();
  let ok = false;
  try {
    ok = document.execCommand("copy");
  } catch {
    // Deprecated, and occasionally disabled by policy.
  }
  area.remove();
  // Back to the button that was pressed, so a keyboard user is where they were.
  previous?.focus();
  return ok;
}

/** The shortcut to copy a selection by hand, for when nothing else worked. */
export function copyShortcut(): string {
  return navigator.platform?.startsWith("Mac") ? "⌘C" : "Ctrl+C";
}
