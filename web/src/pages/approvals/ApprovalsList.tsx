import { useCallback, useMemo, useState } from "react";
import {
  Ban, Check, CircleCheck, ClipboardCheck, PencilLine, Play, TimerOff,
  TriangleAlert, Undo2, X,
} from "lucide-react";
import { api, type Operation, type OperationState } from "@/lib/api";
import {
  changeDelta, confirmationWord, deltaWords, describeChange, describeOutcome,
  OPERATION_STATES, relative, riskLabel, stateLabel,
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
import { FieldDelta } from "./delta";

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

/** Whether a value out of the address is one of the groups this build has. */
function isGroup(value: string): value is Group {
  return value === "" || Object.hasOwn(GROUPS, value);
}

/**
 * The state links that existed before this page had chips.
 *
 * Attention sends people here with `?state=indeterminate`, and a link somebody
 * bookmarked has to land on the changes it named. Translating one to a chip
 * was lossy in both directions -- `rejected` would have picked up withdrawn
 * and expired, and `approved` had no chip at all -- so the parameter keeps its
 * own exact filter and says on screen that it is narrowing the list.
 *
 * `pending_approval` is not one of these: those changes are the section above,
 * which is already the first thing on the page.
 */
function exactState(state: string): OperationState | "" {
  if (state === "" || state === "pending_approval") return "";
  return Object.hasOwn(OPERATION_STATES, state) ? state as OperationState : "";
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
  const [state, setState] = useQueryParam("state");
  // An unknown value in either is no filter rather than a crash: the address
  // bar is somewhere anybody can type, and `GROUPS[group]` on a value nobody
  // defined is a blank page.
  const group: Group = isGroup(show) ? show : "";
  const only = exactState(state);

  const [system, setSystem] = useQueryParam("system");
  const [legacySystem, setLegacySystem] = useQueryParam("plugin");
  const plugin = system || legacySystem;

  // Both halves of each pair, or the legacy one wins back the moment the new
  // one is cleared and the view snaps to a filter nobody chose.
  const chooseGroup = (next: Group) => { setShow(next); setState(""); };
  const chooseSystem = (next: string) => { setSystem(next); setLegacySystem(""); };

  const [needle, setNeedle] = useState("");
  // The owner asked for the last ten, not every change this host has ever
  // made. The rest is a click away rather than a scrollbar away.
  const [showing, setShowing] = useState(10);

  // Two calls, because the unfiltered one is capped per plugin server-side: a
  // system that has settled two hundred changes since lunch would push a
  // proposal still waiting on somebody out of the page entirely, and the
  // queue is the half of this screen that cannot be allowed to lie.
  const load = useCallback(async () => {
    const [all, pending] = await Promise.all([
      api.operations(undefined, 200),
      api.operations("pending_approval", 200),
    ]);
    // The unfiltered answer wins where both carry a change. The two calls are
    // concurrent, and a state only moves forward out of pending: a row that is
    // pending in one answer and applied in the other has been applied, and
    // letting the pending copy overwrite it would put a settled change back in
    // the queue.
    const byId = new Map<string, Operation>();
    for (const op of all.operations ?? []) byId.set(op.id, op);
    for (const op of pending.operations ?? []) {
      if (!byId.has(op.id)) byId.set(op.id, op);
    }
    return { operations: [...byId.values()] };
  }, []);
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
      // The words on the screen, and also the ones that are not: somebody who
      // knows an action or has an id from a support call should find the
      // change by it, even though neither is what the row shows.
      (!q || [
        describeChange(op).sentence, op.plugin, op.action, op.requested_by,
        op.impact, op.id, op.authorized_by_rule ?? "",
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
    () => only
      ? settled.filter((op) => op.state === only)
      : group === ""
        ? settled
        : settled.filter((op) => GROUPS[group].includes(op.state)),
    [settled, group, only],
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
                onChange={(e) => chooseSystem(e.target.value)}
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
        // Not "Nothing to decide": changes may well be waiting, and the filter
        // is the only reason none of them is on the screen.
        <EmptyState mark={<ClipboardCheck />} title="Nothing here">
          No change matches that.{" "}
          <Button
            variant="link" className="h-auto p-0"
            onClick={() => { chooseSystem(""); setNeedle(""); }}
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
                {/* A link that named one exact state gets that exact state,
                    and says so. The chips are groups, and answering a link for
                    turned-down changes with withdrawn and expired ones beside
                    them would be answering a different question. */}
                {only ? (
                  <p className="flex items-center gap-2 text-xs text-muted-foreground">
                    Showing only: {stateLabel(only).toLowerCase()}
                    <Button
                      variant="link" className="h-auto p-0 text-xs"
                      onClick={() => { setState(""); setShowing(10); }}
                    >
                      Show everything
                    </Button>
                  </p>
                ) : (
                <div className="scroll-x">
                  <Segmented
                    label="Show"
                    value={group}
                    onChange={(next) => { chooseGroup(next); setShowing(10); }}
                    options={(Object.keys(GROUP_LABELS) as Group[]).map((g) => ({
                      value: g,
                      label: `${GROUP_LABELS[g]} (${count(settled, g)})`,
                    }))}
                  />
                </div>
                )}
              </div>

              {inGroup.length === 0 ? (
                <p className="mt-3 text-sm text-muted-foreground">
                  Nothing here yet.{" "}
                  <Button
                    variant="link" className="h-auto p-0"
                    onClick={() => { chooseGroup(""); setShowing(10); }}
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
 * One change waiting on a decision.
 *
 * The whole card is the link, because the thing a person does with it is open
 * it. What it leads with is the change itself, in the largest type on the
 * page: everything else here -- who asked, what it risks, what the record will
 * prove -- is a qualifier on that sentence.
 */
function WaitingCard({ op, name }: {
  op: Operation;
  name: (actor: string, resolved?: string) => string;
}) {
  const { headline, detail } = describeChange(op);
  const delta = changeDelta(op);
  const left = Date.parse(op.expires_at) - Date.now();
  const soon = !Number.isNaN(left) && left < SOON_MS;
  // Past the deadline it has not run out "5 minutes ago" -- it is simply too
  // late, and the arithmetic is not the fact.
  const gone = !Number.isNaN(left) && left <= 0;

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
                {gone ? "Out of time" : `Runs out ${relative(op.expires_at)}`}
              </p>
            )}
          </div>

          {delta ? (
            <p className="text-sm">
              <FieldDelta delta={delta} />
            </p>
          ) : detail ? (
            <p className="text-sm text-muted-foreground">{detail}</p>
          ) : null}

          <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 text-xs">
            <p className="text-muted-foreground">
              {name(op.requested_by, op.requested_by_name)}
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
  name: (actor: string, resolved?: string) => string;
}) {
  const { headline, detail } = describeChange(op);
  const delta = changeDelta(op);
  const tone = stateTone(op.state);
  const Mark = MARKS[op.state] ?? Check;

  // Who the row names, and only what the record actually says.
  //
  // `approved_by` is whoever approved it, which on most rows is also whoever
  // settled it. On these three it is not: approved -> cancelled and
  // approved -> expired are both legal, and rejecting never writes the field
  // at all. Naming the approver beside "withdrawn" put the wrong person on
  // the act, so on these the name keeps its own verb and the outcome beside
  // it stays subjectless.
  const approverOnly = op.state === "rejected" || op.state === "cancelled" ||
    op.state === "expired";
  // A rule is named as a rule: `approved_by` on one of those is
  // `system:policy`, which is not an account and must not read as one.
  const decided = op.authorized_by_rule
    ? name("system:policy")
    : op.approved_by
      ? name(op.approved_by, op.approved_by_name)
      : "";
  const who = decided
    ? approverOnly ? `approved by ${decided}` : decided
    : `proposed by ${name(op.requested_by, op.requested_by_name)}`;

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
          {/* A delete records no new value, so without the plugin's own
              sentence two deletions on one system read identically. */}
          {delta ? (
            <span className="text-muted-foreground">
              {" — "}{deltaWords(delta)}
            </span>
          ) : detail ? (
            <span className="text-muted-foreground">{" — "}{detail}</span>
          ) : null}
        </p>
        <p className="text-xs text-muted-foreground">
          {[
            who,
            describeOutcome(op),
            ran ? confirmationWord(op.verified) : "",
          ].filter(Boolean).join(" · ")}
        </p>
      </div>
    </li>
  );
}
