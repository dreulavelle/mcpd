import { useCallback, useEffect, useMemo, useState } from "react";
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
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { useConfirm } from "@/components/confirm";

const LIMITS = [100, 250, 500];

/**
 * The audit trail, drawn as a chain because the chain is the property worth
 * seeing: it has somewhere to visibly break, and the break is rendered where it
 * happened. Silent when intact, so the one failure is not skipped over.
 */
export function Audit() {
  const mayVerify = useCan("history:read");
  const [limit, setLimit] = useState(LIMITS[0]!);
  const [expanded, setExpanded] = useState<number | null>(null);
  const [brokenAt, setBrokenAt] = useState<number | null>(null);
  const [checks, setChecks] = useState(0);

  const [kind, setKind] = useState("");
  const [actor, setActor] = useState("");
  const [plugin, setPlugin] = useState("");
  const [needle, setNeedle] = useState("");

  const load = useCallback(() => api.audit(limit), [limit]);
  const { data, error, reload } = useLoader(load, "Couldn't load the history.");
  const records = useMemo(() => data?.records ?? [], [data]);

  // Over what is loaded, not a query the server answers: the endpoint takes
  // a count and nothing else, and a filter that quietly narrowed the window
  // it asked for would hide the entries somebody was looking for. The size
  // control beside these says how far back the answer reaches.
  const kinds = useMemo(() => [...new Set(records.map((r) => r.kind))].sort(), [records]);
  const actors = useMemo(() => [...new Set(records.map((r) => r.actor))].sort(), [records]);
  const plugins = useMemo(
    () => [...new Set(records.map((r) => r.plugin).filter((p): p is string => !!p))].sort(),
    [records],
  );
  const shown = useMemo(() => {
    const q = needle.trim().toLowerCase();
    return records.filter((r) =>
      (!kind || r.kind === kind) &&
      (!actor || r.actor === actor) &&
      (!plugin || r.plugin === plugin) &&
      (!q || [
        describeEvent(r), r.actor, r.plugin ?? "", r.operation_id ?? "",
        String(r.seq), pretty(r.detail),
      ].join(" ").toLowerCase().includes(q)));
  }, [records, kind, actor, plugin, needle]);
  const filtering = kind !== "" || actor !== "" || plugin !== "" || needle.trim() !== "";

  /** Only an administrator may run the check, so only one asks for it. */
  useEffect(() => {
    if (!mayVerify) return;
    let live = true;
    api.verifyAudit()
      .then((c) => { if (live) setBrokenAt(c.intact ? null : c.broken_at); })
      // A check that could not run is not a check that failed.
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
        <>
          <div className="mt-4 flex flex-wrap items-end gap-2">
            <NativeSelect aria-label="Kind of entry" className="w-52" value={kind}
                          onChange={(e) => setKind(e.target.value)}>
              <option value="">Every kind</option>
              {kinds.map((k) => <option key={k} value={k}>{k.replace(/[._]/g, " ")}</option>)}
            </NativeSelect>
            <NativeSelect aria-label="Who" className="w-48" value={actor}
                          onChange={(e) => setActor(e.target.value)}>
              <option value="">Anyone</option>
              {actors.map((a) => <option key={a} value={a}>{who(a)}</option>)}
            </NativeSelect>
            {plugins.length > 0 && (
              <NativeSelect aria-label="System" className="w-44" value={plugin}
                            onChange={(e) => setPlugin(e.target.value)}>
                <option value="">Every system</option>
                {plugins.map((p) => <option key={p} value={p}>{p}</option>)}
              </NativeSelect>
            )}
            <Input
              aria-label="Find in these entries"
              className="min-w-48 flex-1"
              placeholder="Find an entry, a reference, a word in its detail…"
              value={needle}
              onChange={(e) => setNeedle(e.target.value)}
            />
          </div>
          {filtering && (
            <p className="mt-2 text-xs text-muted-foreground">
              {shown.length} of the last {records.length} entries match.
              {" "}Gaps in the chain below are entries the filter hides, not missing ones.
            </p>
          )}
          {shown.length === 0 ? (
            <EmptyState title="Nothing matches">
              None of the last {records.length} entries match that. Ask for more
              entries above, or widen the filter.
            </EmptyState>
          ) : (
            <Card className="mt-4 p-4 sm:p-6">
              <ol className="space-y-0">
                {shown.map((r, i) => (
                  <Entry
                    key={r.seq}
                    record={r}
                    first={i === 0}
                    last={i === shown.length - 1}
                    // The page reads newest first, so the severed link is below
                    // the entry the check named.
                    severed={r.seq === brokenAt}
                    // Pruning takes from the oldest end, so a gap in the middle
                    // is missing entries rather than pruned ones -- unless a
                    // filter is on, when a gap is only what it hides.
                    missing={filtering ? 0 : gapBelow(shown, i)}
                    open={expanded === r.seq}
                    onToggle={() => setExpanded(expanded === r.seq ? null : r.seq)}
                  />
                ))}
              </ol>
            </Card>
          )}
        </>
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
  const confirm = useConfirm();
  const mayClear = useCan("history:write");
  const notify = useNotify();
  const [busy, setBusy] = useState(false);

  if (!mayClear) return null;

  async function clear() {
    if (!(await confirm({
      title: "Clear the history?",
      description: "The record of everything so far is removed, and clearing it is itself recorded.",
      action: "Clear it",
    }))) return;
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
