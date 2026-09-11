import { afterEach, describe, expect, it, vi } from "vitest";
import { copyText } from "./clipboard";

/** Makes this page a plain-http LAN address, where the clipboard API is absent. */
function plainHTTP() {
  Object.defineProperty(window, "isSecureContext", { value: false, configurable: true });
  Object.defineProperty(navigator, "clipboard", { value: undefined, configurable: true });
}

function secure(writeText: (text: string) => Promise<void>) {
  Object.defineProperty(window, "isSecureContext", { value: true, configurable: true });
  Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
}

describe("copying", () => {
  const original = document.execCommand;
  afterEach(() => {
    document.execCommand = original;
    vi.restoreAllMocks();
  });

  it("uses the clipboard API where the page is secure", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    secure(writeText);

    expect(await copyText("https://mcpd.example.com/api/auth/sso/entra/callback")).toBe(true);
    expect(writeText).toHaveBeenCalledWith("https://mcpd.example.com/api/auth/sso/entra/callback");
  });

  // The bug this exists for: on http by LAN address the clipboard API is
  // absent, and every copy button caught the failure and did nothing.
  it("copies by selection on a plain-http address", async () => {
    plainHTTP();
    let selected = "";
    document.execCommand = vi.fn(() => {
      // What a real browser would copy: the selection in the scratch textarea.
      const area = document.querySelector("textarea")!;
      selected = area.value.slice(area.selectionStart, area.selectionEnd);
      return true;
    });

    expect(await copyText("203.0.113.10")).toBe(true);
    expect(document.execCommand).toHaveBeenCalledWith("copy");
    expect(selected).toBe("203.0.113.10");
    // The scratch textarea is gone again.
    expect(document.querySelector("textarea")).toBeNull();
  });

  it("falls back to selection when the clipboard API refuses", async () => {
    secure(vi.fn().mockRejectedValue(new Error("denied by policy")));
    document.execCommand = vi.fn(() => true);

    expect(await copyText("x")).toBe(true);
    expect(document.execCommand).toHaveBeenCalledWith("copy");
  });

  // A modal keeps focus to itself, so the scratch textarea goes beside the
  // button that was pressed rather than on the body outside the dialog.
  it("puts the scratch textarea beside the button, inside whatever holds it", async () => {
    plainHTTP();
    const dialog = document.createElement("div");
    const button = document.createElement("button");
    dialog.appendChild(button);
    document.body.appendChild(dialog);
    let parent: Element | null = null;
    document.execCommand = vi.fn(() => {
      parent = document.querySelector("textarea")?.parentElement ?? null;
      return true;
    });

    await copyText("x", button);
    expect(parent).toBe(dialog);
    dialog.remove();
  });

  it("says so when nothing could copy", async () => {
    plainHTTP();
    document.execCommand = vi.fn(() => false);

    expect(await copyText("x")).toBe(false);
  });
});
