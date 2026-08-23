import type { AuditRecord, Operation, OperationState } from "@/lib/api";
import { stateLabel } from "@/lib/format";
import { cn } from "@/lib/utils";
import { Chip } from "@/components/status";
import { Card, CardContent } from "@/components/ui/card";

/**
 * Where an operation sits in the machine, drawn. SVG and CSS, no library, and
 * nothing that moves: a transition here pictures a change that did not happen.
 */

/**
 * Mirrors the transition table in `internal/operations/state_machine.go`.
 * `indeterminate` has successors on purpose: it is unknown, not terminal.
 */
const NEXT: Partial<Record<OperationState, OperationState[]>> = {
  draft: ["pending_approval", "cancelled"],
  pending_approval: ["approved", "rejected", "cancelled", "expired"],
  approved: ["executing", "cancelled", "expired"],
  executing: ["succeeded", "failed", "indeterminate"],
  indeterminate: ["succeeded", "failed"],
};

/**
 * What a state proves about the past on its own -- only what is certain. The
 * trail says the rest, when it has not been cleared.
 */
const PAST: Record<OperationState, OperationState[]> = {
  draft: [],
  pending_approval: [],
  approved: ["pending_approval"],
  executing: ["pending_approval", "approved"],
  succeeded: ["pending_approval", "approved", "executing"],
  failed: ["pending_approval", "approved", "executing"],
  indeterminate: ["pending_approval", "approved", "executing"],
  rejected: ["pending_approval"],
  expired: ["pending_approval"],
  cancelled: [],
};

/** How a node is drawn, and why. */
export type NodeStatus =
  /** Where it is now. */
  | "current"
  /** It went through here. */
  | "past"
  /** It can still happen. */
  | "ahead"
  /** It cannot happen any more, or never did. */
  | "closed";

/**
 * Reads the shape of one operation's life. Pure, so the reachability rules are
 * testable without a DOM. `seen` is what the trail witnessed.
 */
export function lifecycle(
  state: OperationState,
  seen: Iterable<OperationState> = [],
): Map<OperationState, NodeStatus> {
  const past = new Set<OperationState>([...(PAST[state] ?? []), ...seen]);
  past.delete(state);

  // Everything still in front of it, by walking the table forward.
  const ahead = new Set<OperationState>();
  const queue = [...(NEXT[state] ?? [])];
  while (queue.length > 0) {
    const s = queue.shift()!;
    if (ahead.has(s) || s === state) continue;
    ahead.add(s);
    queue.push(...(NEXT[s] ?? []));
  }

  const status = new Map<OperationState, NodeStatus>();
  for (const s of Object.keys(PAST) as OperationState[]) {
    status.set(
      s,
      s === state ? "current" : past.has(s) ? "past" : ahead.has(s) ? "ahead" : "closed",
    );
  }
  return status;
}

/** Which states the trail actually witnessed. */
function witnessed(audit: AuditRecord[]): OperationState[] {
  const seen: OperationState[] = [];
  for (const r of audit) {
    if (r.from_state) seen.push(r.from_state as OperationState);
    if (r.to_state) seen.push(r.to_state as OperationState);
  }
  return seen;
}

/**
 * Only the node it is on carries a tone: colouring a failure that has not
 * happened would report a state the operation is not in. `indeterminate` is
 * "attention" and never "problem" -- it may have landed.
 */
const NODE_TONE: Partial<Record<OperationState, string>> = {
  succeeded: "fill-good-soft stroke-good",
  failed: "fill-problem-soft stroke-problem",
  indeterminate: "fill-attention-soft stroke-attention",
};

const NODE_TEXT: Partial<Record<OperationState, string>> = {
  succeeded: "fill-good",
  failed: "fill-problem",
  indeterminate: "fill-attention",
};

interface Box {
  state: OperationState;
  x: number;
  y: number;
  w: number;
  h: number;
}

const SPINE_W = 118;
const SPINE_H = 40;
const OUT_W = 136;
const OUT_H = 34;

/** The spine: what happens when nothing interrupts. */
const SPINE: Box[] = [
  { state: "draft", x: 8, y: 76, w: SPINE_W, h: SPINE_H },
  { state: "pending_approval", x: 158, y: 76, w: SPINE_W, h: SPINE_H },
  { state: "approved", x: 308, y: 76, w: SPINE_W, h: SPINE_H },
  { state: "executing", x: 458, y: 76, w: SPINE_W, h: SPINE_H },
];

/** How running ends. */
const OUTCOMES: Box[] = [
  { state: "succeeded", x: 616, y: 22, w: OUT_W, h: OUT_H },
  { state: "indeterminate", x: 616, y: 79, w: OUT_W, h: OUT_H },
  { state: "failed", x: 616, y: 136, w: OUT_W, h: OUT_H },
];

/** How it ends without ever running. */
const OFF_RAMPS: Box[] = [
  { state: "cancelled", x: 8, y: 178, w: SPINE_W, h: OUT_H },
  { state: "rejected", x: 158, y: 178, w: SPINE_W, h: OUT_H },
  { state: "expired", x: 308, y: 178, w: SPINE_W, h: OUT_H },
];

const ALL_BOXES = [...SPINE, ...OUTCOMES, ...OFF_RAMPS];

export function Lifecycle({ operation: op, audit }: {
  operation: Operation;
  audit: AuditRecord[];
}) {
  const status = lifecycle(op.state, witnessed(audit));
  const remaining = NEXT[op.state] ?? [];

  return (
    <div className="space-y-4">
      <Card>
        <CardContent className="space-y-4">
          <div className="scroll-x">
            <svg
              viewBox="0 0 760 224"
              className="h-auto w-full min-w-[560px]"
              role="img"
              aria-label={summary(op.state, remaining, op.authorized_by_rule)}
            >
              <defs>
                <marker
                  id="lifecycle-arrow" viewBox="0 0 8 8" refX="7" refY="4"
                  markerWidth="6" markerHeight="6" orient="auto-start-reverse"
                >
                  <path d="M 0 1 L 7 4 L 0 7 z" className="fill-border" />
                </marker>
              </defs>

              <g className="stroke-border" strokeWidth="1.5" fill="none" markerEnd="url(#lifecycle-arrow)">
                {/* The spine. */}
                <path d="M 126 96 H 158" />
                <path d="M 276 96 H 308" />
                <path d="M 426 96 H 458" />
                {/* Running fans out into the three ways it can end. */}
                <path d="M 576 96 H 596 V 39 H 616" />
                <path d="M 576 96 H 616" />
                <path d="M 576 96 H 596 V 153 H 616" />
                {/* Waiting and approved both drop to the rail below. */}
                <path d="M 217 116 V 160" markerEnd="none" />
                <path d="M 367 116 V 160" markerEnd="none" />
                <path d="M 67 160 H 367" markerEnd="none" />
                <path d="M 67 160 V 178" />
                <path d="M 217 160 V 178" />
                <path d="M 367 160 V 178" />
              </g>

              {ALL_BOXES.map((box) => (
                <Node key={box.state} box={box} status={status.get(box.state)!} />
              ))}
            </svg>
          </div>

          <p className="text-xs text-muted-foreground">
            The row below is how it ends without running: turned down only while
            it is waiting, withdrawn by whoever proposed it, expired when nobody
            decided in time or the approval itself ran out.
          </p>

          {/* Every node is true, but "approved" reads as somebody having
              approved it. Said plainly, not flagged: this is a legitimate
              route through the machine. */}
          {op.authorized_by_rule && (
            <p className="text-xs text-muted-foreground">
              <strong className="font-medium text-foreground">
                No one was asked.
              </strong>{" "}
              Rule <code className="font-mono">{op.authorized_by_rule}</code>{" "}
              allowed this kind of change, so it was approved straight away.
              Everything after that was recorded as usual.
            </p>
          )}
        </CardContent>
      </Card>

      <Remaining state={op.state} remaining={remaining} />
      <Proofs operation={op} />
    </div>
  );
}

function Node({ box, status }: { box: Box; status: NodeStatus }) {
  const current = status === "current";

  return (
    <g data-node={box.state} data-status={status}>
      <rect
        x={box.x} y={box.y} width={box.w} height={box.h} rx="8"
        strokeWidth={current ? 2 : 1}
        strokeDasharray={status === "ahead" || status === "closed" ? "4 3" : undefined}
        className={cn(
          current
            ? NODE_TONE[box.state] ?? "fill-accent stroke-foreground"
            : status === "past"
              ? "fill-muted stroke-border"
              : "fill-none stroke-border-soft",
        )}
      />
      <text
        x={box.x + box.w / 2} y={box.y + box.h / 2 + 4}
        textAnchor="middle" fontSize="13"
        className={cn(
          current
            ? cn("font-semibold", NODE_TEXT[box.state] ?? "fill-foreground")
            : status === "past"
              ? "fill-foreground"
              : "fill-muted-foreground",
          // Struck through rather than greyed further, which would fail contrast.
          status === "closed" && "line-through",
        )}
      >
        {stateLabel(box.state)}
      </text>
    </g>
  );
}

/** What can still happen, in words, beside the picture that shows it. */
function Remaining({ state, remaining }: {
  state: OperationState;
  remaining: OperationState[];
}) {
  if (remaining.length === 0) {
    return (
      <p className="text-sm text-muted-foreground">
        Nothing else can happen to this — it is settled.
      </p>
    );
  }
  return (
    <div className="flex flex-wrap items-center gap-2">
      <p className="text-sm text-muted-foreground">
        {state === "indeterminate"
          // Reconciliation, not a retry.
          ? "Still open. Reading the target upstream settles it as:"
          : "What can still happen:"}
      </p>
      {remaining.map((s) => <Chip key={s}>{stateLabel(s)}</Chip>)}
    </div>
  );
}

/** The two proofs that separate a reviewed change from a gated call. */
function Proofs({ operation: op }: { operation: Operation }) {
  const outcome = outcomeProof(op);
  return (
    <dl className="grid gap-3 sm:grid-cols-2">
      <Proof
        label="Drift"
        tone={op.drift_checked ? "good" : "neutral"}
        mark={op.drift_checked ? "Snapshot held" : "None declared"}
      >
        {op.drift_checked
          ? "The plan carries a precondition snapshot, so a change underneath it is detectable."
          : "Nothing was compared. Two absent snapshots matching is not a check that passed — it is one that never ran."}
      </Proof>
      <Proof label="Outcome" tone={outcome.tone} mark={outcome.mark}>
        {outcome.detail}
      </Proof>
    </dl>
  );
}

/**
 * Three values, not two: absent is "nobody checked", the ordinary state of
 * anything in flight, and must never render as a tick.
 */
function outcomeProof(op: Operation): {
  tone: "good" | "problem" | "neutral";
  mark: string;
  detail: string;
} {
  if (op.verified === true) {
    return {
      tone: "good",
      mark: "Confirmed",
      detail: "mcpd re-read the target afterwards and found what was asked for.",
    };
  }
  if (op.verified === false) {
    return {
      tone: "problem",
      mark: "Did not match",
      detail: "mcpd re-read the target afterwards and what it found was not what was asked for.",
    };
  }
  if (!op.outcome_verifiable) {
    return {
      tone: "neutral",
      mark: "Not provable",
      detail: "This kind of change declares that re-reading the target would prove nothing, so nothing was read back.",
    };
  }
  return {
    tone: "neutral",
    mark: "Never checked",
    detail: op.terminal
      ? "Nobody re-read the target, so this record says nothing either way about whether the change is in place."
      : "It has not run yet, so there is nothing to read back.",
  };
}

function Proof({ label, tone, mark, children }: {
  label: string;
  tone: "good" | "problem" | "neutral";
  mark: string;
  children: string;
}) {
  return (
    <div className="space-y-1 rounded-lg border p-3">
      <div className="flex items-center gap-2">
        <dt className="text-xs font-medium tracking-wide text-muted-foreground uppercase">
          {label}
        </dt>
        <Chip tone={tone}>{mark}</Chip>
      </div>
      <dd className="text-sm text-muted-foreground">{children}</dd>
    </div>
  );
}

/** The diagram, for somebody who cannot see it. */
function summary(
  state: OperationState,
  remaining: OperationState[],
  authorizedByRule?: string,
): string {
  const now = `This change is ${stateLabel(state).toLowerCase()}.`;
  // In the label too, or somebody reading the diagram through its description
  // is the one person not told.
  const how = authorizedByRule
    ? ` Rule ${authorizedByRule} allowed it, so no one was asked.`
    : "";
  if (remaining.length === 0) return `${now}${how} Nothing else can happen to it.`;
  return `${now}${how} It can still become ${remaining.map(stateLabel).join(", ")}.`;
}
