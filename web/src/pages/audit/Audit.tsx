import { useCallback, useEffect, useState } from "react";
import { ScrollText, ShieldAlert, Unlink } from "lucide-react";
import { api, ApiError, type AuditRecord } from "@/lib/api";
import { describeEvent, pretty, when, who } from "@/lib/format";
import { useLoader } from "@/lib/hooks";
import { Link } from "@/lib/router";
import { useCan } from "@/lib/session";
import { cn } from "@/lib/utils";
import { useNotify } from "@/components/toast";
import {
  CodeBlock, EmptyState, Loading, Notice, PageHeader,
} from "@/components/chrome";
import { RiskBadge } from "@/components/status";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { NativeSelect } from "@/components/ui/native-select";

const LIMITS = [100, 250, 500];

/**
 * The audit trail.
 *
 * Append-only and hash-chained in the database, enforced by triggers, so the
 * question this page answers is not only "what happened" but "is what happened
 * still what it says".
 *
 * Drawn as a chain rather than as a table, because the chain is the property
 * worth seeing. A grid of rows asserts that each entry follows the one before
 * it; a line drawn between them shows it, and — the part that matters — has
 * somewhere to visibly break. The break is rendered where it happened rather
 * than only announced at the top, so "which entries are still evidence" is a
 * thing an operator can read off the page.
 *
 * Silent when the chain is intact. A check that announced success on every
 * visit would train somebody to skip the one time it did not.
 */
export function Audit() {
  const mayVerify = useCan("admin");
  const [limit, setLimit] = useState(LIMITS[0]!);
  const [expanded, setExpanded] = useState<number | null>(null);
  const [brokenAt, setBrokenAt] = useState<number | null>(null);
  const [checks, setChecks] = useState(0);

  const load = useCallback(() => api.audit(limit), [limit]);
  const { data, error, reload } = useLoader(load, "Couldn't load the history.");
  const records = data?.records ?? [];

  /**
   * Whether the chain still verifies.
   *
   * Only an administrator may run the check, so this asks only when one is
   * signed in. Calling it regardless and swallowing the 403 would put a
   * refusal in everyone else's network log for a question they were never
   * allowed to ask.
   */
  useEffect(() => {
    if (!mayVerify) return;
    let live = true;
    api.verifyAudit()
      .then((c) => { if (live) setBrokenAt(c.intact ? null : c.broken_at); })
      // A check that could not run is not a check that failed. Claiming a
      // break because the request timed out would be the worst false alarm
      // this console could raise.
      .catch(() => { if (live) setBrokenAt(null); });
    return () => { live = false; };
  }, [mayVerify, checks]);

  const verify = useCallback(() => setChecks((n) => n + 1), []);

  return (
    <>
      <PageHeader
        title="Audit"
        lede="Every decision and every transition, in order. Each entry carries the hash of the one before it, so an entry that was altered no longer follows the one it claims to."
        actions={
          <div className="flex items-center gap-2">
            <NativeSelect
              aria-label="How many entries"
              className="w-32"
              value={limit}
              onChange={(e) => setLimit(Number(e.target.value))}
            >
              {LIMITS.map((n) => <option key={n} value={n}>Last {n}</option>)}
            </NativeSelect>
            <ClearHistory
              disabled={records.length === 0}
              onCleared={() => { reload(); verify(); }}
            />
          </div>
        }
      />

      {error && <Notice tone="problem">{error}</Notice>}

      {brokenAt !== null && (
        <Notice tone="problem" icon={<ShieldAlert />}>
          <strong>The history has been altered.</strong> Something edited the
          database directly: entry {brokenAt} does not follow the one before it.
          Everything from there on is no longer evidence of anything.
        </Notice>
      )}

      {data === null && !error ? (
        <Loading rows={6} />
      ) : records.length === 0 ? (
        <EmptyState mark={<ScrollText />} title="Nothing recorded yet">
          Entries appear here as soon as an assistant proposes something or
          somebody decides on it.
        </EmptyState>
      ) : (
        <Card className="mt-4 p-4 sm:p-6">
          <ol className="space-y-0">
            {records.map((r, i) => (
              <Entry
                key={r.seq}
                record={r}
                first={i === 0}
                last={i === records.length - 1}
                // The chain reads oldest to newest and the page reads newest
                // first, so the severed link is below the entry the check
                // named: it is that entry which stopped following its
                // predecessor.
                severed={r.seq === brokenAt}
                // A hole between two entries on screen is entries that are no
                // longer in the table. Pruning takes from the oldest end, so a
                // gap in the middle is not that.
                missing={gapBelow(records, i)}
                open={expanded === r.seq}
                onToggle={() => setExpanded(expanded === r.seq ? null : r.seq)}
              />
            ))}
          </ol>
        </Card>
      )}
    </>
  );
}

/** How many entries are absent between this row and the older one below it. */
function gapBelow(records: AuditRecord[], i: number): number {
  const older = records[i + 1];
  if (!older) return 0;
  return Math.max(0, records[i]!.seq - older.seq - 1);
}

function Entry({ record: r, first, last, severed, missing, open, onToggle }: {
  record: AuditRecord;
  first: boolean;
  last: boolean;
  severed: boolean;
  missing: number;
  open: boolean;
  onToggle: () => void;
}) {
  const detail = pretty(r.detail);
  return (
    <li className="relative flex gap-3">
      <Rail first={first} last={last} severed={severed} />

      <div className="min-w-0 flex-1 pb-4">
        <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
          <span className="font-mono text-xs text-muted-foreground tabular-nums">
            {r.seq}
          </span>
          <span className="text-sm">{describeEvent(r)}</span>
          {r.risk && r.risk !== "low" && <RiskBadge risk={r.risk} />}
          <span className="ml-auto text-xs whitespace-nowrap text-muted-foreground">
            {when(r.at)}
          </span>
        </div>

        <div className="mt-0.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
          <span>{who(r.actor)}</span>
          {r.plugin && <span>{r.plugin}</span>}
          {r.from_state && r.to_state && (
            <span className="font-mono">{r.from_state} → {r.to_state}</span>
          )}
          {r.operation_id && (
            <Link
              to={`/approvals/${encodeURIComponent(r.operation_id)}`}
              className="font-mono text-primary hover:underline"
            >
              {r.operation_id.slice(0, 12)}…
            </Link>
          )}
          {detail && (
            <Button variant="ghost" size="xs" onClick={onToggle} aria-expanded={open}>
              {open ? "Hide" : "Detail"}
            </Button>
          )}
        </div>

        {open && detail && <CodeBlock className="mt-2">{detail}</CodeBlock>}

        {severed && (
          <p className="mt-1 flex items-center gap-1.5 text-xs font-medium text-problem">
            <Unlink className="size-3.5" aria-hidden="true" />
            The chain breaks here — this entry does not follow the one below it.
          </p>
        )}

        {missing > 0 && !severed && (
          <p className="mt-1 text-xs text-muted-foreground">
            {missing} {missing === 1 ? "entry is" : "entries are"} not in this
            table between here and the next one.
          </p>
        )}
      </div>
    </li>
  );
}

/**
 * The link between one entry and the next.
 *
 * A hairline and a node, and where the chain does not verify, a severed one:
 * dashed, in the problem colour, with a gap in it. The point is that the
 * tamper-evidence is a thing on the page rather than a claim in a sentence.
 */
function Rail({ first, last, severed }: {
  first: boolean;
  last: boolean;
  severed: boolean;
}) {
  return (
    <div className="relative w-3 shrink-0" aria-hidden="true">
      {!first && (
        <span className="absolute top-0 left-1/2 h-1.5 w-px -translate-x-1/2 bg-border" />
      )}
      <span
        className={cn(
          "absolute top-1.5 left-1/2 size-2 -translate-x-1/2 rounded-full border bg-card",
          severed ? "border-problem" : "border-border",
        )}
      />
      {!last && (
        <span
          className={cn(
            "absolute top-4 bottom-0 left-1/2 -translate-x-1/2",
            severed
              ? "border-l border-dashed border-problem"
              : "w-px bg-border",
          )}
        />
      )}
    </div>
  );
}

function ClearHistory({ disabled, onCleared }: {
  disabled: boolean;
  onCleared: () => void;
}) {
  const mayClear = useCan("admin");
  const notify = useNotify();
  const [busy, setBusy] = useState(false);

  if (!mayClear) return null;

  async function clear() {
    if (!confirm("Clear the history? The record of everything so far is removed, and clearing it is itself recorded.")) {
      return;
    }
    setBusy(true);
    try {
      const r = await api.clearAudit();
      notify("good", `Cleared ${r.removed} ${r.removed === 1 ? "entry" : "entries"}.`);
    } catch (e) {
      notify("problem", e instanceof ApiError ? e.detail : "Couldn't clear it.");
    } finally {
      setBusy(false);
      onCleared();
    }
  }

  return (
    <Button variant="outline" size="sm" disabled={busy || disabled} onClick={clear}>
      {busy ? "Clearing…" : "Clear"}
    </Button>
  );
}
