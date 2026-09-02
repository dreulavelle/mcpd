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
});
