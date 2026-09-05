import { useCallback, useState, type ReactNode } from "react";
import { CircleAlert, TriangleAlert } from "lucide-react";
import { api, type AuditRecord, type Operation, problemText } from "@/lib/api";
import {
  changeDelta, describeChange, pretty, relative, riskLabel, when, whenExact,
} from "@/lib/format";
import { useLoader } from "@/lib/hooks";
import { Link, useRouter } from "@/lib/router";
import { useCan } from "@/lib/session";
import { useNotify } from "@/components/toast";
import {
  CodeBlock, Detail, Loading, Notice, PageHeader, Section,
} from "@/components/chrome";
import { Disclosure } from "@/components/disclosure";
import { usePrincipalNames } from "@/components/principal";
import {
  AssuranceBadge, AuthorisedByRule, RiskBadge, StateBadge,
} from "@/components/status";
import { policyAuthorisation, type PolicyAuthorisation } from "./authorisation";
import { FieldDelta } from "./delta";
import { Lifecycle } from "./Lifecycle";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Separator } from "@/components/ui/separator";
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";

/** One proposed change, in full, and where the decision is taken. */
export function OperationDetail({ id }: { id: string }) {
  const load = useCallback(() => api.operation(id), [id]);
  const { data, error, reload } = useLoader(load, "Couldn't load that change.", 10_000);

  if (error) {
    return (
      <>
        <PageHeader title="Change" back={{ to: "/approvals", label: "Approvals" }} />
        <Notice tone="problem">{error}</Notice>
      </>
    );
  }
  if (!data) {
    return (
      <>
        <PageHeader title="Change" back={{ to: "/approvals", label: "Approvals" }} />
        <Loading rows={6} />
      </>
    );
  }

  return <Body operation={data.operation} audit={data.audit ?? []} onChanged={reload} />;
}

function Body({ operation: op, audit, onChanged }: {
  operation: Operation;
  audit: AuditRecord[];
  onChanged: () => void;
}) {
  const authorisation = policyAuthorisation(op, audit);
  const name = usePrincipalNames();
  const { headline } = describeChange(op);
  const delta = changeDelta(op);

  return (
    <>
      <PageHeader
        // The change, not `label.set`. The machine's name for it is under
        // Technical details, with the id and what was sent.
        title={headline}
        back={{ to: "/approvals", label: "Approvals" }}
        lede={
          <>
            {delta && <span className="block"><FieldDelta delta={delta} /></span>}
            {op.impact && <span className="block">{op.impact}</span>}
            {/* The system is already the last two words of the heading, so
                the lede names it as a place to go rather than saying it
                again. */}
            <span className="block">
              Proposed by{" "}
              <Link
                to={`/activity?principal=${encodeURIComponent(op.requested_by)}&hours=720`}
                className="text-primary hover:underline"
                title="Everything this caller has done, on Activity"
              >
                {name(op.requested_by, op.requested_by_name)}
              </Link>
              .{" "}
              <Link
                to={`/plugins/${encodeURIComponent(op.plugin)}`}
                className="text-primary hover:underline"
              >
                About {op.plugin}
              </Link>
            </span>
          </>
        }
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <AssuranceBadge
              assurance={op.assurance}
              authorizedByRule={op.authorized_by_rule}
            />
            <RiskBadge risk={op.risk} />
            <StateBadge state={op.state} />
          </div>
        }
      />

      <div className="space-y-6">
        {op.state === "indeterminate" && (
          <Notice tone="attention" icon={<TriangleAlert />}>
            <strong>This may have landed.</strong> It started, and nobody
            recorded what happened, which is not the same as failing. Check the
            system before proposing this again — a retry would apply the change
            a second time.
          </Notice>
        )}

        {op.state === "failed" && (
          <Notice tone="problem" icon={<CircleAlert />}>
            <strong>It did not run.</strong> Nothing changed.
            {/* Quoted, because it is what the system said rather than what
                this page has to say. The code beside it is evidence and goes
                under Technical details. */}
            {op.error_detail && <> The system said: “{op.error_detail}”</>}
          </Notice>
        )}

        {authorisation && <AuthorisedInAdvance authorisation={authorisation} />}

        <WhatThisProves operation={op} />

        <Decide operation={op} onChanged={onChanged} />

        <Section title="What changes">
          <Card>
            <CardContent>
              {op.changes && op.changes.length > 0 ? (
                <div className="scroll-x -mx-2">
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>Field</TableHead>
                        <TableHead>From</TableHead>
                        <TableHead>To</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {op.changes.map((c, i) => (
                        <TableRow key={`${c.field}-${i}`}>
                          <TableCell className="font-mono text-xs">{c.field}</TableCell>
                          <TableCell className="font-mono text-xs text-muted-foreground">
                            {pretty(c.from) || "—"}
                          </TableCell>
                          <TableCell className="font-mono text-xs">
                            {pretty(c.to) || "—"}
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </div>
              ) : (
                <p className="text-sm text-muted-foreground">
                  No list of fields was recorded. What was sent is under
                  Technical details.
                </p>
              )}
            </CardContent>
          </Card>
        </Section>

        <Section
          title="Where this stands"
          description="What has happened, what can still happen, and what the record proves."
        >
          <Lifecycle operation={op} audit={audit} />
        </Section>

        <WhoAndWhen operation={op} audit={audit} name={name} />

        <Section title="History" description="Every step this change went through.">
          <Card className="overflow-hidden p-0">
            {audit.length === 0 ? (
              <p className="p-4 text-sm text-muted-foreground">
                Nothing recorded against this change.
              </p>
            ) : (
              <div className="scroll-x">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>When</TableHead>
                      <TableHead>Event</TableHead>
                      <TableHead>Moved</TableHead>
                      <TableHead>Who</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {audit.map((r) => (
                      <TableRow key={r.seq}>
                        <TableCell className="whitespace-nowrap text-muted-foreground">
                          {when(r.at)}
                        </TableCell>
                        <TableCell className="font-mono text-xs">{r.kind}</TableCell>
                        <TableCell className="text-xs text-muted-foreground">
                          {r.from_state && r.to_state
                            ? `${r.from_state} → ${r.to_state}`
                            : "—"}
                        </TableCell>
                        <TableCell className="text-muted-foreground">{r.actor}</TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
            )}
          </Card>
        </Section>

        <Technical operation={op} />
      </div>
    </>
  );
}

/**
 * Who did what, and when, as sentences rather than a grid of labelled cells.
 *
 * Every fact the old Record grid carried is here except the three it shared
 * with the lifecycle's proofs, which say the same things in more words a few
 * inches above, and the id and the attempt count, which are evidence.
 */
function WhoAndWhen({ operation: op, audit, name }: {
  operation: Operation;
  audit: AuditRecord[];
  name: (actor: string, resolved?: string) => string;
}) {
  const lines: ReactNode[] = [];

  lines.push(
    <>
      {name(op.requested_by, op.requested_by_name)} proposed this{" "}
      <Moment iso={op.requested_at} />.
    </>,
  );

  // A deadline only means something while it can still be missed, and once a
  // change is approved nobody is going to approve it again -- the deadline
  // that matters from there is `execute_by`, two sentences down.
  if (op.state === "pending_approval" && op.expires_at) {
    lines.push(
      <>
        The proposal runs out <Moment iso={op.expires_at} />. After that nobody
        can approve it.
      </>,
    );
  }

  // Never the approver field alone: on an auto-approval it is `system:policy`,
  // which is not an account and reads as somebody having clicked.
  if (op.authorized_by_rule) {
    lines.push(
      <>
        A standing rule approved it
        {op.approved_at && <> <Moment iso={op.approved_at} /></>}, with nobody
        asked.{" "}
        <Link to="/settings/policy" className="text-primary hover:underline">
          The rules as they stand now
        </Link>
        .
      </>,
    );
  } else if (op.approved_by) {
    lines.push(
      <>
        {name(op.approved_by, op.approved_by_name)} approved it
        {op.approved_at && <> <Moment iso={op.approved_at} /></>}.
      </>,
    );
  }

  // Turning down and withdrawing are their own entries with their own actors.
  // `approved_by` is whoever approved it, so on a change that was approved and
  // then withdrawn it named the approver as the person who withdrew it -- two
  // different people, and the record says so. Where the trail has been cleared
  // the sentence loses its subject rather than borrowing one.
  const settler = settlement(op, audit);
  if (settler) {
    lines.push(
      settler.actor
        ? <>{name(settler.actor)} {settler.verb} <Moment iso={settler.at} />.</>
        : <>It was {settler.passive} <Moment iso={settler.at} />.</>,
    );
  }

  if (!op.terminal && op.execute_by) {
    lines.push(
      <>
        The approval itself runs out <Moment iso={op.execute_by} />. After that
        the change will not run, even though somebody said yes.
      </>,
    );
  }

  // Only where the settling was not already said above, which it is for a
  // change somebody turned down or withdrew.
  if (op.terminal_at && !settler) {
    lines.push(<>It settled <Moment iso={op.terminal_at} />.</>);
  }

  return (
    <Section title="Who and when">
      <Card>
        <CardContent>
          <ul className="space-y-2 text-sm">
            {lines.map((line, i) => <li key={i}>{line}</li>)}
          </ul>
        </CardContent>
      </Card>
    </Section>
  );
}

/**
 * Who turned a change down or withdrew it, out of the trail that recorded it.
 *
 * Neither writes `approved_by`, so the operation row alone cannot say. The
 * audit entry can, and where it has been pruned the sentence says what
 * happened without inventing somebody to have done it.
 */
function settlement(op: Operation, audit: AuditRecord[]): {
  actor: string;
  verb: string;
  passive: string;
  at: string;
} | null {
  const kinds: Partial<Record<string, { verb: string; passive: string }>> = {
    rejected: { verb: "turned it down", passive: "turned down" },
    cancelled: { verb: "withdrew it", passive: "withdrawn" },
  };
  const words = kinds[op.state];
  if (!words) return null;

  const entry = audit.find((r) => r.kind === `operation.${op.state}`);
  const at = entry?.at ?? op.terminal_at;
  if (!at) return null;
  return { actor: entry?.actor ?? "", verb: words.verb, passive: words.passive, at };
}

/** How long ago, with the exact time one hover away rather than in the line. */
function Moment({ iso }: { iso: string }) {
  return <time dateTime={iso} title={whenExact(iso)}>{relative(iso)}</time>;
}

/**
 * Evidence: the machine's name for the change, the id somebody quotes back,
 * how many times it was tried, an error code, and what was actually sent.
 * Closed, because none of it is what the page is about.
 */
function Technical({ operation: op }: { operation: Operation }) {
  return (
    <Disclosure summary="Technical details">
      <dl className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        <Detail label="Action">
          <code className="font-mono text-xs">{op.action}</code>
        </Detail>
        <Detail label="Attempts">{op.attempts}</Detail>
        {op.error_code && (
          <Detail label="Error code">
            <code className="font-mono text-xs">{op.error_code}</code>
          </Detail>
        )}
        {op.authorized_by_rule && (
          <Detail label="Rule">
            <code className="font-mono text-xs">{op.authorized_by_rule}</code>
          </Detail>
        )}
        <Detail label="Reference" className="sm:col-span-2 lg:col-span-3">
          <code className="font-mono text-xs break-all">{op.id}</code>
        </Detail>
      </dl>

      <div>
        <p className="mb-2 text-sm text-muted-foreground">
          What was sent, recorded when the change was proposed. If it does not
          match at the moment it runs, it does not run.
        </p>
        <div className="grid gap-3 lg:grid-cols-2">
          <Payload label="What it changes" value={op.target} />
          <Payload label="What it should become" value={op.desired} />
          <Payload label="How it was before" value={op.before} />
          <Payload
            label="How it looked afterwards"
            value={op.observed}
            empty="Nobody has looked yet."
          />
        </div>
      </div>
    </Disclosure>
  );
}

/**
 * The rule that approved this, as it stood -- from the audit entry, not the
 * policy endpoint, because the rule may have changed since.
 */
function AuthorisedInAdvance({ authorisation: a }: {
  authorisation: PolicyAuthorisation;
}) {
  return (
    <Section title="Approved by a rule">
      <Card>
        <CardContent className="space-y-3">
          <AuthorisedByRule rule={a.rule} />
          {a.reason && <p className="text-sm">{a.reason}</p>}
          {a.recorded ? (
            <dl className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
              <Detail label="What it covered">
                <code className="font-mono text-xs">{a.scope ?? "not recorded"}</code>
              </Detail>
              <Detail label="Up to">
                {a.maxRisk ? riskLabel(a.maxRisk) : "not recorded"}
              </Detail>
              <Detail label="Note">
                {a.note ?? <span className="text-muted-foreground">None was written.</span>}
              </Detail>
            </dl>
          ) : (
            <p className="text-sm text-muted-foreground">
              The audit entry for this is gone, so what the rule covered
              can't be shown. A rule with that name today may say something
              different.
            </p>
          )}
        </CardContent>
      </Card>
    </Section>
  );
}

/**
 * What this record will prove, said before the decision rather than after it.
 * Silent on a reviewed change: a notice on every operation goes unread on the
 * one that says something else.
 */
function WhatThisProves({ operation: op }: { operation: Operation }) {
  if (op.assurance === "reviewed_change") return null;

  const missing: string[] = [];
  if (!op.drift_checked) {
    missing.push("nothing recorded how the system looked before, so nothing will be compared");
  }
  if (!op.outcome_verifiable) {
    missing.push("its result cannot be confirmed by reading the system again");
  }

  return (
    <Notice tone="info">
      <strong>This is a gated call, not a reviewed change.</strong>{" "}
      {op.authorized_by_rule
        // Nobody is about to approve this one, and nobody did.
        ? <>A rule allowed it and the call was made, but </>
        : <>Approving it records that a person allowed it and that the call was
            made, but </>}
      {missing.join(", and ")}. The record will not say the change is in place.
    </Notice>
  );
}

function Payload({ label, value, empty }: {
  label: string;
  value: unknown;
  empty?: string;
}) {
  const text = pretty(value);
  return (
    <div className="space-y-1.5">
      <p className="text-xs font-medium tracking-wide text-muted-foreground uppercase">
        {label}
      </p>
      {text
        ? <CodeBlock>{text}</CodeBlock>
        : <p className="text-sm text-muted-foreground">{empty ?? "Not recorded."}</p>}
    </div>
  );
}

/**
 * Deciding. Approve and reject need `approve`, withdrawing only `propose`.
 * Hiding them is so nobody types a reason into a form that answers 403.
 */
function Decide({ operation: op, onChanged }: {
  operation: Operation;
  onChanged: () => void;
}) {
  const mayApprove = useCan("approvals:decide");
  const mayPropose = useCan("approvals:read");
  const notify = useNotify();
  const { navigate } = useRouter();
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState<"approve" | "reject" | "cancel" | null>(null);

  const waiting = op.state === "pending_approval";
  // Withdrawing is only meaningful while the change could still run. Once it
  // has settled there is nothing to take back.
  const withdrawable = waiting || op.state === "approved" || op.state === "draft";

  const showApprove = waiting && mayApprove;
  const showWithdraw = withdrawable && mayPropose;

  async function run(
    kind: "approve" | "reject" | "cancel",
    call: () => Promise<unknown>,
    done: string,
  ) {
    setBusy(kind);
    try {
      await call();
      notify("good", done);
      setReason("");
      onChanged();
    } catch (e) {
      notify("problem", problemText(e, "That didn't work."));
    } finally {
      setBusy(null);
    }
  }

  if (!showApprove && !showWithdraw) {
    if (!waiting) return null;
    return (
      <Notice tone="neutral">
        This change is waiting on somebody who may approve it. Your account
        cannot.
      </Notice>
    );
  }

  return (
    <Card>
      <CardContent className="space-y-4">
        <div className="space-y-1.5">
          <Label htmlFor="decision-reason">
            Reason {showApprove ? <span className="text-muted-foreground">(recorded either way)</span> : null}
          </Label>
          <Input
            id="decision-reason"
            value={reason}
            placeholder="Why you are deciding this way"
            onChange={(e) => setReason(e.target.value)}
          />
        </div>

        <Separator />

        <div className="flex flex-wrap gap-2">
          {showApprove && (
            <>
              <Button
                disabled={busy !== null}
                onClick={() => run("approve", () => api.approve(op.id, reason), "Approved.")}
              >
                {busy === "approve" ? "Approving…" : "Approve"}
              </Button>
              <Button
                variant="outline"
                disabled={busy !== null}
                onClick={() => run("reject", () => api.reject(op.id, reason), "Turned down.")}
              >
                {busy === "reject" ? "Turning down…" : "Turn down"}
              </Button>
            </>
          )}
          {showWithdraw && (
            <Button
              variant="ghost"
              className="text-destructive hover:text-destructive"
              disabled={busy !== null}
              onClick={() => run("cancel", async () => {
                await api.cancel(op.id, reason);
                navigate("/approvals");
              }, "Withdrawn.")}
            >
              {busy === "cancel" ? "Withdrawing…" : "Withdraw"}
            </Button>
          )}
        </div>
      </CardContent>
    </Card>
  );
}
