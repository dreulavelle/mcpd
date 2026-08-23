import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

/**
 * The palette, checked against WCAG rather than against taste.
 *
 * A colour token is read once and used everywhere, so a value that fails
 * contrast fails in every page at once and in none of them obviously. The
 * sidebar's group headings were 2.5:1 for exactly that reason: the grey was
 * inherited from a console where those words were decoration, and nothing
 * re-checked it when the new information architecture made them navigation.
 */

// Read from disk rather than imported: the point is to check the values that
// ship, and a bundler would hand back a class-name map instead.
const SRC = join(process.cwd(), "src");
const css = readFileSync(join(SRC, "index.css"), "utf8");

/** Reads a custom property out of the light block or the dark one. */
function token(name: string, theme: "light" | "dark"): string {
  // The dark block is the only place a property is redefined, so splitting on
  // it gives two haystacks with one definition each.
  const [light, dark] = css.split("@media (prefers-color-scheme: dark)");
  const haystack = theme === "light" ? light! : dark!;
  const match = haystack.match(new RegExp(`--${name}:\\s*(#[0-9a-fA-F]{6})`));
  if (!match) throw new Error(`no --${name} in the ${theme} palette`);
  return match[1]!.toLowerCase();
}

const channel = (c: number) =>
  c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;

function luminance(hex: string): number {
  const [r, g, b] = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255);
  return 0.2126 * channel(r!) + 0.7152 * channel(g!) + 0.0722 * channel(b!);
}

function contrast(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi! + 0.05) / (lo! + 0.05);
}

/** Every surface a word is drawn on, in one theme. */
const SURFACES = ["background", "card", "muted", "accent"] as const;

/** Every token used as a text colour. */
const TEXT = ["foreground", "muted-foreground", "good", "attention", "problem", "info"] as const;

describe.each(["light", "dark"] as const)("the %s palette", (theme) => {
  it.each(TEXT)("draws %s legibly on every surface", (name) => {
    const fg = token(name, theme);
    for (const surface of SURFACES) {
      const bg = token(surface, theme);
      expect(
        contrast(fg, bg),
        `--${name} on --${surface} in ${theme}`,
      ).toBeGreaterThanOrEqual(4.5);
    }
  });

  it("keeps the primary action legible against its own foreground", () => {
    expect(contrast(token("primary", theme), token("primary-foreground", theme)))
      .toBeGreaterThanOrEqual(4.5);
  });

  // No assertion about --border or --faint. Both are decoration: a card's
  // hairline, and a dot that never appears without the word it agrees with
  // beside it. 1.4.11's 3:1 governs a control whose identification depends on
  // the mark, which neither is, and picking a lower number would be asserting
  // a threshold nothing supports.
});

/**
 * `--faint` is deliberately not a text colour.
 *
 * It cannot be one: solving it for 4.5:1 in the light theme lands it darker
 * than `--muted-foreground`, so it stops being a fainter step and becomes the
 * same step twice. This asserts the rule the comment in index.css states, so
 * the next `text-faint` fails here rather than in an audit.
 */
it("is never used as a text colour", () => {
  const offenders: string[] = [];
  const walk = (dir: string) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const path = join(dir, entry.name);
      if (entry.isDirectory()) walk(path);
      else if (entry.name.endsWith(".tsx") &&
        readFileSync(path, "utf8").includes("text-faint")) {
        offenders.push(path.slice(SRC.length + 1));
      }
    }
  };
  walk(SRC);
  expect(offenders).toEqual([]);
});
