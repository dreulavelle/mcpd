import { useCallback, useState } from "react";
import { CircleAlert, TriangleAlert } from "lucide-react";
import { api, ApiError, type AuditRecord, type Operation } from "@/lib/api";
import { pretty, relative, when, whenExact } from "@/lib/format";
import { useLoader } from "@/lib/hooks";
import { useRouter } from "@/lib/router";
import { useCan } from "@/lib/session";
import { useNotify } from "@/components/toast";
import {
  CodeBlock, Detail, Loading, Notice, PageHeader, Section,
} from "@/components/chrome";
import {
  AssuranceBadge, RiskBadge, StateBadge, VerifiedBadge,
} from "@/components/status";
import { Lifecycle } from "./Lifecycle";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Separator } from "@/components/ui/separator";
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";

/**
 * One proposed change, in full.
 *
 * Everything a decision rests on is here, and the decision is taken here too.
 * The order is deliberate: what changes, then what it was planned against,
 * then who asked and by when — a reviewer reads down and then acts, rather
 * than acting from a summary at the top.
 */
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
  return (
    <>
      <PageHeader
        title={op.action.replace(/[._]/g, " ")}
        back={{ to: "/approvals", label: "Approvals" }}
        lede={op.impact || undefined}
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <AssuranceBadge assurance={op.assurance} />
            <RiskBadge risk={op.risk} />
            <StateBadge state={op.state} />
          </div>
        }
      />

      <div className="space-y-6">
        {op.state === "indeterminate" && (
          <Notice tone="attention" icon={<TriangleAlert />}>
            <strong>This may have landed.</strong> Execution began and the
            outcome was never recorded, which is not the same as failing. Read
            the target upstream before proposing this again — a retry would
            apply the change a second time.
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

        <Section
          title="Where this stands"
          description="What has happened, what can still happen, and what the record will prove afterwards."
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
                  No field-level summary was recorded. The full payload is below.
                </p>
              )}
            </CardContent>
          </Card>
        </Section>

        <Section title="Payload" description="Frozen when the change was proposed, and hashed. A payload that differs at execution time matches nothing and the change does not run.">
          <div className="grid gap-3 lg:grid-cols-2">
            <Payload label="Target" value={op.target} />
            <Payload label="Desired" value={op.desired} />
            <Payload label="Before" value={op.before} />
            <Payload
              label="Observed after"
              value={op.observed}
              empty="Nothing has been re-read yet."
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
                {op.approved_by && <Detail label="Decided by">{op.approved_by}</Detail>}
                {op.approved_at && (
                  <Detail label="Decided at">{whenExact(op.approved_at)}</Detail>
                )}
                {op.execute_by && (
                  <Detail label="Must run by">
                    {whenExact(op.execute_by)}
                    <span className="block text-xs text-muted-foreground">
                      An approval is not open-ended; after this it expires.
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
                <Detail label="Drift checked">
                  {op.drift_checked
                    ? "Yes — planned against a stored snapshot."
                    : "No snapshot was declared, so nothing was compared."}
                </Detail>
                <Detail label="Outcome verifiable">
                  {op.outcome_verifiable
                    ? "Re-reading the target proves this write."
                    : "This kind of change cannot be confirmed by re-reading."}
                </Detail>
                <Detail label="Reference" className="sm:col-span-2 lg:col-span-3">
                  <code className="font-mono text-xs break-all">{op.id}</code>
                </Detail>
              </dl>
            </CardContent>
          </Card>
        </Section>

        <Section title="History" description="Every transition this change went through, from the audit trail.">
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
 * What this record will be able to prove afterwards.
 *
 * Placed above the decision rather than in the record below it, because it is
 * a fact about the change that ought to inform the decision: approving
 * something whose outcome cannot be confirmed is a different act from
 * approving something that will be re-read and compared, and the operator
 * should know which one they are performing before they perform it.
 *
 * Silent on a reviewed change. Saying "all three proofs present" on every
 * ordinary operation is how a notice becomes furniture, and then goes unread
 * on the one that says something else.
 */
function WhatThisProves({ operation: op }: { operation: Operation }) {
  if (op.assurance === "reviewed_change") return null;

  const missing: string[] = [];
  if (!op.drift_checked) {
    missing.push("it declares no precondition snapshot, so nothing will be compared for drift");
  }
  if (!op.outcome_verifiable) {
    missing.push("its outcome cannot be confirmed by re-reading the target");
  }

  return (
    <Notice tone="info">
      <strong>This is a gated call, not a reviewed change.</strong> Approving it
      records that a person authorised it and that the call was made —{" "}
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
 * Deciding.
 *
 * Approve and reject need the approve capability; withdrawing needs only
 * propose, because taking back your own suggestion is not the same act as
 * authorising somebody else's. Both are checked again on the server; hiding
 * the buttons is so nobody types a reason into a form that will answer 403.
 */
function Decide({ operation: op, onChanged }: {
  operation: Operation;
  onChanged: () => void;
}) {
  const mayApprove = useCan("approve");
  const mayPropose = useCan("propose");
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
      notify("problem", e instanceof ApiError ? e.detail : "That didn't work.");
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
