import type { ReactNode } from "react";
import { CircleHelp, ShieldCheck } from "lucide-react";
import type { OperationState, RiskLevel } from "@/lib/api";
import { OPERATION_STATES, RISK_LABELS, stateLabel } from "@/lib/format";
import { cn } from "@/lib/utils";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";

/**
 * Semantic status. "neutral" is a real value, not a fallback: a check nobody
 * has run is neither good nor bad, and colouring it would say otherwise.
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

export function Chip({ tone = "neutral", className, title, children }: {
  tone?: Tone;
  className?: string;
  /** Evidence a chip's words stand in for -- an HTTP status, say. */
  title?: string;
  children: ReactNode;
}) {
  return (
    <span
      title={title}
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
 * `indeterminate` is "attention" and never "problem": the change may be in
 * place, and painting it as a failure invites a retry that applies it twice.
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
 * Three outcomes, and the third is the one that matters: null or absent is
 * "nobody checked" and must never render as a tick. Typed to include null so a
 * call site cannot narrow it to a boolean without deciding.
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
          The system was read again afterwards, and it did not show the change
          that was asked for.
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
        Nobody has read the system again, so this does not say whether the
        change is in place.
      </TooltipContent>
    </Tooltip>
  );
}

/* -- assurance ------------------------------------------------------------- */

/**
 * What the record proves. Different words on purpose, and neither is coloured
 * as a fault: a gated call is legitimate, it is just not the larger guarantee.
 */
export function AssuranceBadge({ assurance, authorizedByRule }: {
  assurance: string;
  /** Changes the sentence, never the badge, which would claim a person said yes. */
  authorizedByRule?: string;
}) {
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
          ? "The exact change was recorded, checked against how the system looked beforehand, and confirmed afterwards."
          : authorizedByRule
            ? "A rule allowed it and the call was made. This does not say whether the change is in place."
            : "A person allowed it and the call was made. This does not say whether the change is in place."}
      </TooltipContent>
    </Tooltip>
  );
}

/* -- authorisation --------------------------------------------------------- */

/**
 * A change a rule approved, with nobody asked. `authorized_by_rule` is the
 * discriminator, never `approved_by` -- that is `system:policy`, not an account.
 */
export function AuthorisedByRule({ rule }: { rule: string }) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span tabIndex={0} className="rounded-full">
          <Chip tone="info">
            <ShieldCheck className="size-3 shrink-0" aria-hidden="true" />
            <span>
              No one was asked — rule <code className="font-mono">{rule}</code>
            </span>
          </Chip>
        </span>
      </TooltipTrigger>
      <TooltipContent className="max-w-xs">
        A rule allowed this kind of change, so it was approved without going to
        a person.
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

/**
 * A health state as a word somebody would say out loud. "unhealthy" and
 * "degraded" are the API's vocabulary, not a person's; a state this build does
 * not know renders as itself, which is the signal the two have drifted apart.
 */
export function healthWords(health: string): string {
  switch (health) {
    case "healthy": case "up": return "Working";
    case "degraded": return "Having trouble";
    case "unhealthy": case "down": return "Not working";
    case "unknown": case "": return "Not checked";
    default: return health;
  }
}
