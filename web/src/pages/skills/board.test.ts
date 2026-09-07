import { describe, expect, it } from "vitest";
import {
  compact, movers, publishers, search, linePath, trend, type Skill,
} from "./board";

function skill(over: Partial<Skill> = {}): Skill {
  return { source: "acme/skills", id: "one", name: "one", installs: 10, ...over };
}

describe("trend", () => {
  it("compares the later half against the earlier one, not week against week", () => {
    // 100 then 200: the halves differ by a doubling. A last-against-previous
    // reading would call the same series flat, because 200 follows 200.
    expect(trend([50, 50, 50, 50, 100, 100, 100, 100])).toBeCloseTo(1);
  });

  it("reports a fall as a negative fraction", () => {
    expect(trend([100, 100, 50, 50])).toBeCloseTo(-0.5);
  });

  /**
   * A skill that went from nothing to something has grown by no finite
   * percentage. Dividing by the earlier half regardless renders "+Infinity%"
   * in the row, which is why this returns null instead.
   */
  it("gives no number when the earlier half is zero", () => {
    expect(trend([0, 0, 500, 900])).toBeNull();
  });

  it("gives no number without enough of a series to halve", () => {
    expect(trend(undefined)).toBeNull();
    expect(trend([5, 9])).toBeNull();
  });
});

describe("movers", () => {
  const rows = [
    skill({ id: "up", weekly: [10, 10, 30, 30] }),        // +100%
    skill({ id: "flat", weekly: [10, 10, 10, 10] }),      //    0%
    skill({ id: "down", weekly: [40, 40, 10, 10] }),      //  -75%
    skill({ id: "none" }),
  ];

  it("ranks by movement, not by size", () => {
    const { climbing, falling } = movers(rows, 1);
    expect(climbing[0]?.skill.id).toBe("up");
    expect(falling[0]?.skill.id).toBe("down");
  });

  /**
   * Only the all-time board carries a series. Scoring a row without one as
   * flat would rank it above a real decline, which is a skill nobody can see
   * moving beating one that visibly fell.
   */
  it("skips a row with no series rather than scoring it flat", () => {
    const { climbing, falling } = movers(rows, 3);
    const ids = [...climbing, ...falling].map((m) => m.skill.id);
    expect(ids).not.toContain("none");
  });

  /** With too few to go round, the falling side gives up rows. */
  it("never puts the same skill on both sides", () => {
    const { climbing, falling } = movers(rows, 3);
    const ids = [...climbing, ...falling].map((m) => m.skill.id);
    expect(new Set(ids).size).toBe(ids.length);
  });
});

describe("linePath", () => {
  it("draws one point per reading, spanning the full width", () => {
    const d = linePath([1, 2, 3], 80, 20);
    expect(d.match(/[ML]/g)).toHaveLength(3);
    expect(d).toContain("M0.0 20.0");   // the low sits on the floor
    expect(d).toContain("L80.0 0.0");   // the high on the ceiling
  });

  /**
   * A flat series has no span to scale against. Dividing by it gives NaN and
   * the row renders no line at all, so a flat line through the middle is drawn
   * instead -- which is also what "no change" should look like.
   */
  it("draws a flat series through the middle rather than dividing by zero", () => {
    const d = linePath([7, 7, 7], 80, 20);
    expect(d).toBe("M0.0 10.0 L40.0 10.0 L80.0 10.0");
  });

  it("draws nothing from a single reading", () => {
    expect(linePath([4], 80, 20)).toBe("");
  });
});

describe("search", () => {
  const rows = [
    skill({ name: "frontend-design", source: "anthropics/skills" }),
    skill({ name: "tdd", source: "mattpocock/skills" }),
  ];

  it("matches the publisher as well as the name", () => {
    expect(search(rows, "ANTHROPICS")).toHaveLength(1);
    expect(search(rows, "tdd")[0]?.name).toBe("tdd");
  });

  it("returns everything for a blank or whitespace query", () => {
    expect(search(rows, "   ")).toHaveLength(2);
  });
});

describe("compact", () => {
  it("shortens once a number stops being readable in full", () => {
    expect(compact(3_293_855)).toBe("3.3M");
    expect(compact(48_120)).toBe("48K");
    expect(compact(1_240)).toBe("1.2K");
    expect(compact(286)).toBe("286");
  });
});

describe("publishers", () => {
  const rows = [
    skill({ source: "acme/pack", id: "a", installs: 60 }),
    skill({ source: "acme/pack", id: "b", installs: 20 }),
    skill({ source: "globex/pack", id: "c", installs: 20 }),
  ];

  it("sums a publisher's skills into one share of the board", () => {
    const [first, second] = publishers(rows);
    expect(first).toMatchObject({ source: "acme/pack", installs: 80, skills: 2, share: 0.8 });
    expect(second).toMatchObject({ source: "globex/pack", installs: 20, skills: 1 });
  });

  /** The hot board's counts can all be zero early in the day. */
  it("gives every share as zero rather than NaN when nothing was installed", () => {
    const quiet = publishers([skill({ installs: 0 }), skill({ id: "b", installs: 0 })]);
    expect(quiet.every((p) => p.share === 0)).toBe(true);
  });
});
