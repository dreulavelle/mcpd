import type { ReactNode } from "react";
import { Unlink } from "lucide-react";
import { whenExact } from "@/lib/format";
import { cn } from "@/lib/utils";
import { type Tone } from "@/components/status";

/**
 * A dated rail with one mark per entry.
 *
 * The rail is drawn per entry rather than once behind the list, because the
 * one thing it has to be able to say is that the link between two entries does
 * not hold -- and a single line behind everything has nowhere to break. Each
 * entry owns the segment below its own mark, so the break is rendered exactly
 * where the check found it.
 */

/** Where the rail sits inside its column, in both the grid and the offsets. */
const RAIL_COLUMN = "grid-cols-[1.75rem_minmax(0,1fr)] sm:grid-cols-[2rem_minmax(0,1fr)]";

export function Timeline({ children }: { children: ReactNode }) {
  return <div className="mt-2">{children}</div>;
}

/**
 * One day's entries under a heading that stays put while they scroll.
 *
 * The heading is what turns a list of timestamps into a timeline: a reader
 * asking "what happened on Tuesday" scrolls to a word rather than reading
 * dates off every row.
 */
export function TimelineDay({ label, count, children }: {
  label: string;
  count: number;
  children: ReactNode;
}) {
  return (
    <section>
      {/* Under the phone's own header rather than behind it. */}
      <h2 className="sticky top-14 z-10 -mx-1 flex items-baseline gap-3 bg-background/95 px-1 py-2 backdrop-blur-sm lg:top-0">
        <span className="text-sm font-medium">{label}</span>
        <span className="text-xs text-muted-foreground">
          {count} {count === 1 ? "entry" : "entries"}
        </span>
        <span aria-hidden="true" className="h-px flex-1 bg-border-soft" />
      </h2>
      <ol className="mt-1">{children}</ol>
    </section>
  );
}

/**
 * One entry on the rail.
 *
 * `mark` is who did it, so the eye can follow one person down a busy day
 * without reading a name on every line. `muted` is for an entry nobody did --
 * housekeeping mcpd performs on a schedule -- which belongs in the record but
 * not in the foreground.
 */
export function TimelineItem({ mark, severed = false, muted = false, last = false, children }: {
  mark: ReactNode;
  /** The chain does not hold between this entry and the older one below it. */
  severed?: boolean;
  muted?: boolean;
  last?: boolean;
  children: ReactNode;
}) {
  return (
    <li className={cn("grid gap-x-3", RAIL_COLUMN)}>
      <div className="relative flex justify-center" aria-hidden="true">
        {!last && (
          <span className="absolute top-4 bottom-0 left-1/2 w-px -translate-x-1/2 bg-border" />
        )}
        {/*
          On the page's own ground, so the line reads as passing behind it.

          A break is drawn on the mark rather than on the segment beneath it. A
          change is one card at its newest entry's position, so the item below
          any given one is not reliably the record's previous entry, and a
          dashed length of rail between them would be pointing at a neighbour
          it cannot vouch for. The ring names the entry, which is the thing the
          check actually found.
        */}
        <span
          className={cn(
            "relative rounded-full bg-background p-0.5",
            severed && "ring-2 ring-problem",
            muted && "opacity-70",
          )}
        >
          {mark}
        </span>
      </div>
      <div className={cn("min-w-0 pb-5", muted && "opacity-75")}>{children}</div>
    </li>
  );
}

/**
 * The sentence itself: who, then what they did.
 *
 * The actor carries the only weight on the line. Everything after it is the
 * same size and colour as the rest of the page, because an entry that shouts
 * is an entry the next one has to shout over.
 */
export function TimelineSentence({ actor, children, at, badges }: {
  actor: string;
  children: ReactNode;
  at: string;
  badges?: ReactNode;
}) {
  return (
    <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
      <p className="min-w-0 flex-1 text-sm leading-6">
        <span className="font-medium">{actor}</span>{" "}
        {children}
      </p>
      {badges}
      <TimelineTime at={at} />
    </div>
  );
}

/** The time, exact on hover, in the same column down the whole page. */
export function TimelineTime({ at, className }: { at: string; className?: string }) {
  const d = new Date(at);
  const shown = Number.isNaN(d.getTime())
    ? at
    : d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
  return (
    <time
      dateTime={at}
      title={whenExact(at)}
      className={cn(
        "shrink-0 text-xs whitespace-nowrap text-muted-foreground tabular-nums",
        className,
      )}
    >
      {shown}
    </time>
  );
}

/**
 * The short facts under a sentence: a reason in quotes, a reach, an expiry.
 *
 * Fragments rather than clauses, because these are read by scanning and not by
 * reading -- and a fragment cannot pretend to be prose the way half a sentence
 * can.
 */
export function TimelineFacts({ mark, children }: { mark?: ReactNode; children: ReactNode }) {
  return (
    <p className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
      {mark}
      {children}
    </p>
  );
}

/**
 * A separator between two fragments on the facts line.
 *
 * It inherits the line's own colour rather than taking a fainter one: `--faint`
 * is not a text colour, and a middle dot is a character somebody reads past
 * rather than a mark they read.
 */
export function FactDot() {
  return <span aria-hidden="true">·</span>;
}

const STEP_DOT: Record<Tone, string> = {
  good: "bg-good",
  attention: "bg-attention",
  problem: "bg-problem",
  info: "bg-info",
  neutral: "bg-faint",
};

/**
 * One step of a change, on its own small rail inside the card.
 *
 * A change is five entries in the table and one thing that happened, so it is
 * drawn as one thing that happened. The steps keep their own times: how long a
 * change waited for a decision is a question somebody asks.
 */
export function ThreadStep({ tone = "neutral", at, severed = false, children, last = false }: {
  tone?: Tone;
  at?: string;
  /**
   * The chain does not hold at this step. Drawn here rather than on the card,
   * because a break inside a change is between two of its own entries and
   * saying so against the whole card would name the wrong neighbour.
   */
  severed?: boolean;
  children: ReactNode;
  last?: boolean;
}) {
  return (
    <li className="relative flex items-baseline gap-x-2 pb-2 pl-4 last:pb-0">
      <span aria-hidden="true" className="absolute top-0 bottom-0 left-[0.1875rem] w-px bg-border-soft" />
      {last && (
        <span aria-hidden="true" className="absolute top-2 bottom-0 left-[0.1875rem] w-px bg-card" />
      )}
      <span
        aria-hidden="true"
        className={cn(
          "absolute top-[0.4375rem] left-0 size-1.5 rounded-full ring-2 ring-card",
          severed ? "bg-problem" : STEP_DOT[tone],
        )}
      />
      <span className="min-w-0 flex-1 text-xs leading-5">
        {children}
        {severed && (
          <span className="mt-1 flex items-center gap-1.5 font-medium text-problem">
            <Unlink className="size-3.5 shrink-0" aria-hidden="true" />
            The entry at this point does not follow the one before it.
          </span>
        )}
      </span>
      {at && <TimelineTime at={at} />}
    </li>
  );
}

/** The steps of one change, under its heading. */
export function Thread({ children }: { children: ReactNode }) {
  return <ol className="mt-2.5">{children}</ol>;
}

const RULE_TONE: Record<Tone, string> = {
  good: "border-l-good",
  attention: "border-l-attention",
  problem: "border-l-problem",
  info: "border-l-info",
  neutral: "border-l-border",
};

/**
 * A change, raised off the rail.
 *
 * The one place on this page that carries a surface of its own. Most entries
 * are a line about an administrative act; a change is the thing this host
 * exists to gate, and the rhythm of quiet rows and the occasional raised card
 * is what makes a busy day readable. The left rule carries the outcome, so how
 * a change ended is legible before a word of it is read.
 */
export function ThreadCard({ tone = "neutral", children }: {
  tone?: Tone;
  children: ReactNode;
}) {
  return (
    <div
      className={cn(
        "rounded-lg border border-l-2 bg-card px-3 py-2.5 sm:px-4 sm:py-3",
        RULE_TONE[tone],
      )}
    >
      {children}
    </div>
  );
}

/**
 * The exact fields a change carries, as `was → now`.
 *
 * This is the difference between a reviewed change and a gated call, so it is
 * shown rather than folded into the sentence: a call that carried an
 * authorisation and nothing else has nothing to draw here.
 */
export function ChangeFields({ changes }: {
  changes: { field: string; from?: string; to?: string }[];
}) {
  return (
    <dl className="mt-2.5 grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-xs">
      {changes.map((c) => (
        <div key={c.field} className="col-span-2 grid grid-cols-subgrid items-baseline">
          <dt className="text-muted-foreground">{c.field}</dt>
          <dd className="min-w-0 font-mono break-words">
            {c.from !== undefined && c.from !== "" && (
              <>
                <span className="text-muted-foreground line-through">{c.from}</span>
                <span aria-hidden="true" className="px-1.5 text-muted-foreground">→</span>
              </>
            )}
            <span>{c.to === undefined || c.to === "" ? "—" : c.to}</span>
          </dd>
        </div>
      ))}
    </dl>
  );
}

/**
 * The break in the chain, drawn against the entry the check named.
 *
 * Tamper-evidence that is only a sentence at the top of the page is a claim.
 *
 * It says "the one before it" -- meaning the entry before it in the record --
 * and never "the one below it". A change is drawn as one card at its newest
 * entry's position, so what is below any given item on screen is not reliably
 * the record's own previous entry, and a note that pointed at the screen would
 * name the wrong neighbour. The marker stays on the exact entry; only the
 * spatial claim goes.
 */
export function SeveredNote() {
  return (
    <p className="mt-1.5 flex items-center gap-1.5 text-xs font-medium text-problem">
      <Unlink className="size-3.5 shrink-0" aria-hidden="true" />
      This entry does not follow the one before it.
    </p>
  );
}

/** Entries that were in the record between this one and the one before it. */
export function GapNote({ missing }: { missing: number }) {
  return (
    <p className="mt-1.5 text-xs text-muted-foreground">
      {missing} {missing === 1 ? "entry" : "entries"} between this one and the
      one before it {missing === 1 ? "has" : "have"} been removed.
    </p>
  );
}

/**
 * The record as it is stored, closed by default.
 *
 * Every identifier lives here and nowhere else: an id in a sentence is a
 * string a person has to decode before they can read the line it is in.
 */
export function RawEntry({ children }: { children: ReactNode }) {
  return (
    <details className="group mt-2">
      <summary className="inline-flex cursor-pointer list-none text-xs text-muted-foreground hover:text-foreground">
        Raw entry
      </summary>
      <div className="mt-1.5">{children}</div>
    </details>
  );
}
