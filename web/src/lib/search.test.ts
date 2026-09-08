import { describe, expect, it } from "vitest";
import { score } from "./search";

describe("ranking a search", () => {
  it("prefers a match at the start, then at a word, then anywhere, then scattered", () => {
    const q = "plug";
    expect(score("plugins", q)).toBeGreaterThan(score("the plugins", q));
    expect(score("the plugins", q)).toBeGreaterThan(score("unplugged", q));
    expect(score("unplugged", q)).toBeGreaterThan(score("p l u g", q));
    expect(score("p-lug", q)).toBeGreaterThan(0);
  });

  it("matches everything with nothing typed and nothing with a stray letter", () => {
    expect(score("anything", "")).toBeGreaterThan(0);
    expect(score("plugins", "zq")).toBe(0);
  });

  it("does not care about case", () => {
    expect(score("Approvals", "app")).toBe(100);
  });

  /**
   * People type the words they remember, in the order they think of them.
   * Scoring the whole query as one string made the order load-bearing:
   * "settings keys" landed in the bottom bucket because its characters happen
   * to appear in order with the space falling in the gap, and "keys settings"
   * scored nothing at all.
   */
  it("does not care what order the words come in", () => {
    expect(score("Settings › Keys", "keys settings"))
      .toBe(score("Settings › Keys", "settings keys"));
    expect(score("Backup & Restore", "restore backup")).toBeGreaterThan(50);
    expect(score("Restart the device on cnmaestro", "cnmaestro restart"))
      .toBeGreaterThan(50);
  });

  /** Typing more words narrows the list. It must never widen it. */
  it("requires every word to be there", () => {
    expect(score("Settings › Keys", "settings certificates")).toBe(0);
    expect(score("Approvals", "approvals zq")).toBe(0);
  });

  /** A word that matched well should not be dragged down by the count. */
  it("scores a multi-word match on how well it matched, not how long it was", () => {
    // Both words sit at the start of a word, so this is as good as one of them.
    expect(score("Backup & Restore", "backup restore")).toBeGreaterThanOrEqual(80);
  });

  it("ignores stray whitespace around and between the words", () => {
    expect(score("Settings › Keys", "  settings   keys  "))
      .toBe(score("Settings › Keys", "settings keys"));
    expect(score("anything", "   ")).toBeGreaterThan(0);
  });
});