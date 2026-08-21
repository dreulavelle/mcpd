import { useCallback, useEffect, useState, type ReactNode } from "react";
import type { AuditRecord, Change, Operation, OperationState, RiskLevel } from "./api";

/* ── status ─────────────────────────────────────────────────────────────── */

export type Tone = "good" | "attention" | "problem" | "info" | "busy" | "";

export function Dot({ tone }: { tone: Tone }) {
  return <span className={`dot ${tone}`} aria-hidden="true" />;
}

export function Pill({ tone = "", loud, children }: { tone?: Tone; loud?: boolean; children: ReactNode }) {
  return <span className={`pill ${loud ? "loud" : tone}`}>{children}</span>;
}

/** How a change's state should read, and how loudly. */
export function stateTone(state: OperationState): Tone {
  switch (state) {
    case "succeeded": return "good";
    case "pending_approval": return "attention";
    case "approved":
    case "executing": return "busy";
    case "failed":
    case "rejected":
    case "indeterminate": return "problem";
    default: return "";
  }
}

/** Plain wording for a state. "Approved" and "done" are easy to confuse, and
 *  the difference is whether anything actually happened yet. */
export function stateLabel(state: OperationState): string {
  switch (state) {
    case "pending_approval": return "Waiting for you";
    case "approved": return "About to run";
    case "executing": return "Running";
    case "succeeded": return "Done";
    case "failed": return "Didn't run";
    case "indeterminate": return "Needs checking";
    case "rejected": return "Turned down";
    case "expired": return "Expired";
    case "cancelled": return "Withdrawn";
    default: return "Draft";
  }
}

export function StateBadge({ state }: { state: OperationState }) {
  return (
    <Pill tone={stateTone(state)} loud={state === "indeterminate"}>
      {stateLabel(state)}
    </Pill>
  );
}

export function riskTone(risk: RiskLevel): Tone {
  switch (risk) {
    case "critical":
    case "high": return "problem";
    case "medium": return "attention";
    default: return "";
  }
}

export function RiskBadge({ risk }: { risk: RiskLevel }) {
  const label = { low: "Minor", medium: "Moderate", high: "Significant", critical: "Major" }[risk];
  return <Pill tone={riskTone(risk)} loud={risk === "critical"}>{label} impact</Pill>;
}

/** What a state means for the systems being managed, in a sentence. */
export function stateMeaning(op: Operation): { tone: Tone; text: string } {
  switch (op.state) {
    case "pending_approval":
      return { tone: "attention", text: "Nothing has happened yet. This is waiting for your decision." };
    case "approved":
      return { tone: "info", text: "Approved, and about to be applied." };
    case "executing":
      return { tone: "info", text: "Being applied right now." };
    case "succeeded":
      return {
        tone: "good",
        text: op.verified
          ? "Applied, and confirmed by checking afterwards."
          : "Applied. It wasn't possible to confirm the result independently.",
      };
    case "failed":
      return { tone: "problem", text: "Not applied. Nothing was changed." };
    case "indeterminate":
      return {
        tone: "problem",
        text: "This may or may not have gone through. Don't try again — check the " +
          "system directly and sort it out by hand.",
      };
    case "rejected":
      return { tone: "info", text: "Turned down. Nothing was changed." };
    case "expired":
      return { tone: "info", text: "Ran out of time before anyone decided. Nothing was changed." };
    case "cancelled":
      return { tone: "info", text: "Withdrawn. Nothing was changed." };
    default:
      return { tone: "", text: "" };
  }
}

/* ── messages ───────────────────────────────────────────────────────────── */

const ICON: Record<string, string> = {
  good: "✓", attention: "!", problem: "✕", info: "i", busy: "…", "": "",
};

export function Message({ tone, children }: { tone: Tone; children: ReactNode }) {
  return (
    <div className={`msg ${tone}`} role={tone === "problem" ? "alert" : undefined}>
      <span className="msg-icon" aria-hidden="true">{ICON[tone]}</span>
      <div>{children}</div>
    </div>
  );
}

/** Transient confirmation. A save that shows a banner forever makes the page
 *  look like it still has something to say. */
export function useToasts() {
  const [toasts, setToasts] = useState<{ id: number; tone: Tone; text: string }[]>([]);

  const show = useCallback((tone: Tone, text: string) => {
    const id = Date.now() + Math.random();
    setToasts((t) => [...t, { id, tone, text }]);
    setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), 4500);
  }, []);

  const view = (
    <div className="toasts" aria-live="polite">
      {toasts.map((t) => (
        <div className={`toast ${t.tone}`} key={t.id}>
          <span className="msg-icon" aria-hidden="true">{ICON[t.tone]}</span>
          <span>{t.text}</span>
        </div>
      ))}
    </div>
  );

  return { show, view };
}

/* ── empty and loading ──────────────────────────────────────────────────── */

export function Empty({ mark, title, children }: { mark: string; title: string; children?: ReactNode }) {
  return (
    <div className="card">
      <div className="empty">
        <div className="empty-mark" aria-hidden="true">{mark}</div>
        <h3>{title}</h3>
        {children && <p>{children}</p>}
      </div>
    </div>
  );
}

/** Shaped like the content it replaces, so the page doesn't jump when it
 *  arrives. */
export function Skeleton({ rows = 3 }: { rows?: number }) {
  return (
    <div className="card">
      <div className="card-body stack" aria-busy="true" aria-label="Loading">
        {Array.from({ length: rows }, (_, i) => (
          <div className="skeleton" key={i} style={{ width: `${90 - i * 14}%` }} />
        ))}
      </div>
    </div>
  );
}

/* ── copy ───────────────────────────────────────────────────────────────── */

export function Copyable({ value, label }: { value: string; label?: string }) {
  const [copied, setCopied] = useState(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      setTimeout(() => setCopied(false), 1600);
    } catch {
      // Refused outside a secure context, which is normal on a plain-http LAN
      // address. The text stays selectable, so there is still a way through.
      setCopied(false);
    }
  }

  return (
    <div className="copybox">
      <code>{value}</code>
      <button className="btn sm" onClick={copy} aria-label={label ? `Copy ${label}` : "Copy"}>
        {copied ? "Copied" : "Copy"}
      </button>
    </div>
  );
}

export function CodeBlock({ children }: { children: string }) {
  const [copied, setCopied] = useState(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(children);
      setCopied(true);
      setTimeout(() => setCopied(false), 1600);
    } catch {
      setCopied(false);
    }
  }

  return (
    <div className="codeblock">
      <button className="btn sm" onClick={copy}>{copied ? "Copied" : "Copy"}</button>
      <pre className="code">{children}</pre>
    </div>
  );
}

/** A link that leaves the app. Marked so it's obvious before clicking. */
export function Out({ href, children }: { href: string; children: ReactNode }) {
  return (
    <a className="out" href={href} target="_blank" rel="noopener noreferrer">
      {children}
    </a>
  );
}

/* ── data ───────────────────────────────────────────────────────────────── */

export function Diff({ changes }: { changes?: Change[] }) {
  if (!changes?.length) return <p className="note tight">No details were recorded.</p>;

  return (
    <div className="diff">
      {changes.map((c, i) => (
        <div className="diff-row" key={`${c.field}-${i}`}>
          <span className="diff-field">{c.field.replace(/_/g, " ")}</span>
          <span>
            <span className="diff-from">{fmt(c.from)}</span>
            <span className="diff-arrow" aria-label="becomes">→</span>
            <span className="diff-to">{fmt(c.to)}</span>
          </span>
        </div>
      ))}
    </div>
  );
}

function fmt(v: unknown): string {
  if (v === null || v === undefined) return "not set";
  if (typeof v === "string") return v || "empty";
  return JSON.stringify(v);
}

export function Json({ value }: { value: unknown }) {
  if (value === null || value === undefined) {
    return <p className="note tight">Nothing recorded.</p>;
  }
  return <pre className="code">{JSON.stringify(value, null, 2)}</pre>;
}

export function History({ records }: { records: AuditRecord[] }) {
  if (!records.length) {
    return <div className="empty"><p>Nothing recorded yet.</p></div>;
  }
  return (
    <div className="tablewrap">
      <table>
        <thead>
          <tr><th>When</th><th>What happened</th><th>Who</th></tr>
        </thead>
        <tbody>
          {records.map((r) => (
            <tr key={r.seq}>
              <td className="num dim" style={{ whiteSpace: "nowrap" }}>{when(r.at)}</td>
              <td>{describeEvent(r)}</td>
              <td className="dim">{who(r.actor)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** Turns an event name into something readable. */
function describeEvent(r: AuditRecord): string {
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

/** Strips the machine prefix off an identity. */
function who(actor: string): string {
  if (actor.startsWith("system:")) return "mcpd";
  return actor.replace(/^(user|svc):/, "");
}

export function when(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString(undefined, {
    month: "short", day: "numeric", hour: "2-digit", minute: "2-digit",
  });
}

/** Relative time, so a deadline reads as urgency rather than a timestamp. */
export function ago(iso: string): string {
  const target = new Date(iso).getTime();
  if (Number.isNaN(target)) return iso;

  let delta = Math.round((target - Date.now()) / 1000);
  const steps: [Intl.RelativeTimeFormatUnit, number][] = [
    ["second", 60], ["minute", 60], ["hour", 24], ["day", 7], ["week", 4.35], ["month", 12],
  ];

  let unit: Intl.RelativeTimeFormatUnit = "second";
  for (const [u, size] of steps) {
    unit = u;
    if (Math.abs(delta) < size) break;
    delta = Math.round(delta / size);
  }
  return new Intl.RelativeTimeFormat(undefined, { numeric: "auto" }).format(delta, unit);
}

/** Re-runs a loader on an interval, and cleans up. */
export function usePoll(fn: () => void, ms: number) {
  useEffect(() => {
    fn();
    const t = setInterval(fn, ms);
    return () => clearInterval(t);
  }, [fn, ms]);
}
