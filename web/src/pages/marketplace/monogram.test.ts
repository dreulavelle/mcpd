import { describe, expect, it } from "vitest";
import { initials, luminance, monogram } from "./monogram";

describe("the letters", () => {
  it("takes two words over one", () => {
    expect(initials("GitHub Issues")).toBe("GI");
  });

  it("takes two letters from a single word", () => {
    expect(initials("Weather")).toBe("WE");
  });

  it("ends a word at a digit, so Context7 is C7 and not Co", () => {
    expect(initials("Context7")).toBe("C7");
  });

  it("splits a camel-cased name", () => {
    expect(initials("PagerDuty")).toBe("PD");
  });

  it("has something to draw for a name it cannot read", () => {
    expect(initials("   ")).toBe("?");
  });

  // A reverse-DNS name is mostly the publisher, and "io" on forty cards says
  // nothing. Only the last segment is read.
  it("reads the last segment of a reverse-DNS name, not the publisher", () => {
    expect(monogram("io.github.example/weather").text).toBe("WE");
  });

  it("prefers the title, which is what a person reads", () => {
    expect(monogram("io.github.example/mcp", "Context7").text).toBe("C7");
  });
});

describe("the colour", () => {
  it("is the same for the same server, every render and every machine", () => {
    expect(monogram("com.example/weather").background)
      .toBe(monogram("com.example/weather").background);
  });

  // Keyed on the catalogue's identifier rather than the title, so a server
  // that gets a nicer title keeps the colour people recognise.
  it("survives a change of title", () => {
    expect(monogram("com.example/weather", "Weather").background)
      .toBe(monogram("com.example/weather", "Forecasts").background);
  });

  it("differs between servers", () => {
    const seen = new Set(
      ["a", "b", "c", "d", "e", "f", "g", "h"].map((n) => monogram(n).background),
    );
    expect(seen.size).toBeGreaterThan(4);
  });

  /**
   * The generated colours are outside the palette `index.test.ts` guards, so
   * the guarantee has to hold by construction: black and white cross at
   * 4.58:1, so taking the better of the two never drops below it. A fixed ink
   * would fail on roughly half the wheel.
   */
  it("draws legibly on every colour it can generate", () => {
    const contrast = (a: string, b: string) => {
      const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
      return (hi! + 0.05) / (lo! + 0.05);
    };

    for (let i = 0; i < 720; i++) {
      const mark = monogram(`server-${i}`);
      expect(
        contrast(mark.background, mark.ink),
        `${mark.text} on ${mark.background}`,
      ).toBeGreaterThanOrEqual(4.5);
    }
  });
});
