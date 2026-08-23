import { useCallback, useEffect, useState } from "react";
import { ScrollText, ShieldAlert } from "lucide-react";
import { api, ApiError, type AuditRecord } from "@/lib/api";
import { describeEvent, pretty, when, who } from "@/lib/format";
import { useLoader } from "@/lib/hooks";
import { Link } from "@/lib/router";
import { useCan } from "@/lib/session";
import { useNotify } from "@/components/toast";
import {
  CodeBlock, EmptyState, Loading, Notice, PageHeader,
} from "@/components/chrome";
import { RiskBadge } from "@/components/status";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { NativeSelect } from "@/components/ui/native-select";
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";

const LIMITS = [100, 250, 500];

/**
 * The audit trail.
 *
 * Append-only and hash-chained in the database, enforced by triggers, so the
 * question this page answers is not only "what happened" but "is what happened
 * still what it says". The chain check is silent when the chain is intact: one
 * that announced success on every visit would train somebody to skip the one
 * time it did not.
 */
export function Audit() {
  const [limit, setLimit] = useState(LIMITS[0]!);
  const [expanded, setExpanded] = useState<number | null>(null);

  const load = useCallback(() => api.audit(limit), [limit]);
  const { data, error, reload } = useLoader(load, "Couldn't load the history.");
  const records = data?.records ?? [];

  return (
    <>
      <PageHeader
        title="Audit"
        lede="Every decision and every transition, in order. The table is append-only and hash-chained; mcpd notices if anything is altered."
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
            <ClearHistory disabled={records.length === 0} onCleared={reload} />
          </div>
        }
      />

      {error && <Notice tone="problem">{error}</Notice>}
      <ChainCheck />

      {data === null && !error ? (
        <Loading rows={6} />
      ) : records.length === 0 ? (
        <EmptyState mark={<ScrollText />} title="Nothing recorded yet">
          Entries appear here as soon as an assistant proposes something or
          somebody decides on it.
        </EmptyState>
      ) : (
        <Card className="mt-4 overflow-hidden p-0">
          <div className="scroll-x">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-16">#</TableHead>
                  <TableHead>When</TableHead>
                  <TableHead>What happened</TableHead>
                  <TableHead>Where</TableHead>
                  <TableHead>Who</TableHead>
                  <TableHead />
                </TableRow>
              </TableHeader>
              <TableBody>
                {records.map((r) => (
                  <Row
                    key={r.seq} record={r}
                    open={expanded === r.seq}
                    onToggle={() => setExpanded(expanded === r.seq ? null : r.seq)}
                  />
                ))}
              </TableBody>
            </Table>
          </div>
        </Card>
      )}
    </>
  );
}

function Row({ record: r, open, onToggle }: {
  record: AuditRecord;
  open: boolean;
  onToggle: () => void;
}) {
  const detail = pretty(r.detail);
  return (
    <>
      <TableRow>
        <TableCell className="font-mono text-xs text-faint tabular-nums">{r.seq}</TableCell>
        <TableCell className="whitespace-nowrap text-muted-foreground">{when(r.at)}</TableCell>
        <TableCell>
          <div className="flex flex-wrap items-center gap-2">
            <span>{describeEvent(r)}</span>
            {r.risk && r.risk !== "low" && <RiskBadge risk={r.risk} />}
          </div>
          {r.from_state && r.to_state && (
            <div className="font-mono text-xs text-muted-foreground">
              {r.from_state} → {r.to_state}
            </div>
          )}
        </TableCell>
        <TableCell className="text-muted-foreground">
          {r.plugin && <div className="text-xs">{r.plugin}</div>}
          {r.operation_id && (
            <Link
              to={`/approvals/${encodeURIComponent(r.operation_id)}`}
              className="font-mono text-xs text-primary hover:underline"
            >
              {r.operation_id.slice(0, 12)}…
            </Link>
          )}
        </TableCell>
        <TableCell className="text-muted-foreground">{who(r.actor)}</TableCell>
        <TableCell className="text-right">
          {detail && (
            <Button variant="ghost" size="xs" onClick={onToggle} aria-expanded={open}>
              {open ? "Hide" : "Detail"}
            </Button>
          )}
        </TableCell>
      </TableRow>
      {open && detail && (
        <TableRow>
          <TableCell colSpan={6} className="bg-muted/40">
            <CodeBlock>{detail}</CodeBlock>
          </TableCell>
        </TableRow>
      )}
    </>
  );
}

/**
 * Whether the chain still verifies.
 *
 * Only an administrator may run the check, so this asks only when one is
 * signed in. Calling it regardless and swallowing the 403 would put a refusal
 * in everyone else's network log for a question they were never allowed to
 * ask.
 */
function ChainCheck() {
  const mayVerify = useCan("admin");
  const [brokenAt, setBrokenAt] = useState<number | null>(null);

  useEffect(() => {
    if (!mayVerify) return;
    let live = true;
    api.verifyAudit()
      .then((c) => { if (live) setBrokenAt(c.intact ? null : c.broken_at); })
      .catch(() => { if (live) setBrokenAt(null); });
    return () => { live = false; };
  }, [mayVerify]);

  if (brokenAt === null) return null;
  return (
    <Notice tone="problem" icon={<ShieldAlert />}>
      <strong>The history has been altered.</strong> Something edited the
      database directly, starting at entry {brokenAt}. Everything from there on
      is no longer evidence of anything.
    </Notice>
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
