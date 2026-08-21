import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from "react";
import type { AuditRecord } from "./api";

/* ── status ─────────────────────────────────────────────────────────────── */

export type Tone = "good" | "attention" | "problem" | "info" | "busy" | "";

/**
 * Who is signed in, for pages that render differently by role.
 *
 * A context rather than props because the question is asked deep in the tree --
 * a field inside a form inside a page -- and threading it through every layer
 * would mean every component in between knowing about roles it does not use.
 *
 * The default is null, which reads as "not an administrator". A page rendered
 * outside the provider therefore hides the controls rather than offering ones
 * the API will refuse.
 */
export const SessionContext = createContext<{ role: string } | null>(null);

/** True when the signed-in account may administer the host. */
export function useIsAdmin(): boolean {
  return useContext(SessionContext)?.role === "admin";
}

export function Dot({ tone }: { tone: Tone }) {
  return <span className={`dot ${tone}`} aria-hidden="true" />;
}

export function Pill({ tone = "", loud, children }: { tone?: Tone; loud?: boolean; children: ReactNode }) {
  return <span className={`pill ${loud ? "loud" : tone}`}>{children}</span>;
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
/**
 * Raising a toast.
 *
 * Named because it is passed down through pages to the rows that actually
 * report something, and each of those was restating the signature -- one of
 * them narrowed the tone to "good", which was a lie the compiler could not
 * catch because it was never asked to raise anything else.
 */
export type Notify = (tone: Tone, text: string) => void;

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

/** Re-runs a loader on an interval, and cleans up. */
export function usePoll(fn: () => void, ms: number) {
  useEffect(() => {
    fn();
    const t = setInterval(fn, ms);
    return () => clearInterval(t);
  }, [fn, ms]);
}
