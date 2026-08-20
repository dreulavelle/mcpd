import type { ReactNode } from "react";
import type { AuditRecord, Change, Operation, OperationState, RiskLevel } from "./api";

export function StateChip({ state }: { state: OperationState }) {
  return <span className={`chip ${state}`}>{state.replace(/_/g, " ")}</span>;
}

export function RiskChip({ risk }: { risk: RiskLevel }) {
  return <span className={`chip risk-${risk}`}>{risk}</span>;
}

/**
 * Every state gets a sentence saying what it means for the infrastructure.
 * "approved" and "succeeded" are easy to conflate at a glance, and the
 * difference between them is whether anything has actually changed.
 */
export function stateMeaning(op: Operation): { tone: string; text: string } {
  switch (op.state) {
    case "pending_approval":
      return {
        tone: "warn",
        text: "Nothing has changed yet. This is waiting for a decision.",
      };
    case "approved":
      return {
        tone: "info",
        text: "Approved but not yet applied. It will run shortly, or expire if it cannot.",
      };
    case "executing":
      return { tone: "info", text: "Being applied now." };
    case "succeeded":
      return {
        tone: "ok",
        text: op.verified
          ? "Applied, and confirmed by re-reading the target."
          : "Applied. The outcome was not independently verified.",
      };
    case "failed":
      return { tone: "error", text: "Not applied. The target was left unchanged." };
    case "indeterminate":
      return {
        tone: "error",
        text:
          "Outcome unknown. The change may or may not have been applied. " +
          "Do not retry it — check the device directly and resolve this by hand.",
      };
    case "rejected":
      return { tone: "info", text: "Rejected. Nothing was changed." };
    case "expired":
      return { tone: "info", text: "Expired before it could be applied. Nothing was changed." };
    case "cancelled":
      return { tone: "info", text: "Withdrawn. Nothing was changed." };
    default:
      return { tone: "info", text: "" };
  }
}

export function Banner({ tone, children }: { tone: string; children: ReactNode }) {
  return <div className={`banner ${tone}`}>{children}</div>;
}

export function Diff({ changes }: { changes?: Change[] }) {
  if (!changes?.length) {
    return <p className="muted">No field-level diff was recorded.</p>;
  }
  return (
    <div className="diff">
      {changes.map((c, i) => (
        <div className="diff-row" key={`${c.field}-${i}`}>
          <span className="diff-field">{c.field}</span>
          <span>
            <span className="diff-from">{format(c.from)}</span>
            <span className="diff-arrow">→</span>
            <span className="diff-to">{format(c.to)}</span>
          </span>
        </div>
      ))}
    </div>
  );
}

function format(v: unknown): string {
  if (v === null || v === undefined) return "unset";
  if (typeof v === "string") return v || '""';
  return JSON.stringify(v);
}

export function Json({ value }: { value: unknown }) {
  if (value === null || value === undefined) return null;
  return <pre className="json">{JSON.stringify(value, null, 2)}</pre>;
}

export function AuditTrail({ records }: { records: AuditRecord[] }) {
  if (!records.length) return <p className="empty">No audit entries.</p>;
  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr>
            <th>When</th>
            <th>Event</th>
            <th>Actor</th>
            <th>Transition</th>
          </tr>
        </thead>
        <tbody>
          {records.map((r) => (
            <tr key={r.seq}>
              <td className="mono num">{formatTime(r.at)}</td>
              <td className="mono">{r.kind}</td>
              <td className="mono">{r.actor}</td>
              <td className="mono muted">
                {r.from_state ? `${r.from_state} → ${r.to_state}` : r.to_state || "—"}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

export function formatTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString(undefined, {
    year: "numeric",
    month: "short",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

/** Relative time, so an expiry deadline reads as urgency rather than a date. */
export function relativeTime(iso: string): string {
  const target = new Date(iso).getTime();
  if (Number.isNaN(target)) return iso;

  const deltaSeconds = Math.round((target - Date.now()) / 1000);
  const abs = Math.abs(deltaSeconds);

  const units: [Intl.RelativeTimeFormatUnit, number][] = [
    ["second", 60],
    ["minute", 60],
    ["hour", 24],
    ["day", 7],
  ];

  let value = deltaSeconds;
  let unit: Intl.RelativeTimeFormatUnit = "second";
  let threshold = abs;

  for (const [u, size] of units) {
    unit = u;
    if (Math.abs(value) < size) break;
    value = Math.round(value / size);
    threshold = Math.abs(value);
  }
  void threshold;

  return new Intl.RelativeTimeFormat(undefined, { numeric: "auto" }).format(value, unit);
}
