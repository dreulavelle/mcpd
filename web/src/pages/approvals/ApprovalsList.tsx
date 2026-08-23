import { useCallback, useMemo, useState } from "react";
import { ClipboardCheck } from "lucide-react";
import { api, type Operation, type OperationState } from "@/lib/api";
import { relative, when } from "@/lib/format";
import { useLoader } from "@/lib/hooks";
import { Link } from "@/lib/router";
import { EmptyState, Loading, Notice, PageHeader } from "@/components/chrome";
import {
  AssuranceBadge, AuthorisedByRule, RiskBadge, StateBadge, VerifiedBadge,
} from "@/components/status";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { NativeSelect } from "@/components/ui/native-select";
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";

/** "" is every state; the endpoint takes at most one. */
type Filter = "" | OperationState;

const FILTERS: [Filter, string][] = [
  ["pending_approval", "Waiting for a decision"],
  ["approved", "Approved, not yet run"],
  ["executing", "Running"],
  ["indeterminate", "Unknown outcome"],
  ["failed", "Didn't run"],
  ["succeeded", "Applied"],
  ["rejected", "Turned down"],
  ["expired", "Expired"],
  ["cancelled", "Withdrawn"],
  ["", "Everything"],
];

/**
 * Changes an assistant has proposed.
 *
 * The list shows what a change is and where it stands, and nothing else: a
 * decision is made on the detail page, where the change itself is. Approving
 * from a row would be approving a one-line summary, and "the thing approved is
 * exactly the thing reviewed" is the property this product exists to hold.
 *
 * Defaults to what is waiting, because that is the only part of this list
 * anyone is expected to act on.
 */
export function ApprovalsList() {
  const [filter, setFilter] = useState<Filter>("pending_approval");

  const load = useCallback(
    () => api.operations(filter || undefined, 200),
    [filter],
  );
  const { data, error } = useLoader(load, "Couldn't load proposed changes.", 10_000);

  // Newest first. The endpoint walks plugin by plugin, so what comes back is
  // grouped by integration rather than ordered in time, and an operator
  // scanning for what just happened would have to read all of it.
  const operations = useMemo(() => {
    const list = data?.operations ?? [];
    return [...list].sort(
      (a, b) => Date.parse(b.requested_at) - Date.parse(a.requested_at),
    );
  }, [data]);

  return (
    <>
      <PageHeader
        title="Approvals"
        lede="A change an assistant proposed is frozen when it is proposed. What you decide on is exactly what will run."
        actions={
          <NativeSelect
            aria-label="Show"
            className="w-56"
            value={filter}
            onChange={(e) => setFilter(e.target.value as Filter)}
          >
            {FILTERS.map(([value, label]) => (
              <option key={value || "all"} value={value}>{label}</option>
            ))}
          </NativeSelect>
        }
      />

      {error && <Notice tone="problem">{error}</Notice>}

      {data === null && !error ? (
        <Loading rows={5} />
      ) : operations.length === 0 ? (
        <EmptyState mark={<ClipboardCheck />} title="Nothing here">
          {filter === "pending_approval" ? (
            <>
              No change is waiting on a decision.{" "}
              <Button
                variant="link" className="h-auto p-0"
                onClick={() => setFilter("")}
              >
                Show everything
              </Button>
              .
            </>
          ) : (
            "No change matches that."
          )}
        </EmptyState>
      ) : (
        <Card className="overflow-hidden p-0">
          <div className="scroll-x">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>State</TableHead>
                  <TableHead>Change</TableHead>
                  <TableHead>Risk</TableHead>
                  <TableHead>Proposed by</TableHead>
                  <TableHead>When</TableHead>
                  <TableHead>Confirmed</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {operations.map((op) => (
                  <Row key={op.id} op={op} />
                ))}
              </TableBody>
            </Table>
          </div>
        </Card>
      )}
    </>
  );
}

function Row({ op }: { op: Operation }) {
  const waiting = op.state === "pending_approval";
  return (
    <TableRow>
      <TableCell><StateBadge state={op.state} /></TableCell>
      <TableCell>
        <Link
          to={`/approvals/${encodeURIComponent(op.id)}`}
          className="font-medium hover:underline"
        >
          {op.action.replace(/[._]/g, " ")}
        </Link>
        <div className="text-xs text-muted-foreground">
          {op.plugin}
          {op.impact ? ` — ${op.impact}` : ""}
        </div>
        {/* Only the weaker of the two, and in a neutral tone. A gated call is
            an ordinary, legitimate thing rather than a fault; what it is not
            is a reviewed change, and that is worth seeing before opening the
            row. Flagging the stronger case as well would put a chip on every
            line and say nothing.

            The rule beside it is a different fact and stays a different chip:
            what can be proved about a change and who authorised it are
            orthogonal, and an auto-approved change can carry every proof. */}
        {(op.assurance === "gated_call" || op.authorized_by_rule) && (
          <div className="mt-1 flex flex-wrap items-center gap-1.5">
            {op.assurance === "gated_call" && (
              <AssuranceBadge
                assurance={op.assurance}
                authorizedByRule={op.authorized_by_rule}
              />
            )}
            {op.authorized_by_rule && (
              <AuthorisedByRule rule={op.authorized_by_rule} />
            )}
          </div>
        )}
      </TableCell>
      <TableCell><RiskBadge risk={op.risk} /></TableCell>
      <TableCell className="text-muted-foreground">{op.requested_by}</TableCell>
      <TableCell className="whitespace-nowrap text-muted-foreground">
        {when(op.requested_at)}
        {/* A proposal expires, so how long is left is the fact somebody is
            acting on. Everything else can be read from the timestamp. */}
        {waiting && op.expires_at && (
          <div className="text-xs text-attention">
            expires {relative(op.expires_at)}
          </div>
        )}
      </TableCell>
      <TableCell>
        {/* Only meaningful once execution has been attempted. Before that,
            "not checked" is true of every row and says nothing. */}
        {op.state === "succeeded" || op.state === "failed" || op.state === "indeterminate"
          ? <VerifiedBadge verified={op.verified} />
          : <span className="text-xs text-muted-foreground">—</span>}
      </TableCell>
    </TableRow>
  );
}
