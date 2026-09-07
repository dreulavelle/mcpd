import { useMemo, useState } from "react";
import { BadgeCheck, ExternalLink, Search } from "lucide-react";
import { Copyable, PageHeader } from "@/components/chrome";
import { Segmented } from "@/components/Segmented";
import { Input } from "@/components/ui/input";
import { day } from "@/lib/format";
import { useQueryParam } from "@/lib/router";
import { cn } from "@/lib/utils";
import {
  BOARDS, SKILLS, TIERS,
  type BoardKey, type Mover, type Publisher, type Skill, type Tier,
  compact, installCommand, movers, publishers, search, skillUrl, linePath,
  trend,
} from "./board";

/**
 * What the wider skills ecosystem is installing.
 *
 * Deliberately not the Marketplace's card grid. A marketplace answers "what
 * could I add", and its rows are things this host can reach; this answers
 * "what is everyone else using", where the interesting part is movement --
 * eight weeks of history per skill, and two boards that count only the last
 * day. So it leads with what moved rather than what is biggest, which a grid
 * of equal cards cannot show at all.
 *
 * The numbers are a snapshot in the bundle, never a live call. mcpd runs on
 * somebody else's hardware and this page is not worth reaching off it.
 */
export function Skills() {
  const [open, setOpen] = useState("");
  const [boardParam, setBoard] = useQueryParam("board");
  const [tierParam, setTier] = useQueryParam("show");
  const [query, setQuery] = useQueryParam("q");

  const board = BOARDS.find((b) => b.key === boardParam)?.key ?? "top";
  const tier = (TIERS as readonly number[]).includes(Number(tierParam))
    ? (Number(tierParam) as Tier)
    : 10;
  const meta = BOARDS.find((b) => b.key === board)!;

  const rows = SKILLS.boards[board];
  // The rank a skill holds on the board. Built once rather than scanning the
  // board for every rendered row on every keystroke.
  const ranks = useMemo(() => new Map(rows.map((r, i) => [r, i + 1])), [rows]);
  const shown = useMemo(
    () => (query.trim() ? search(rows, query) : rows.slice(0, tier)),
    [rows, query, tier],
  );

  const moving = useMemo(() => movers(rows), [rows]);
  const leading = useMemo(() => publishers(rows), [rows]);

  return (
    <>
      <PageHeader
        title="Skills"
        lede={`What the wider ecosystem is installing, as counted on ${day(SKILLS.captured)}. Nothing here runs on this host.`}
      />

      {board === "top"
        ? <Movers {...moving} />
        : <Leaders publishers={leading} />}

      <div className="mt-6 flex flex-wrap items-center gap-3">
        <Segmented
          label="Which ranking"
          value={board}
          onChange={(next: BoardKey) => setBoard(next === "top" ? "" : next)}
          options={BOARDS.map((b) => ({ value: b.key, label: b.label }))}
          size="md"
        />
        <Segmented
          label="How many to show"
          value={String(tier)}
          onChange={(next) => setTier(next === "10" ? "" : next)}
          options={TIERS.map((t) => ({ value: String(t), label: `Top ${t}` }))}
          size="md"
        />
        <div className="relative ml-auto w-full sm:w-64">
          <Search
            className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground"
            aria-hidden="true"
          />
          <Input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search name or publisher"
            aria-label="Search skills"
            className="pl-8"
          />
        </div>
      </div>

      {shown.length === 0 && query.trim() ? (
        <p className="mt-8 text-sm text-muted-foreground">
          No skills match “{query}”.
        </p>
      ) : (
        <ol className="mt-4 divide-y overflow-hidden rounded-lg border">
          {shown.map((skill) => {
            const key = `${skill.source}/${skill.id}`;
            return (
            <Row
              key={key}
              id={`skill-${key.replace(/[^a-zA-Z0-9]+/g, "-")}`}
              open={open === key}
              onToggle={() => setOpen(open === key ? "" : key)}
              skill={skill}
              // The rank it holds on the board, which is not its position in
              // a filtered list -- a search must not renumber the leaderboard.
              rank={ranks.get(skill) ?? 0}
              board={board}
              counts={meta.counts}
            />
            );
          })}
        </ol>
      )}

    </>
  );
}

/**
 * The steepest climbs and falls over the eight weeks.
 *
 * The page's one loud element, and the only thing here the ranked list cannot
 * say: sorted by size, a skill that doubled sits wherever its all-time total
 * puts it. Everything below this is deliberately quiet.
 */
function Movers({ climbing, falling }: { climbing: Mover[]; falling: Mover[] }) {
  return (
    <div className="mt-6 grid gap-px overflow-hidden rounded-lg border bg-border sm:grid-cols-2">
      <MoverColumn title="Climbing" movers={climbing} rising />
      <MoverColumn title="Falling" movers={falling} rising={false} />
    </div>
  );
}

function MoverColumn({ title, movers, rising }: {
  title: string;
  movers: Mover[];
  rising: boolean;
}) {
  return (
    <section className="bg-card p-4">
      <h2 className="text-sm font-medium">
        {title}{" "}
        <span className="font-normal text-muted-foreground">over eight weeks</span>
      </h2>
      <ul className="mt-3 space-y-2.5">
        {movers.map(({ skill, change }) => (
          <li key={`${skill.source}/${skill.id}`} className="flex items-center gap-3">
            <span className="min-w-0 flex-1">
              <span className="block truncate text-sm">{skill.name}</span>
              <span className="block truncate font-mono text-xs text-muted-foreground">
                {skill.source}
              </span>
            </span>
            <Trendline weekly={skill.weekly} rising={rising} />
            <span
              className={cn(
                "w-14 shrink-0 text-right text-sm font-medium tabular-nums",
                rising ? "text-good" : "text-problem",
              )}
            >
              {change >= 0 ? "+" : "−"}
              {Math.abs(Math.round(change * 100))}%
            </span>
          </li>
        ))}
      </ul>
    </section>
  );
}

/**
 * Who a day board's installs actually went to.
 *
 * The movers panel's counterpart for the two boards carrying no history. Both
 * answer the same kind of question -- the one a ranked list of individual
 * skills structurally cannot, because the pattern is spread across the rows
 * rather than sitting in any one of them. One publisher holding a fifth of a
 * board across thirty-seven near-identical entries is invisible until the rows
 * are added up.
 *
 * Resist adding "today against yesterday" here. A row is on the hot board
 * because it grew, so summing those rows against their own previous day and
 * calling the result a trend measures the selection and nothing else: the
 * board reports every one of its five hundred rising and none falling.
 */
function Leaders({ publishers }: { publishers: Publisher[] }) {
  return (
    <section className="mt-6 rounded-lg border bg-card p-4">
      <h2 className="text-sm font-medium">Top Publishers</h2>

      {/* The numbers meant nothing unheaded: a count and a percentage sat
          together at the end of a row with no word between them. */}
      <div className="mt-3 flex items-center gap-3 border-b pb-1.5 text-xs text-muted-foreground">
        <span className="w-48 shrink-0">Publisher</span>
        <span className="hidden w-16 shrink-0 sm:block">On board</span>
        <span className="flex-1" />
        <span className="w-16 shrink-0 text-right">Installs</span>
        <span className="w-12 shrink-0 text-right">Share</span>
      </div>

      <ul className="mt-2 space-y-2">
        {publishers.map((p) => (
          <li key={p.source} className="flex items-center gap-3">
            <span className="w-48 shrink-0 truncate font-mono text-xs" title={p.source}>
              {p.source}
            </span>
            <span className="hidden w-16 shrink-0 text-xs text-muted-foreground tabular-nums sm:block">
              {p.skills} {p.skills === 1 ? "skill" : "skills"}
            </span>
            <span className="h-2 flex-1 overflow-hidden rounded-full bg-muted">
              <span
                className="block h-full rounded-full bg-[var(--chart-1)]"
                style={{ width: `${Math.max(1, p.share * 100).toFixed(1)}%` }}
              />
            </span>
            <span className="w-16 shrink-0 text-right text-sm tabular-nums">
              {compact(p.installs)}
            </span>
            <span className="w-12 shrink-0 text-right text-sm text-muted-foreground tabular-nums">
              {Math.round(p.share * 100)}%
            </span>
          </li>
        ))}
      </ul>
    </section>
  );
}

/** One skill, which opens to show how to install it. */
function Row({ skill, rank, board, counts, open, onToggle, id }: {
  skill: Skill;
  rank: number;
  board: BoardKey;
  counts: string;
  open: boolean;
  onToggle: () => void;
  /** Ties the button to the panel it opens, for anything reading the page. */
  id: string;
}) {
  const movement = trend(skill.weekly);

  return (
    <li className="bg-card">
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        aria-controls={`${id}-detail`}
        className={cn(
          "flex w-full items-center gap-3 px-3 py-2.5 text-left outline-none",
          "hover:bg-muted/50 focus-visible:ring-[3px] focus-visible:ring-ring/50",
        )}
      >
        <span className="w-7 shrink-0 text-right font-mono text-xs text-muted-foreground tabular-nums">
          {rank}
        </span>

        <span className="min-w-0 flex-1">
          <span className="flex items-center gap-1.5">
            <span className="truncate text-sm font-medium">{skill.name}</span>
            {skill.official && (
              <BadgeCheck
                className="size-3.5 shrink-0 text-info"
                aria-label="Published by the tool's own authors"
              />
            )}
          </span>
          <span className="block truncate font-mono text-xs text-muted-foreground">
            {skill.source}
          </span>
        </span>

        {board === "top" && (
          <Trendline weekly={skill.weekly} rising={movement !== null && movement >= 0} />
        )}

        <span className="w-24 shrink-0 text-right">
          {/* The hot board is ordered by the day's change, so that is the
              number the column shows: printing installs there gives a ranked
              list whose figures do not descend. */}
          <span className="block text-sm font-medium tabular-nums">
            {board === "hot" && skill.change !== undefined
              ? `${skill.change >= 0 ? "+" : "−"}${Math.abs(skill.change).toLocaleString()}`
              : skill.installs.toLocaleString()}
          </span>
          {board === "top" && movement !== null && (
            <span
              className={cn(
                "block text-xs tabular-nums",
                movement >= 0 ? "text-good" : "text-problem",
              )}
            >
              {movement >= 0 ? "+" : "−"}
              {Math.abs(Math.round(movement * 100))}% over eight weeks
            </span>
          )}
          {board === "hot" && skill.yesterday !== undefined && (
            <span className="block text-xs text-muted-foreground tabular-nums">
              {skill.yesterday === 0
                ? `${skill.installs.toLocaleString()} today, new`
                : `${skill.installs.toLocaleString()} today, ${skill.yesterday.toLocaleString()} yesterday`}
            </span>
          )}
        </span>
      </button>

      {open && <Detail skill={skill} counts={counts} id={`${id}-detail`} />}
    </li>
  );
}

/**
 * Eight weeks in eighty pixels.
 *
 * Drawn in the same pair as the percentage beside it rather than in the chart
 * series colours: those were chosen to stay legible as filled areas, and two
 * different greens in one row read as two different meanings.
 */
function Trendline({ weekly, rising }: { weekly: number[] | undefined; rising: boolean }) {
  if (!weekly || weekly.length < 2) return <span className="hidden w-20 sm:block" />;
  return (
    <svg
      viewBox="0 0 80 22"
      className="hidden h-[22px] w-20 shrink-0 sm:block"
      aria-hidden="true"
    >
      <path
        d={linePath(weekly, 80, 22)}
        fill="none"
        stroke={rising ? "var(--good)" : "var(--problem)"}
        strokeWidth="1.5"
        strokeLinejoin="round"
        strokeLinecap="round"
      />
    </svg>
  );
}

/** What the row opens to: how to install it, and where to read more. */
function Detail({ skill, counts, id }: { skill: Skill; counts: string; id: string }) {
  const command = installCommand(skill);

  return (
    <div id={id} className="border-t bg-muted/30 px-3 py-3 pl-13">
      <p className="text-xs text-muted-foreground">
        {skill.installs.toLocaleString()} {counts}.
      </p>

      {command
        ? <Copyable value={command} label="the install command" className="mt-2" />
        : (
          // Some rows are published from a bare domain rather than a
          // repository, and `skills add` has no form that reaches those.
          <p className="mt-2 text-xs text-muted-foreground">
            Published from {skill.source} rather than a repository, so there is
            no one-line install for it.
          </p>
        )}

      <a
        href={skillUrl(skill)}
        target="_blank"
        rel="noreferrer noopener"
        className={cn(
          "mt-2 inline-flex items-center gap-1.5 rounded-md border px-2.5 py-1.5 text-xs font-medium",
          "hover:bg-muted focus-visible:ring-[3px] focus-visible:ring-ring/50 outline-none",
        )}
      >
        <ExternalLink className="size-3.5" aria-hidden="true" />
        Read about it
      </a>
    </div>
  );
}
