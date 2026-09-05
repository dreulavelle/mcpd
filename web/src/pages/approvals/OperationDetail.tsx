import { useCallback, useState } from "react";
import { CircleAlert, TriangleAlert } from "lucide-react";
import { api, type AuditRecord, type Operation, problemText } from "@/lib/api";
import { pretty, relative, riskLabel, when, whenExact } from "@/lib/format";
import { useLoader } from "@/lib/hooks";
import { Link, useRouter } from "@/lib/router";
import { useCan } from "@/lib/session";
import { useNotify } from "@/components/toast";
import {
  CodeBlock, Detail, Loading, Notice, PageHeader, Section,
} from "@/components/chrome";
import {
  AssuranceBadge, AuthorisedByRule, RiskBadge, StateBadge, VerifiedBadge,
} from "@/components/status";
import { policyAuthorisation, type PolicyAuthorisation } from "./authorisation";
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

  return (
    <>
      <PageHeader
        title={op.action.replace(/[._]/g, " ")}
        back={{ to: "/approvals", label: "Approvals" }}
        lede={
          <>
            {op.impact && <span className="block">{op.impact}</span>}
            <span className="block">
              A change to{" "}
              <Link to={`/plugins/${encodeURIComponent(op.plugin)}`} className="text-primary hover:underline">
                {op.plugin}
              </Link>
              , proposed by{" "}
              <Link
                to={`/activity?principal=${encodeURIComponent(op.requested_by)}&hours=720`}
                className="text-primary hover:underline"
                title="Everything this caller has done, on Activity"
              >
                {op.requested_by}
              </Link>
              .
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

        {op.state === "failed" && (op.error_detail || op.error_code) && (
          <Notice tone="problem" icon={<CircleAlert />}>
            <strong>It did not run.</strong>{" "}
            {op.error_detail || op.error_code}
            {op.error_detail && op.error_code && (
              <span className="ml-1 font-mono text-xs opacity-80">({op.error_code})</span>
            )}
          </Notice>
        )}

        {authorisation && <AuthorisedInAdvance authorisation={authorisation} />}

        <Section
          title="Where this stands"
          description="What has happened, and what can still happen."
        >
          <Lifecycle operation={op} audit={audit} />
        </Section>

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
                  No list of fields was recorded. What was sent is below.
                </p>
              )}
            </CardContent>
          </Card>
        </Section>

        <Section title="What was sent" description="Recorded when the change was proposed. If it does not match at the moment it runs, it does not run.">
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
        </Section>

        <Section title="Record">
          <Card>
            <CardContent>
              <dl className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
                <Detail label="Proposed by">{op.requested_by}</Detail>
                <Detail label="Proposed at">{whenExact(op.requested_at)}</Detail>
                <Detail label="Proposal expires">
                  {whenExact(op.expires_at)}
                  <span className="block text-xs text-muted-foreground">
                    {relative(op.expires_at)}
                  </span>
                </Detail>
                {/* Never the approver field alone: on an auto-approval it is
                    `system:policy`, which reads as somebody having clicked. */}
                {op.authorized_by_rule ? (
                  <Detail label="Approved by">
                    Rule <code className="font-mono text-xs">{op.authorized_by_rule}</code>
                    <span className="block text-xs text-muted-foreground">
                      No one was asked.{" "}
                      <Link to="/settings/policy" className="text-primary hover:underline">
                        The rules as they stand now
                      </Link>
                      .
                    </span>
                  </Detail>
                ) : op.approved_by ? (
                  <Detail label="Decided by">{op.approved_by}</Detail>
                ) : null}
                {op.approved_at && (
                  <Detail label={op.authorized_by_rule ? "Approved at" : "Decided at"}>
                    {whenExact(op.approved_at)}
                  </Detail>
                )}
                {op.execute_by && (
                  <Detail label="Must run by">
                    {whenExact(op.execute_by)}
                    <span className="block text-xs text-muted-foreground">
                      An approval does not last for ever. After this it expires.
                    </span>
                  </Detail>
                )}
                {op.terminal_at && (
                  <Detail label="Settled at">{whenExact(op.terminal_at)}</Detail>
                )}
                <Detail label="Attempts">{op.attempts}</Detail>
                <Detail label="Outcome confirmed">
                  <VerifiedBadge verified={op.verified} />
                </Detail>
                <Detail label="Checked against how it was before">
                  {op.drift_checked
                    ? "Yes — compared against a record of how the system looked before."
                    : "Nothing recorded how it looked before, so nothing was compared."}
                </Detail>
                <Detail label="Can be confirmed afterwards">
                  {op.outcome_verifiable
                    ? "Reading the system again can prove this change happened."
                    : "This kind of change leaves nothing to read back, so it cannot be confirmed."}
                </Detail>
                <Detail label="Reference" className="sm:col-span-2 lg:col-span-3">
                  <code className="font-mono text-xs break-all">{op.id}</code>
                </Detail>
              </dl>
            </CardContent>
          </Card>
        </Section>

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
      </div>
    </>
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
 * What this record will prove. Silent on a reviewed change: a notice on every
 * operation goes unread on the one that says something else.
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
