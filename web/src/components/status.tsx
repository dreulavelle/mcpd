import type { ReactNode } from "react";
import { CircleHelp } from "lucide-react";
import type { OperationState, RiskLevel } from "@/lib/api";
import { OPERATION_STATES, RISK_LABELS, stateLabel } from "@/lib/format";
import { cn } from "@/lib/utils";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";

/**
 * Semantic status, kept separate from the accent.
 *
 * "neutral" is a real value and not a fallback: several things in this console
 * are genuinely neither good nor bad -- an operation nobody has decided on, a
 * check nobody has run -- and colouring those in would be saying something
 * untrue about them.
 */
export type Tone = "good" | "attention" | "problem" | "info" | "neutral";

const TONE_CHIP: Record<Tone, string> = {
  good: "bg-good-soft text-good border-good/25",
  attention: "bg-attention-soft text-attention border-attention/25",
  problem: "bg-problem-soft text-problem border-problem/25",
  info: "bg-info-soft text-info border-info/25",
  neutral: "bg-muted text-muted-foreground border-border",
};

const TONE_DOT: Record<Tone, string> = {
  good: "bg-good",
  attention: "bg-attention",
  problem: "bg-problem",
  info: "bg-info",
  neutral: "bg-faint",
};

export function StatusDot({ tone, className }: { tone: Tone; className?: string }) {
  return (
    <span
      aria-hidden="true"
      className={cn("inline-block size-2 shrink-0 rounded-full", TONE_DOT[tone], className)}
    />
  );
}

export function Chip({ tone = "neutral", className, children }: {
  tone?: Tone;
  className?: string;
  children: ReactNode;
}) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5",
        "text-xs font-medium whitespace-nowrap",
        TONE_CHIP[tone], className,
      )}
    >
      {children}
    </span>
  );
}

/* -- operations ------------------------------------------------------------ */

/**
 * The tone of each operation state.
 *
 * `indeterminate` is "attention" and never "problem". It is not a failure --
 * it means execution began and the outcome was never recorded, so the change
 * may be in place. Painting it the same as `failed` is what leads somebody to
 * retry and apply it twice.
 */
const STATE_TONE: Record<OperationState, Tone> = {
  draft: "neutral",
  pending_approval: "info",
  approved: "info",
  executing: "info",
  succeeded: "good",
  failed: "problem",
  indeterminate: "attention",
  rejected: "neutral",
  expired: "neutral",
  cancelled: "neutral",
};

export function stateTone(state: OperationState | string): Tone {
  return STATE_TONE[state as OperationState] ?? "neutral";
}

/** An operation's state, with what it means available on hover. */
export function StateBadge({ state }: { state: OperationState | string }) {
  const meaning = OPERATION_STATES[state as OperationState]?.meaning;
  const chip = (
    <Chip tone={stateTone(state)}>
      <StatusDot tone={stateTone(state)} />
      {stateLabel(state)}
    </Chip>
  );
  if (!meaning) return chip;
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span tabIndex={0} className="rounded-full">{chip}</span>
      </TooltipTrigger>
      <TooltipContent className="max-w-xs">{meaning}</TooltipContent>
    </Tooltip>
  );
}

const RISK_TONE: Record<RiskLevel, Tone> = {
  low: "neutral", medium: "info", high: "attention", critical: "problem",
};

export function RiskBadge({ risk }: { risk: RiskLevel | string }) {
  const tone = RISK_TONE[risk as RiskLevel] ?? "neutral";
  return <Chip tone={tone}>{RISK_LABELS[risk as RiskLevel] ?? risk}</Chip>;
}

/* -- verification ---------------------------------------------------------- */

/**
 * Whether re-reading upstream confirmed the change.
 *
 * Three outcomes, and the third is the one that matters. `true` was confirmed,
 * `false` was checked and did not match, and null or absent means nobody has
 * checked -- which is the ordinary state of anything still in flight, and is
 * common enough that rendering it as a tick would be wrong most of the time it
 * appeared.
 *
 * `verified` is typed `boolean | null | undefined` so a call site cannot narrow
 * it to a boolean without deciding what to do about the third case.
 */
export function VerifiedBadge({ verified }: { verified?: boolean | null }) {
  if (verified === true) {
    return (
      <Chip tone="good">
        <StatusDot tone="good" />
        Verified
      </Chip>
    );
  }
  if (verified === false) {
    return (
      <Tooltip>
        <TooltipTrigger asChild>
          <span tabIndex={0} className="rounded-full">
            <Chip tone="problem">
              <StatusDot tone="problem" />
              Did not match
            </Chip>
          </span>
        </TooltipTrigger>
        <TooltipContent className="max-w-xs">
          mcpd re-read the target afterwards and what it found was not what was
          asked for.
        </TooltipContent>
      </Tooltip>
    );
  }
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span tabIndex={0} className="rounded-full">
          <Chip tone="neutral">
            <CircleHelp className="size-3" aria-hidden="true" />
            Not checked
          </Chip>
        </span>
      </TooltipTrigger>
      <TooltipContent className="max-w-xs">
        Nobody has re-read the target, so this says nothing either way about
        whether the change is in place.
      </TooltipContent>
    </Tooltip>
  );
}

/* -- assurance ------------------------------------------------------------- */

/**
 * What the record proves, in one chip.
 *
 * "Reviewed change" and "gated call" are different words on purpose: the first
 * carries exact fields, drift detection and a confirmed outcome, the second a
 * person's yes and nothing else. Neither is a fault -- a gated call is a
 * legitimate, ordinary thing -- so neither is coloured as one. The distinction
 * is worth stating precisely because it is easy to read the smaller guarantee
 * as the larger one.
 */
export function AssuranceBadge({ assurance }: { assurance: string }) {
  const reviewed = assurance === "reviewed_change";
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span tabIndex={0} className="rounded-full">
          <Chip tone={reviewed ? "good" : "neutral"}>
            {reviewed ? "Reviewed change" : "Gated call"}
          </Chip>
        </span>
      </TooltipTrigger>
      <TooltipContent className="max-w-xs">
        {reviewed
          ? "Exact fields, a drift check against a stored snapshot, and an outcome confirmed by re-reading the target."
          : "A person authorised it and the call was made. That is all this record proves — it does not say the change is in place."}
      </TooltipContent>
    </Tooltip>
  );
}

/* -- health ---------------------------------------------------------------- */

export function healthTone(health: string): Tone {
  switch (health) {
    case "healthy": case "up": return "good";
    case "degraded": return "attention";
    default: return "problem";
  }
}
