import { useCallback, useRef, useState } from "react";
import { api, ApiError, type Caller, type ToolCall } from "@/lib/api";
import { when } from "@/lib/format";
import { usePoll } from "@/lib/hooks";
import { EmptyState, Loading, Notice, PageHeader, Section } from "@/components/chrome";
import { Chip } from "@/components/status";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { NativeSelect } from "@/components/ui/native-select";
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";

/**
 * Who called what.
 *
 * The counters on the Performance page can say a tool was called four hundred
 * times; they cannot say who called it, because a metric labelled by credential
 * is unbounded cardinality. The audit trail records administrative acts and
 * mutations, not ordinary reads. This is the third question — and the one an
 * incident actually asks.
 *
 * Callers first, calls second, because the summary is what somebody scanning
 * arrives to answer: is anything using this host that should not be, and is any
 * credential doing more than it was issued for.
 */
const OUTCOMES: Record<ToolCall["outcome"], { label: string; tone: "good" | "problem" | "attention" | "neutral" }> = {
  ok: { label: "ok", tone: "good" },
  error: { label: "error", tone: "problem" },
  denied: { label: "denied", tone: "attention" },
  rate_limited: { label: "rate limited", tone: "neutral" },
};

/**
 * How long a call took, in words that do not overstate what was measured.
 *
 * An absent duration is a call that never ran, and an em dash says so. A call
 * that ran in under a millisecond gets "<1 ms" rather than "0 ms", because a
 * zero would read as the same nothing the em dash means.
 */
function took(us?: number): string {
  if (us === undefined) return "—";
  if (us < 1000) return "<1 ms";
  return `${Math.round(us / 1000)} ms`;
}

export function Activity() {
  const [hours, setHours] = useState(24);
  const [outcome, setOutcome] = useState("");
  const [principal, setPrincipal] = useState("");

  const [calls, setCalls] = useState<ToolCall[] | null>(null);
  const [callers, setCallers] = useState<Caller[] | null>(null);
  const [error, setError] = useState("");
  const [next, setNext] = useState("");
  const [loadingMore, setLoadingMore] = useState(false);
  // Whether pages beyond the first have been asked for. The poll refreshes
  // the head of the list; it must not throw those pages away, nor reset the
  // cursor to the first page's, or "Show more" undoes itself within thirty
  // seconds. Cleared when the filters change, because then the list is new.
  const deeper = useRef(false);

  const load = useCallback(() => {
    deeper.current = false;
    Promise.all([
      api.calls({ hours, outcome, principal, limit: 100 }),
      api.callers(Math.max(1, Math.round(hours / 24))),
    ])
      .then(([c, k]) => {
        setCalls((prev) => {
          if (!prev || !deeper.current) return c.calls;
          // New calls go on the front; what was already loaded stays.
          const seen = new Set(c.calls.map((call) => call.id));
          return [...c.calls, ...prev.filter((call) => !seen.has(call.id))];
        });
        setNext((cursor) => (deeper.current ? cursor : c.next));
        setCallers(k.callers);
        setError("");
      })
      .catch((e) => setError(
        e instanceof ApiError ? e.detail : "Couldn't read the call record."));
  }, [hours, outcome, principal]);
  usePoll(load, 30_000);

  async function more() {
    if (!next || loadingMore) return;
    setLoadingMore(true);
    try {
      const page = await api.calls({ hours, outcome, principal, limit: 100, before: next });
      deeper.current = true;
      setCalls((prev) => {
        const seen = new Set((prev ?? []).map((call) => call.id));
        return [...(prev ?? []), ...page.calls.filter((call) => !seen.has(call.id))];
      });
      setNext(page.next);
    } catch (e) {
      setError(e instanceof ApiError ? e.detail : "Couldn't read more.");
    } finally {
      setLoadingMore(false);
    }
  }

  return (
    <>
      <PageHeader
        title="Activity"
        lede="Every tool call this host served, and who made it."
      />

      {error && <Notice tone="problem">{error}</Notice>}

      <div className="mb-4 flex flex-wrap items-end gap-2">
        <NativeSelect
          aria-label="Period"
          className="w-40"
          value={String(hours)}
          onChange={(e) => setHours(Number(e.target.value))}
        >
          <option value="1">Last hour</option>
          <option value="24">Last 24 hours</option>
          <option value="168">Last 7 days</option>
          <option value="720">Last 30 days</option>
        </NativeSelect>
        <NativeSelect
          aria-label="Outcome"
          className="w-40"
          value={outcome}
          onChange={(e) => setOutcome(e.target.value)}
        >
          <option value="">Every outcome</option>
          <option value="ok">Succeeded</option>
          <option value="error">Failed</option>
          <option value="denied">Refused</option>
          <option value="rate_limited">Rate limited</option>
        </NativeSelect>
        {principal && (
          <Button variant="outline" size="sm" onClick={() => setPrincipal("")}>
            Clear filter: {principal}
          </Button>
        )}
      </div>

      <Section
        title="Callers"
        description="What each credential actually reached, which is not the same as what it is permitted to reach."
      >
        {!callers ? <Loading rows={2} /> : callers.length === 0 ? (
          <Notice tone="neutral">
            Nothing has called a tool in this period. That is the right state for
            a host nobody is using yet — a connector that has been set up but not
            asked anything looks exactly like this.
          </Notice>
        ) : (
          <Card className="overflow-hidden p-0">
            <div className="scroll-x">
              <Table aria-label="Callers">
                <TableHeader>
                  <TableRow>
                    <TableHead>Caller</TableHead>
                    <TableHead className="text-right">Calls</TableHead>
                    <TableHead className="text-right">Failed</TableHead>
                    <TableHead className="text-right">Refused</TableHead>
                    <TableHead>Reached</TableHead>
                    <TableHead>Last seen</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {callers.map((c) => (
                    <TableRow key={c.principal}>
                      <TableCell>
                        <button
                          type="button"
                          className="font-mono text-sm underline-offset-2 hover:underline"
                          onClick={() => setPrincipal(c.principal)}
                        >
                          {c.principal}
                        </button>
                        {c.role && (
                          <div className="text-xs text-muted-foreground">{c.role}</div>
                        )}
                      </TableCell>
                      <TableCell className="text-right tabular-nums">{c.calls}</TableCell>
                      <TableCell className="text-right tabular-nums">
                        {c.errors > 0
                          ? <span className="text-problem">{c.errors}</span>
                          : <span className="text-muted-foreground">0</span>}
                      </TableCell>
                      <TableCell className="text-right tabular-nums">
                        {c.denied > 0
                          ? <span className="text-attention">{c.denied}</span>
                          : <span className="text-muted-foreground">0</span>}
                      </TableCell>
                      <TableCell>
                        <div className="flex flex-wrap gap-1">
                          {(c.plugins ?? []).map((p) => (
                            <Chip key={p} tone="neutral">{p}</Chip>
                          ))}
                        </div>
                      </TableCell>
                      <TableCell className="whitespace-nowrap text-sm text-muted-foreground">
                        {when(c.last_seen)}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          </Card>
        )}
      </Section>

      <Section title="Calls" description="Newest first. Arguments and results are never recorded.">
        {!calls ? <Loading rows={4} /> : calls.length === 0 ? (
          <EmptyState title="No calls">
            Nothing matched. Try a longer period, or another outcome.
          </EmptyState>
        ) : (
          <>
            <Card className="overflow-hidden p-0">
              <div className="scroll-x">
                <Table aria-label="Calls">
                  <TableHeader>
                    <TableRow>
                      <TableHead>When</TableHead>
                      <TableHead>Caller</TableHead>
                      <TableHead>Tool</TableHead>
                      <TableHead>Outcome</TableHead>
                      <TableHead className="text-right">Took</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {calls.map((c) => (
                      <TableRow key={c.id}>
                        <TableCell className="whitespace-nowrap text-sm text-muted-foreground">
                          {when(c.at)}
                        </TableCell>
                        <TableCell className="font-mono text-xs">{c.principal}</TableCell>
                        <TableCell>
                          <span className="font-mono text-sm">{c.plugin}_{c.tool}</span>
                        </TableCell>
                        <TableCell>
                          <Chip tone={OUTCOMES[c.outcome]?.tone ?? "neutral"}>
                            {OUTCOMES[c.outcome]?.label ?? c.outcome}
                          </Chip>
                        </TableCell>
                        <TableCell className="text-right tabular-nums text-sm text-muted-foreground">
                          {took(c.duration_us)}
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
            </Card>
            {next && (
              <div className="mt-3">
                <Button variant="outline" size="sm" onClick={more} disabled={loadingMore}>
                  Show more
                </Button>
              </div>
            )}
          </>
        )}
      </Section>
    </>
  );
}
