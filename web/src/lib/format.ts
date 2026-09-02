import type { AuditRecord, OperationState, RiskLevel } from "./api";

/** A timestamp, in the reader's locale, short enough to sit in a table cell. */
export function when(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString(undefined, {
    month: "short", day: "numeric", hour: "2-digit", minute: "2-digit",
  });
}

/** The same, with the year and seconds, for a detail page. */
export function whenExact(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString(undefined, {
    year: "numeric", month: "short", day: "numeric",
    hour: "2-digit", minute: "2-digit", second: "2-digit",
  });
}

/** How long until a moment, or how long since. */
export function relative(iso: string, now: number = Date.now()): string {
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return iso;
  const seconds = Math.round((t - now) / 1000);
  const abs = Math.abs(seconds);

  // Coarse to fine, so the first match is the largest unit that fits.
  const scales: [Intl.RelativeTimeFormatUnit, number][] = [
    ["year", 31_536_000],
    ["month", 2_592_000],
    ["week", 604_800],
    ["day", 86_400],
    ["hour", 3_600],
    ["minute", 60],
  ];
  const format = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });

  for (const [unit, size] of scales) {
    if (abs >= size) return format.format(Math.round(seconds / size), unit);
  }
  return format.format(seconds, "second");
}

/** Strips the machine prefix off an identity. */
export function who(actor: string): string {
  if (actor.startsWith("system:")) return "mcpd";
  return actor.replace(/^(user|svc):/, "");
}

/** Turns an audit event name into something readable. */
export function describeEvent(r: AuditRecord): string {
  const what = r.action ? r.action.replace(/[._]/g, " ") : "";
  switch (r.kind) {
    case "operation.proposed": return what ? `Suggested: ${what}` : "Suggested a change";
    case "operation.approved": return what ? `Approved: ${what}` : "Approved a change";
    case "operation.rejected": return "Turned down a change";
    case "operation.cancelled": return "Withdrew a change";
    case "operation.expired": return "A change ran out of time";
    case "operation.executing": return what ? `Applying: ${what}` : "Applying a change";
    case "operation.succeeded": return what ? `Applied: ${what}` : "Applied a change";
    case "operation.failed": return "A change didn't run";
    case "operation.indeterminate": return "A change ended up in an unknown state";
    default: return r.kind.replace(/[._]/g, " ");
  }
}

/**
 * What each state is called and what it means. `indeterminate` is not
 * `failed`: reading it as settled invites a retry that applies the change twice.
 */
export const OPERATION_STATES: Record<OperationState, { label: string; meaning: string }> = {
  draft: { label: "Draft", meaning: "Being put together. Not asking for anything yet." },
  pending_approval: { label: "Waiting", meaning: "Proposed, and waiting for somebody to decide." },
  approved: { label: "Approved", meaning: "Cleared to run. It has not run yet." },
  executing: { label: "Running", meaning: "Being applied now." },
  succeeded: { label: "Applied", meaning: "It ran, and the change is in place." },
  failed: { label: "Didn't run", meaning: "It did not run. Nothing changed upstream." },
  indeterminate: {
    label: "Unknown",
    meaning:
      "Execution began and the outcome was never recorded. The change may " +
      "have landed. Check upstream before proposing it again — a retry would " +
      "apply it twice.",
  },
  rejected: { label: "Turned down", meaning: "Somebody decided against it." },
  expired: { label: "Expired", meaning: "Nobody decided in time." },
  cancelled: { label: "Withdrawn", meaning: "Whoever proposed it took it back." },
};

export function stateLabel(state: OperationState | string): string {
  return OPERATION_STATES[state as OperationState]?.label ?? String(state);
}

export const RISK_LABELS: Record<RiskLevel, string> = {
  low: "Low", medium: "Medium", high: "High", critical: "Critical",
};

/** A risk level as a word. A level this build does not know renders as itself. */
export function riskLabel(risk: string): string {
  return RISK_LABELS[risk as RiskLevel] ?? risk;
}

/** Tool names carry their plugin prefix; a page about one plugin already says which. */
export function unprefixed(tool: string, plugin: string): string {
  return tool.startsWith(plugin + "_") ? tool.slice(plugin.length + 1) : tool;
}

/** Pretty-prints a decoded JSON value for display. */
export function pretty(value: unknown): string {
  if (value === undefined || value === null) return "";
  if (typeof value === "string") return value;
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}
