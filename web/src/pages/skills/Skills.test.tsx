import { describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWith } from "@/test/render";
import type { Skill } from "./board";

/**
 * The page reads a snapshot that a scheduled job rewrites. Asserting against
 * whatever it holds today would mean a refresh of the data breaking the build,
 * so the fixture is the test's own and only the arithmetic and the wiring are
 * under test here.
 */
const top: Skill[] = Array.from({ length: 12 }, (_, i) => ({
  source: i === 0 ? "anthropics/skills" : `acme/pack-${i}`,
  id: `skill-${i}`,
  name: `skill-${i}`,
  installs: 12_000 - i * 500,
  // Ranked by installs, so movement has to cut across that order for the
  // movers panel to be worth anything: skill-9 is small and climbing, skill-1
  // is large and falling.
  weekly: i === 9 ? [10, 10, 10, 10, 90, 90, 90, 90]
    : i === 1 ? [90, 90, 90, 90, 10, 10, 10, 10]
    : [10, 10, 10, 10, 11, 11, 11, 11],
  ...(i === 0 ? { official: true } : {}),
}));

vi.mock("./board", async () => {
  const actual = await vi.importActual<typeof import("./board")>("./board");
  return {
    ...actual,
    SKILLS: {
      captured: "2026-09-07",
      source: "https://www.skills.sh",
      boards: {
        top,
        trending: [{ source: "acme/pack", id: "climber", name: "climber", installs: 900 }],
        hot: [
          { source: "acme/pack", id: "sudden", name: "sudden", installs: 286, yesterday: 40, change: 246 },
          { source: "globex/pack", id: "fresh", name: "fresh", installs: 14, yesterday: 0, change: 14 },
        ],
      },
    },
  };
});

const { Skills } = await import("./Skills");

/** Each skill is a button that opens; the segmented controls are radios. */
function rows() {
  return screen.getAllByRole("button", { expanded: false });
}

describe("the skills page", () => {
  /**
   * The rank is the skill's place on the board, not its position in whatever
   * is currently on screen. Numbering the filtered list instead tells somebody
   * who searched that their result is the most-installed skill there is.
   */
  it("keeps a skill's board rank when a search narrows the list", async () => {
    renderWith(<Skills />, { path: "/skills" });
    expect(rows()).toHaveLength(10);

    await userEvent.type(screen.getByLabelText("Search skills"), "skill-4");
    const only = rows();
    expect(only).toHaveLength(1);
    expect(within(only[0]!).getByText("5")).toBeInTheDocument();
  });

  it("opens on the shortest tier, and widens to the one chosen", async () => {
    renderWith(<Skills />, { path: "/skills" });
    expect(rows()).toHaveLength(10);

    await userEvent.click(screen.getByRole("radio", { name: "Top 100" }));
    expect(rows()).toHaveLength(12);
  });

  /**
   * The tier decides how much to browse, not how much is here. Filtering the
   * tier first means typing the name of a skill that is on the board and
   * being told nothing matches it.
   */
  it("searches past the tier, into the whole board", async () => {
    renderWith(<Skills />, { path: "/skills" });
    expect(rows()).toHaveLength(10);

    // skill-11 sits outside the ten on screen.
    await userEvent.type(screen.getByLabelText("Search skills"), "skill-11");
    const found = rows();
    expect(found).toHaveLength(1);
    expect(within(found[0]!).getByText("skill-11")).toBeInTheDocument();
  });

  /**
   * Switching board used to leave the top of the page empty, because only the
   * all-time board carries the history the movers panel needs. Each board
   * shows the thing its own data can answer instead.
   */
  it("keeps a panel at the top on every board", async () => {
    renderWith(<Skills />, { path: "/skills" });
    expect(screen.getByRole("heading", { name: /Climbing/ })).toBeInTheDocument();

    await userEvent.click(screen.getByRole("radio", { name: "Trending" }));
    expect(screen.getByRole("heading", { name: "Top Publishers" })).toBeInTheDocument();

    await userEvent.click(screen.getByRole("radio", { name: "Hot" }));
    expect(screen.getByRole("heading", { name: "Top Publishers" })).toBeInTheDocument();
  });

  /** The counts at the end of a publisher row meant nothing unheaded. */
  it("heads the publisher columns", async () => {
    renderWith(<Skills />, { path: "/skills" });
    await userEvent.click(screen.getByRole("radio", { name: "Hot" }));
    for (const head of ["Publisher", "On board", "Installs", "Share"]) {
      expect(screen.getByText(head)).toBeInTheDocument();
    }
  });

  /** A row that did not exist yesterday reads better than "0 yesterday". */
  it("names a hot row that came from nothing", async () => {
    renderWith(<Skills />, { path: "/skills" });
    await userEvent.click(screen.getByRole("radio", { name: "Hot" }));
    expect(screen.getByText("new today")).toBeInTheDocument();
    expect(screen.getByText("40 yesterday")).toBeInTheDocument();
  });

  it("says plainly when a search matches nothing", async () => {
    renderWith(<Skills />, { path: "/skills" });
    await userEvent.type(screen.getByLabelText("Search skills"), "wordpress");
    expect(screen.getByText(/No skills match/)).toBeInTheDocument();
  });

  /**
   * The three boards count different windows -- ever, the last day, and since
   * midnight -- and the page must say which, because the same column of
   * numbers means something different on each.
   */
  it("names the window each board counts", async () => {
    renderWith(<Skills />, { path: "/skills" });

    await userEvent.click(screen.getByRole("radio", { name: "Hot" }));
    expect(screen.getByText("40 yesterday")).toBeInTheDocument();
    await userEvent.click(rows()[0]!);
    expect(screen.getByText(/286 installs so far today/)).toBeInTheDocument();

    await userEvent.click(screen.getByRole("radio", { name: "Trending" }));
    await userEvent.click(rows()[0]!);
    expect(screen.getByText(/900 installs in the last day/)).toBeInTheDocument();
  });

  it("opens a row onto the command that installs it", async () => {
    renderWith(<Skills />, { path: "/skills" });
    await userEvent.click(rows()[0]!);
    expect(screen.getByText("npx skills add anthropics/skills/skill-0")).toBeInTheDocument();
  });

  /**
   * The lede promises the page calls nothing from this host, which is the
   * whole reason the ranking is a committed snapshot rather than a fetch. A
   * later edit that "just refreshes it live" breaks that promise silently.
   */
  it("renders without reaching the network", () => {
    const fetched = vi.spyOn(globalThis, "fetch");
    renderWith(<Skills />, { path: "/skills" });
    expect(screen.getByRole("heading", { name: "Skills" })).toBeInTheDocument();
    expect(fetched).not.toHaveBeenCalled();
  });

  /**
   * The list is ranked by size, so a small skill climbing hard is invisible
   * in it. That is the whole reason the panel is there.
   */
  it("surfaces movement the ranked list buries", () => {
    renderWith(<Skills />, { path: "/skills" });

    const climbing = screen.getByRole("heading", { name: /Climbing/ }).parentElement!;
    expect(within(climbing).getByText("skill-9")).toBeInTheDocument();

    const falling = screen.getByRole("heading", { name: /Falling/ }).parentElement!;
    expect(within(falling).getByText("skill-1")).toBeInTheDocument();
  });

  /**
   * Two install commands on screen at once is two things to copy and no way
   * to tell which row either belongs to.
   */
  it("closes the open row when another is opened", async () => {
    renderWith(<Skills />, { path: "/skills" });

    await userEvent.click(rows()[0]!);
    expect(screen.getByText("npx skills add anthropics/skills/skill-0")).toBeInTheDocument();

    await userEvent.click(rows()[0]!);
    expect(screen.getAllByText(/^npx skills add/)).toHaveLength(1);
    expect(screen.getByText("npx skills add acme/pack-1/skill-1")).toBeInTheDocument();
  });

  it("closes a row when it is clicked again", async () => {
    renderWith(<Skills />, { path: "/skills" });
    const first = rows()[0]!;
    await userEvent.click(first);
    await userEvent.click(screen.getAllByRole("button", { expanded: true })[0]!);
    expect(screen.queryByText(/^npx skills add/)).not.toBeInTheDocument();
  });

});
