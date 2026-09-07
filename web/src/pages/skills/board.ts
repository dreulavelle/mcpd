import snapshot from "@/data/skills.json";

/**
 * The Skills boards, and the arithmetic the page draws.
 *
 * The numbers come from a snapshot committed to this repository rather than a
 * live call -- see `web/scripts/fetch-skills.mjs` for why. Everything here is
 * pure, so the sums and the trend are testable without rendering anything.
 */

/**
 * Which ranking. They count different windows and must not be conflated: an
 * install on `top` is one ever recorded, on `trending` one in the last day,
 * and on `hot` one since midnight.
 */
export type BoardKey = "top" | "trending" | "hot";

export interface Skill {
  /** The repository it is published from, e.g. "anthropics/skills". */
  source: string;
  id: string;
  name: string;
  /** Installs within this board's window. See BoardKey. */
  installs: number;
  /** Eight weekly totals, oldest first. Only the all-time board carries them. */
  weekly?: number[];
  /** Installs the previous day. Only the hot board carries it. */
  yesterday?: number;
  /** Today's installs less yesterday's. Only the hot board carries it. */
  change?: number;
  /** Published by the tool's own authors, as skills.sh records it. */
  official?: boolean;
}

interface Snapshot {
  captured: string;
  source: string;
  boards: Record<BoardKey, Skill[]>;
}

export const SKILLS = snapshot as Snapshot;

/**
 * How many rows a tier shows when nobody is searching. A search ignores these
 * and looks at the whole board -- the tier is about how much to browse, not
 * about how much is here.
 */
export const TIERS = [10, 100, 250] as const;
export type Tier = (typeof TIERS)[number];

/** What each board counts, in the page's own words. */
export const BOARDS: { key: BoardKey; label: string; counts: string }[] = [
  {
    key: "top",
    label: "All time",
    counts: "installs, all time",
  },
  {
    key: "trending",
    label: "Trending",
    counts: "installs in the last day",
  },
  {
    key: "hot",
    label: "Hot",
    counts: "installs so far today",
  },
];

/**
 * The change across the eight weeks, as a fraction: 0.2 is a fifth more.
 *
 * The second half against the first rather than the last week against the one
 * before it. Weekly totals swing hard -- a skill can halve and recover inside
 * a fortnight -- and a single-week comparison reports that noise as a trend.
 *
 * Null when there is no series, or when the earlier half is zero: a skill that
 * went from nothing to something has grown by no finite percentage, and
 * rendering that as a number invites "+Infinity%".
 */
export function trend(weekly: number[] | undefined): number | null {
  if (!weekly || weekly.length < 4) return null;
  // Halves of equal length. An odd series compared three weeks against two and
  // reported the extra week as growth.
  const half = Math.floor(weekly.length / 2);
  const even = weekly.slice(weekly.length - half * 2);
  const earlier = even.slice(0, half).reduce((a, b) => a + b, 0);
  const later = even.slice(half).reduce((a, b) => a + b, 0);
  if (earlier === 0) return null;
  return (later - earlier) / earlier;
}

/** One skill and how far it moved across the eight weeks. */
export interface Mover {
  skill: Skill;
  /** The fraction `trend` returned, so the caller need not recompute it. */
  change: number;
}

/**
 * The steepest climbs and the steepest falls.
 *
 * The board is ranked by size, which is exactly what hides movement: a skill
 * doubling its weekly installs sits wherever its all-time total puts it, and
 * on a list of 250 nobody finds it. This is the question the table cannot
 * answer, which is why it earns the space at the top.
 *
 * Rows carrying no series are skipped rather than scored as flat -- only the
 * all-time board has one, and a zero would rank a skill with no history above
 * a real decline.
 */
export function movers(rows: Skill[], count = 3): { climbing: Mover[]; falling: Mover[] } {
  const scored: Mover[] = [];
  for (const skill of rows) {
    const change = trend(skill.weekly);
    if (change !== null) scored.push({ skill, change });
  }
  scored.sort((a, b) => b.change - a.change);

  // Split on the sign, not on position. Slicing the head and tail of a sorted
  // list fills a column called Falling with whatever is least positive, so a
  // week where everything grew printed "+10%" in red under that heading.
  const up = scored.filter((m) => m.change > 0);
  const down = scored.filter((m) => m.change < 0);
  return {
    climbing: up.slice(0, count),
    falling: down.slice(-count).reverse(),
  };
}

/** One publisher's share of a board. */
export interface Publisher {
  source: string;
  /** Installs summed across every skill this publisher has on the board. */
  installs: number;
  /** How many of the board's rows are theirs. */
  skills: number;
  /** Their fraction of the board's installs, 0 to 1. */
  share: number;
}

/**
 * Who the board's installs actually went to.
 *
 * The list ranks skills, so it cannot show that one publisher holds a fifth of
 * a board across thirty-seven near-identical entries -- you would have to
 * notice the same name thirty-seven times. This is the day boards' equivalent
 * of the movers panel: the thing a ranked list of individuals structurally
 * hides.
 */
export function publishers(rows: Skill[], count = 5): Publisher[] {
  const totals = new Map<string, { installs: number; skills: number }>();
  let all = 0;
  for (const row of rows) {
    const at = totals.get(row.source) ?? { installs: 0, skills: 0 };
    at.installs += row.installs;
    at.skills += 1;
    totals.set(row.source, at);
    all += row.installs;
  }

  return [...totals]
    .map(([source, t]) => ({ source, ...t, share: all === 0 ? 0 : t.installs / all }))
    .sort((a, b) => b.installs - a.installs)
    .slice(0, count);
}

/** Case-insensitive match on the name or the repository it comes from. */
export function search(rows: Skill[], query: string): Skill[] {
  const needle = query.trim().toLowerCase();
  if (needle === "") return rows;
  return rows.filter(
    (r) => r.name.toLowerCase().includes(needle) || r.source.toLowerCase().includes(needle),
  );
}

/**
 * The command that installs one, or null when there is no honest one to give.
 *
 * `skills add` takes a repository and picks the skill out of it with --skill;
 * it does not take `owner/repo/skill`, which is neither of its accepted forms
 * and installs nothing. Verified against skills.sh's own copy button and the
 * CLI's help at skills@1.5.24.
 *
 * A publisher without a slash is not a repository at all -- some rows are
 * published from a bare domain -- and there is no shorthand that reaches those,
 * so they get the link and no command rather than one that fails on paste.
 */
export function installCommand(skill: Skill): string | null {
  if (!skill.source.includes("/")) return null;
  return `npx skills add ${skill.source} --skill ${skill.id}`;
}

/**
 * Where the skill is described in full.
 *
 * The publisher and the skill sit directly under the origin. A `/skills`
 * segment in front of them answers 200 with the site's front page, so a wrong
 * link here looks like a working one.
 */
export function skillUrl(skill: Skill): string {
  return `${SKILLS.source}/${skill.source}/${skill.id}`;
}

/** A rounded, readable count: 3,293,855 becomes "3.3M". */
export function compact(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 10_000) return `${Math.round(n / 1000)}K`;
  if (n >= 1_000) return `${(n / 1000).toFixed(1)}K`;
  return String(n);
}

/**
 * An SVG path through a series, scaled to fill the box.
 *
 * A flat series would divide by zero, so it draws down the middle instead of
 * pinning to the floor -- a straight line through the centre reads as "no
 * change", which is what it is.
 */
export function linePath(series: number[], width: number, height: number): string {
  if (series.length < 2) return "";
  const low = Math.min(...series);
  const high = Math.max(...series);
  const span = high - low;
  const step = width / (series.length - 1);
  return series
    .map((value, i) => {
      const y = span === 0 ? height / 2 : height - ((value - low) / span) * height;
      return `${i === 0 ? "M" : "L"}${(i * step).toFixed(1)} ${y.toFixed(1)}`;
    })
    .join(" ");
}
