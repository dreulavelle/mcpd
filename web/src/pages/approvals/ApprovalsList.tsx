import { useCallback, useMemo, useState } from "react";
import {
  Ban, Check, CircleCheck, ClipboardCheck, PencilLine, Play, TimerOff,
  TriangleAlert, Undo2, X,
} from "lucide-react";
import { api, type Operation, type OperationState } from "@/lib/api";
import {
  confirmationWord, describeChange, describeOutcome, fieldValue, relative,
  riskLabel,
} from "@/lib/format";
import { useLoader } from "@/lib/hooks";
import { Link, useQueryParam } from "@/lib/router";
import { cn } from "@/lib/utils";
import { EmptyState, Loading, Notice, PageHeader } from "@/components/chrome";
import { usePrincipalNames } from "@/components/principal";
import { Segmented } from "@/components/Segmented";
import { stateTone, type Tone } from "@/components/status";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";

/**
 * Changes an assistant has proposed: the ones waiting on a person, and then
 * what became of the rest.
 *
 * Nothing is decided here. Approving from a row would be approving a one-line
 * summary rather than the change, so every card and every row leads to the
 * detail page and the decision is taken there.
 */

/** How the settled changes are cut. "" is all of them. */
type Group = "" | "applied" | "turned-down" | "didnt-run" | "unknown";

const GROUPS: Record<Exclude<Group, "">, OperationState[]> = {
  applied: ["succeeded"],
  // Three ways of never running that a person caused or a clock did. They read
  // as one answer -- it did not happen -- and separating them into three chips
  // would split a handful of rows across three near-empty views.
  "turned-down": ["rejected", "cancelled", "expired"],
  "didnt-run": ["failed"],
  unknown: ["indeterminate"],
};

const GROUP_LABELS: Record<Group, string> = {
  "": "Everything",
  applied: "Applied",
  "turned-down": "Turned down",
  "didnt-run": "Didn't run",
  unknown: "Unknown",
};

/**
 * The state links that existed before this page had chips. Attention sends
 * people here with `?state=indeterminate`, and a link somebody bookmarked has
 * to keep landing on the same changes.
 */
function groupForState(state: string): Group {
  for (const [group, states] of Object.entries(GROUPS)) {
    if (states.includes(state as OperationState)) return group as Group;
  }
  return "";
}

/** What each state's mark is, so a row is scannable before it is read. */
const MARKS: Record<OperationState, typeof Check> = {
  draft: PencilLine,
  pending_approval: TimerOff,
  approved: CircleCheck,
  executing: Play,
  succeeded: Check,
  failed: X,
  indeterminate: TriangleAlert,
  rejected: Ban,
  expired: TimerOff,
  cancelled: Undo2,
};

const MARK_TONE: Record<Tone, string> = {
  good: "text-good",
  attention: "text-attention",
  problem: "text-problem",
  info: "text-info",
  neutral: "text-muted-foreground",
};

/** Risk, as the one colour on a waiting card. Low is deliberately not a colour. */
const RAIL: Record<string, string> = {
  low: "bg-border",
  medium: "bg-info",
  high: "bg-attention",
  critical: "bg-problem",
};

/** An approval within this of running out is worth reading first. */
const SOON_MS = 60 * 60 * 1000;

export function ApprovalsList() {
  const [show, setShow] = useQueryParam("show");
  const [state] = useQueryParam("state");
  // `show` is what this page writes; `state` is what older links carry.
  const group: Group = (show || groupForState(state)) as Group;

  const [system, setSystem] = useQueryParam("system");
  const [legacySystem] = useQueryParam("plugin");
  const plugin = system || legacySystem;

  const [needle, setNeedle] = useState("");
  // The owner asked for the last ten, not every change this host has ever
  // made. The rest is a click away rather than a scrollbar away.
  const [showing, setShowing] = useState(10);

  const load = useCallback(() => api.operations(undefined, 200), []);
  const { data, error } = useLoader(load, "Couldn't load proposed changes.", 10_000);
  const name = usePrincipalNames();

  const loaded = data?.operations ?? [];
  const systems = useMemo(() => {
    const seen = new Set(loaded.map((op) => op.plugin));
    if (plugin) seen.add(plugin);
    return [...seen].sort();
  }, [loaded, plugin]);

  const matching = useMemo(() => {
    const q = needle.trim().toLowerCase();
    return loaded.filter((op) =>
      (!plugin || op.plugin === plugin) &&
      (!q || [
        describeChange(op).sentence, op.plugin, op.requested_by, op.impact,
        op.id, op.authorized_by_rule ?? "",
      ].join(" ").toLowerCase().includes(q)));
  }, [loaded, plugin, needle]);

  // Soonest to run out first: the queue is ordered by the deadline somebody is
  // acting against, not by when the assistant happened to ask.
  const waiting = useMemo(
    () => matching
      .filter((op) => op.state === "pending_approval")
      .sort((a, b) => expiry(a) - expiry(b)),
    [matching],
  );

  // The endpoint walks plugin by plugin, so what comes back is grouped by
  // system rather than ordered in time.
  const settled = useMemo(
    () => matching
      .filter((op) => op.state !== "pending_approval")
      .sort((a, b) => Date.parse(b.requested_at) - Date.parse(a.requested_at)),
    [matching],
  );

  const inGroup = useMemo(
    () => group === "" ? settled : settled.filter((op) => GROUPS[group].includes(op.state)),
    [settled, group],
  );

  const narrowed = plugin !== "" || needle.trim() !== "";

  return (
    <>
      <PageHeader
        title="Approvals"
        lede="Changes your assistants have proposed, and what became of them."
        actions={
          <div className="flex flex-wrap items-center gap-2">
            {systems.length > 1 && (
              <NativeSelect
                aria-label="System"
                className="w-44"
                value={plugin}
                onChange={(e) => setSystem(e.target.value)}
              >
                <option value="">Every system</option>
                {systems.map((p) => <option key={p} value={p}>{p}</option>)}
              </NativeSelect>
            )}
            {loaded.length > 8 && (
              <Input
                aria-label="Find a change"
                className="w-56"
                placeholder="Find a change…"
                value={needle}
                onChange={(e) => setNeedle(e.target.value)}
              />
            )}
          </div>
        }
      />

      {error && <Notice tone="problem">{error}</Notice>}

      {data === null && !error ? (
        <Loading rows={5} />
      ) : loaded.length === 0 ? (
        <EmptyState mark={<ClipboardCheck />} title="Nothing to decide">
          No assistant has proposed anything yet.
        </EmptyState>
      ) : matching.length === 0 ? (
        <EmptyState mark={<ClipboardCheck />} title="Nothing to decide">
          No change matches that.{" "}
          <Button
            variant="link" className="h-auto p-0"
            onClick={() => { setSystem(""); setNeedle(""); }}
          >
            Widen it
          </Button>
          .
        </EmptyState>
      ) : (
        <div className="space-y-8">
          <section aria-labelledby="waiting-heading">
            <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
              <h2 id="waiting-heading" className="text-sm font-semibold">
                Waiting on you{" "}
                {waiting.length > 0 && (
                  <span className="font-normal text-muted-foreground">
                    ({waiting.length})
                  </span>
                )}
              </h2>
              {waiting.length > 1 && (
                <p className="text-xs text-muted-foreground">
                  Soonest to run out first
                </p>
              )}
            </div>
            {waiting.length === 0 ? (
              <p className="mt-2 text-sm text-muted-foreground">
                {narrowed
                  ? "Nothing matching that is waiting on you."
                  : "Nothing is waiting on you."}
              </p>
            ) : (
              <div className="mt-3 space-y-3">
                {waiting.map((op) => (
                  <WaitingCard key={op.id} op={op} name={name} />
                ))}
              </div>
            )}
          </section>

          {settled.length > 0 && (
            <section aria-labelledby="lately-heading">
              <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
                <h2 id="lately-heading" className="text-sm font-semibold">Lately</h2>
                <div className="scroll-x">
                  <Segmented
                    label="Show"
                    value={group}
                    onChange={(next) => { setShow(next); setShowing(10); }}
                    options={(Object.keys(GROUP_LABELS) as Group[]).map((g) => ({
                      value: g,
                      label: `${GROUP_LABELS[g]} (${count(settled, g)})`,
                    }))}
                  />
                </div>
              </div>

              {inGroup.length === 0 ? (
                <p className="mt-3 text-sm text-muted-foreground">
                  Nothing here yet.{" "}
                  <Button
                    variant="link" className="h-auto p-0"
                    onClick={() => setShow("")}
                  >
                    Show everything
                  </Button>
                  .
                </p>
              ) : (
                <>
                  <Card className="mt-3 gap-0 overflow-hidden p-0">
                    <ul className="divide-y">
                      {inGroup.slice(0, showing).map((op) => (
                        <SettledRow key={op.id} op={op} name={name} />
                      ))}
                    </ul>
                  </Card>
                  {inGroup.length > showing && (
                    <Button
                      variant="link" className="mt-2 h-auto p-0"
                      onClick={() => setShowing((n) => n + 10)}
                    >
                      Show more ({inGroup.length - showing} left)
                    </Button>
                  )}
                </>
              )}
            </section>
          )}
        </div>
      )}
    </>
  );
}

/** Missing expiry sorts last: a change with no deadline is not the urgent one. */
function expiry(op: Operation): number {
  const t = Date.parse(op.expires_at ?? "");
  return Number.isNaN(t) ? Number.POSITIVE_INFINITY : t;
}

function count(settled: Operation[], group: Group): number {
  return group === ""
    ? settled.length
    : settled.filter((op) => GROUPS[group].includes(op.state)).length;
}

/**
 * The first recorded field, as the two halves a layout can weight separately.
 * Null where nothing was recorded, or where the value is structured and has no
 * honest short form.
 */
function fieldDelta(op: Operation): { from: string | null; to: string } | null {
  const first = op.changes?.[0];
  if (!first) return null;
  const to = fieldValue(first.to);
  if (to === null) return null;
  return { from: fieldValue(first.from), to };
}

/**
 * One change waiting on a decision.
 *
 * The whole card is the link, because the thing a person does with it is open
 * it. What it leads with is the change itself, in the largest type on the
 * page: everything else here -- who asked, what it risks, what the record will
 * prove -- is a qualifier on that sentence.
 */
function WaitingCard({ op, name }: {
  op: Operation;
  name: (actor: string) => string;
}) {
  const { headline, detail } = describeChange(op);
  const delta = fieldDelta(op);
  const left = Date.parse(op.expires_at) - Date.now();
  const soon = !Number.isNaN(left) && left < SOON_MS;

  return (
    <Link
      to={`/approvals/${encodeURIComponent(op.id)}`}
      className="group block rounded-xl focus-visible:ring-[3px] focus-visible:ring-ring/50 focus-visible:outline-none"
    >
      <Card className="relative gap-0 overflow-hidden py-0 group-hover:border-primary/40">
        <span
          aria-hidden="true"
          className={cn("absolute inset-y-0 left-0 w-[3px]", RAIL[op.risk] ?? RAIL.low)}
        />
        <div className="space-y-3 py-4 pr-4 pl-5">
          <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
            <h3 className="text-base font-medium">{headline}</h3>
            {op.expires_at && (
              <p className={cn(
                "text-xs whitespace-nowrap",
                soon ? "text-attention" : "text-muted-foreground",
              )}>
                Runs out {relative(op.expires_at)}
              </p>
            )}
          </div>

          {delta ? (
            <p className="text-sm text-muted-foreground">
              {delta.from !== null && (
                <>from <span className="font-medium text-foreground">{delta.from}</span>{" "}</>
              )}
              to <span className="font-medium text-foreground">{delta.to}</span>
            </p>
          ) : detail ? (
            <p className="text-sm text-muted-foreground">{detail}</p>
          ) : null}

          <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 text-xs">
            <p className="text-muted-foreground">
              {name(op.requested_by)}
              {" · "}
              <span className={op.risk === "low" ? undefined : MARK_TONE[riskTone(op.risk)]}>
                {riskLabel(op.risk).toLowerCase()} risk
              </span>
              {" · "}
              {op.assurance === "reviewed_change" ? "a reviewed change" : "a gated call"}
            </p>
            <span className="font-medium text-primary group-hover:underline">
              Read and decide →
            </span>
          </div>
        </div>
      </Card>
    </Link>
  );
}

function riskTone(risk: string): Tone {
  switch (risk) {
    case "critical": return "problem";
    case "high": return "attention";
    case "medium": return "info";
    default: return "neutral";
  }
}

/**
 * One change that is no longer waiting: the sentence, then who, what happened
 * and whether anybody confirmed it. Not a table -- six columns of which four
 * are empty for most rows is what this page had, and it read as a database.
 */
function SettledRow({ op, name }: {
  op: Operation;
  name: (actor: string) => string;
}) {
  const { headline } = describeChange(op);
  const delta = fieldDelta(op);
  const tone = stateTone(op.state);
  const Mark = MARKS[op.state] ?? Check;

  // Who the row is about is whoever settled it, falling back to whoever asked.
  // A rule is named as a rule: `approved_by` on one of those is `system:policy`,
  // which is not an account and must not read as one.
  const actor = op.authorized_by_rule
    ? "system:policy"
    : op.approved_by || op.requested_by;

  // Before it ran, "not checked" is true of everything and says nothing.
  const ran = op.state === "succeeded" || op.state === "failed" ||
    op.state === "indeterminate";

  return (
    <li className="flex items-start gap-3 px-4 py-3">
      <Mark
        aria-hidden="true"
        className={cn("mt-0.5 size-4 shrink-0", MARK_TONE[tone])}
      />
      <div className="min-w-0 flex-1">
        <p className="truncate text-sm">
          <Link
            to={`/approvals/${encodeURIComponent(op.id)}`}
            className="font-medium hover:underline"
          >
            {headline}
          </Link>
          {delta && (
            <span className="text-muted-foreground">
              {" — "}
              {delta.from !== null ? `from ${delta.from} to ${delta.to}` : `to ${delta.to}`}
            </span>
          )}
        </p>
        <p className="text-xs text-muted-foreground">
          {[
            name(actor),
            describeOutcome(op),
            ran ? confirmationWord(op.verified) : "",
          ].filter(Boolean).join(" · ")}
        </p>
      </div>
    </li>
  );
}
