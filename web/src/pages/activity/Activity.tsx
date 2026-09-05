import { useCallback, useMemo, useRef, useState } from "react";
import { api, type Caller, type ToolCall, problemText } from "@/lib/api";
import { when, who } from "@/lib/format";
import { usePoll } from "@/lib/hooks";
import { Link, useQueryParam } from "@/lib/router";
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
  ok: { label: "Succeeded", tone: "good" },
  error: { label: "Failed", tone: "problem" },
  denied: { label: "Refused", tone: "attention" },
  rate_limited: { label: "Rate limited", tone: "neutral" },
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

/** Where a caller's identity leads, when it leads anywhere. */
function callerLink(principal: string): string | null {
  if (principal.startsWith("key:")) return "/settings/keys";
  if (principal.startsWith("user:")) return "/settings/users";
  if (principal.startsWith("svc:chatgpt")) return "/settings/chatgpt";
  return null;
}

/**
 * How many calls a page holds. Twenty, not a hundred: the summary above is
 * what somebody arrives to read, and a long list under it pushed the two
 * sections into one another. "Show more" reaches the rest.
 */
const PAGE = 20;

export function Activity() {
  // Every filter lives in the address, so a plugin's page can link to "what
  // has called this" and a reload keeps the view.
  const [hoursParam, setHoursParam] = useQueryParam("hours");
  const [outcome, setOutcome] = useQueryParam("outcome");
  const [principal, setPrincipal] = useQueryParam("principal");
  const [plugin, setPlugin] = useQueryParam("plugin");
  const hours = Number(hoursParam) > 0 ? Number(hoursParam) : 24;
  const setHours = (h: number) => setHoursParam(h === 24 ? "" : String(h));
  // The callers summary is by whole days, which is what its endpoint takes.
  // Said in its heading rather than left to disagree with the calls below.
  const callerDays = Math.max(1, Math.round(hours / 24));

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
      api.calls({ hours, outcome, principal, plugin, limit: PAGE }),
      api.callers(callerDays),
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
        problemText(e, "Couldn't read the call record.")));
  }, [hours, outcome, principal, plugin, callerDays]);
  usePoll(load, 30_000);

  // Every system anybody has reached in the period, for the plugin filter.
  const plugins = useMemo(() => {
    const seen = new Set<string>();
    for (const c of callers ?? []) for (const p of c.plugins ?? []) seen.add(p);
    if (plugin) seen.add(plugin);
    return [...seen].sort();
  }, [callers, plugin]);

  async function more() {
    if (!next || loadingMore) return;
    setLoadingMore(true);
    try {
      const page = await api.calls({ hours, outcome, principal, plugin, limit: PAGE, before: next });
      deeper.current = true;
      setCalls((prev) => {
        const seen = new Set((prev ?? []).map((call) => call.id));
        return [...(prev ?? []), ...page.calls.filter((call) => !seen.has(call.id))];
      });
      setNext(page.next);
    } catch (e) {
      setError(problemText(e, "Couldn't read more."));
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
        <NativeSelect
          aria-label="System"
          className="w-44"
          value={plugin}
          onChange={(e) => setPlugin(e.target.value)}
        >
          <option value="">Every system</option>
          {plugins.map((p) => <option key={p} value={p}>{p}</option>)}
        </NativeSelect>
        {principal && (
          <Button variant="outline" size="sm" title={principal} onClick={() => setPrincipal("")}>
            Clear caller: {who(principal)}
          </Button>
        )}
      </div>

      <div className="space-y-10">
      <Section
        title="Callers"
        description={`What each caller actually reached over the last ${callerDays === 1 ? "day" : `${callerDays} days`}. This is not the same as what it is allowed to reach.`}
      >
        {!callers ? <Loading rows={2} /> : callers.length === 0 ? (
          <Notice tone="neutral">
            Nothing has called a tool in this period. A connector that is set up
            but has not been asked anything looks exactly like this.
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
                          title={c.principal}
                          onClick={() => setPrincipal(c.principal)}
                        >
                          {who(c.principal)}
                        </button>
                        <div className="text-xs text-muted-foreground">
                          {c.role}
                          {callerLink(c.principal) && (
                            <>
                              {c.role ? " · " : ""}
                              <Link to={callerLink(c.principal)!} className="text-primary hover:underline">
                                open
                              </Link>
                            </>
                          )}
                        </div>
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
                            <Link key={p} to={`/plugins/${encodeURIComponent(p)}`}>
                              <Chip tone="neutral" className="hover:border-ring/50">{p}</Chip>
                            </Link>
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
                        <TableCell className="font-mono text-xs">
                          <button
                            type="button"
                            className="underline-offset-2 hover:underline"
                            title={c.principal}
                            onClick={() => setPrincipal(c.principal)}
                          >
                            {who(c.principal)}
                          </button>
                        </TableCell>
                        <TableCell>
                          <Link
                            to={`/plugins/${encodeURIComponent(c.plugin)}`}
                            className="font-mono text-sm hover:underline"
                            title={`Open ${c.plugin}`}
                          >
                            {c.plugin}_{c.tool}
                          </Link>
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
      </div>
    </>
  );
}
