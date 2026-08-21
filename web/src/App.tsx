import { useCallback, useEffect, useState } from "react";
import {
  api, ApiError, clearToken, getToken, setToken,
  type AuditRecord, type HealthReport, type Meta, type Operation,
} from "./api";
import {
  ago, CodeBlock, Diff, Dot, Empty, History, Json, Message, Pill,
  RiskBadge, Skeleton, StateBadge, stateMeaning, usePoll, useToasts, when,
} from "./components";
import { Plugins } from "./Plugins";
import { Settings } from "./Settings";
import { Tunnels } from "./Tunnels";

type Tab = "changes" | "plugins" | "tunnels" | "settings" | "history";

export default function App() {
  const [signedIn, setSignedIn] = useState(() => getToken() !== null);
  const [meta, setMeta] = useState<Meta | null>(null);

  useEffect(() => {
    api.meta().then(setMeta).catch(() => setMeta(null));
  }, []);

  if (!signedIn) return <SignIn meta={meta} onDone={() => setSignedIn(true)} />;
  return <Console onSignOut={() => { clearToken(); setSignedIn(false); }} />;
}

/* ── sign in ────────────────────────────────────────────────────────────── */

function SignIn({ meta, onDone }: { meta: Meta | null; onDone: () => void }) {
  const [key, setKey] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    setToken(key.trim());
    try {
      // Any protected endpoint proves the key works. Using one the console
      // needs anyway keeps this from drifting out of step with real access.
      await api.health();
      onDone();
    } catch (err) {
      clearToken();
      setError(
        err instanceof ApiError && err.status === 401
          ? "That key wasn't accepted."
          : "Couldn't reach mcpd. Is it running?",
      );
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="signin">
      <div className="signin-card">
        <div className="brand">
          <span className="brand-mark" aria-hidden="true">m</span>
          mcpd
        </div>

        <div className="card">
          <div className="card-body">
            {error && <Message tone="problem">{error}</Message>}

            <form onSubmit={submit}>
              <div className="field">
                <label htmlFor="key">Access key</label>
                <input
                  id="key" type="password" autoComplete="off" autoFocus
                  value={key} onChange={(e) => setKey(e.target.value)}
                  placeholder="Paste your key"
                />
                <p className="note">
                  It's in your <code>.env</code> file, as <code>MCPD_TOKEN_LOCAL</code>.
                </p>
              </div>

              <button className="btn primary" type="submit" disabled={busy || !key.trim()}
                      style={{ width: "100%" }}>
                {busy ? "Checking…" : "Sign in"}
              </button>
            </form>
          </div>
        </div>

        {meta && (
          <p className="note" style={{ textAlign: "center", marginTop: "var(--s4)" }}>
            mcpd {meta.version}
          </p>
        )}
      </div>
    </div>
  );
}

/* ── console ────────────────────────────────────────────────────────────── */

function Console({ onSignOut }: { onSignOut: () => void }) {
  const [tab, setTab] = useState<Tab>("changes");
  const [openId, setOpenId] = useState<string | null>(null);
  const [health, setHealth] = useState<HealthReport | null>(null);
  const [waiting, setWaiting] = useState(0);

  const pollHealth = useCallback(() => {
    api.health().then(setHealth).catch(() => setHealth(null));
  }, []);
  usePoll(pollHealth, 20_000);

  // The count of things waiting on a person belongs in the nav: it's the one
  // number worth knowing without opening anything.
  const pollWaiting = useCallback(() => {
    api.operations("pending_approval")
      .then((r) => setWaiting(r.count ?? 0))
      .catch(() => setWaiting(0));
  }, []);
  usePoll(pollWaiting, 10_000);

  const tabs: [Tab, string][] = [
    ["changes", "Changes"],
    ["plugins", "Systems"],
    ["tunnels", "ChatGPT"],
    ["settings", "Settings"],
    ["history", "History"],
  ];

  return (
    <div className="shell">
      <header className="topbar">
        <div className="brand">
          <span className="brand-mark" aria-hidden="true">m</span>
          mcpd
        </div>

        <nav className="tabs">
          {tabs.map(([id, label]) => (
            <button key={id} aria-current={tab === id ? "page" : undefined}
                    onClick={() => { setTab(id); setOpenId(null); }}>
              {label}
              {id === "changes" && waiting > 0 && (
                <Pill tone="attention">{waiting}</Pill>
              )}
            </button>
          ))}
        </nav>

        <span className="grow" />

        {health && (
          <span className="health" title={health.checks.map((c) => `${c.name}: ${c.status}`).join("\n")}>
            <Dot tone={health.status === "up" ? "good" : health.status === "down" ? "problem" : "attention"} />
            {health.status === "up" ? "All good" : health.status === "down" ? "Problem" : "Degraded"}
          </span>
        )}

        <button className="btn quiet" onClick={onSignOut}>Sign out</button>
      </header>

      <main>
        {tab === "changes" && (openId
          ? <ChangeDetail id={openId} onBack={() => setOpenId(null)} />
          : <Changes onOpen={setOpenId} />)}
        {tab === "plugins" && <Plugins />}
        {tab === "tunnels" && <Tunnels />}
        {tab === "settings" && <Settings />}
        {tab === "history" && <FullHistory />}
      </main>
    </div>
  );
}

/* ── changes ────────────────────────────────────────────────────────────── */

function Changes({ onOpen }: { onOpen: (id: string) => void }) {
  const [ops, setOps] = useState<Operation[] | null>(null);
  const [error, setError] = useState("");

  const load = useCallback(() => {
    api.operations()
      .then((r) => { setOps(r.operations ?? []); setError(""); })
      .catch((e) => { setOps([]); setError(e instanceof Error ? e.message : "Couldn't load changes."); });
  }, []);
  usePoll(load, 6_000);

  if (ops === null) return <><h1>Changes</h1><Skeleton rows={4} /></>;

  const waiting = ops.filter((o) => o.state === "pending_approval" || o.state === "indeterminate");
  const rest = ops.filter((o) => !waiting.includes(o));

  return (
    <>
      <h1>Changes</h1>
      <p className="lede">
        Everything an assistant has suggested, and what became of it. Nothing
        here takes effect until someone says yes.
      </p>

      {error && <Message tone="problem">{error}</Message>}

      {waiting.length > 0 && (
        <>
          <h2 style={{ marginTop: 0 }}>Waiting for you</h2>
          <ChangeTable ops={waiting} onOpen={onOpen} />
        </>
      )}

      {rest.length > 0 && (
        <>
          <h2>{waiting.length > 0 ? "Everything else" : "Recent"}</h2>
          <ChangeTable ops={rest} onOpen={onOpen} />
        </>
      )}

      {ops.length === 0 && !error && (
        <Empty mark="✓" title="Nothing to review">
          When an assistant suggests a change, it'll show up here for you to
          approve or turn down.
        </Empty>
      )}
    </>
  );
}

function ChangeTable({ ops, onOpen }: { ops: Operation[]; onOpen: (id: string) => void }) {
  return (
    <div className="card">
      <div className="tablewrap">
        <table>
          <thead>
            <tr>
              <th>Status</th><th>What</th><th>Impact</th><th>Where</th><th>Suggested</th>
            </tr>
          </thead>
          <tbody>
            {ops.map((op) => (
              <tr key={op.id} className="tappable" onClick={() => onOpen(op.id)}
                  tabIndex={0} onKeyDown={(e) => e.key === "Enter" && onOpen(op.id)}>
                <td><StateBadge state={op.state} /></td>
                <td>{op.action.replace(/[._]/g, " ")}</td>
                <td><RiskBadge risk={op.risk} /></td>
                <td className="dim">{op.plugin}</td>
                <td className="dim num" style={{ whiteSpace: "nowrap" }}>{ago(op.requested_at)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function ChangeDetail({ id, onBack }: { id: string; onBack: () => void }) {
  const [op, setOp] = useState<Operation | null>(null);
  const [history, setHistory] = useState<AuditRecord[]>([]);
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const { show, view } = useToasts();

  const load = useCallback(() => {
    api.operation(id)
      .then((r) => { setOp(r.operation); setHistory(r.audit ?? []); })
      .catch((e) => setError(e instanceof Error ? e.message : "Couldn't load this change."));
  }, [id]);
  usePoll(load, 5_000);

  async function decide(action: "approve" | "reject") {
    setBusy(true);
    setError("");
    try {
      await (action === "approve" ? api.approve(id, reason) : api.reject(id, reason));
      setReason("");
      show("good", action === "approve" ? "Approved — applying now." : "Turned down.");
      load();
    } catch (e) {
      setError(e instanceof ApiError ? explain(e) : "Couldn't record your decision.");
    } finally {
      setBusy(false);
    }
  }

  if (!op) {
    return (
      <>
        <button className="btn quiet" onClick={onBack}>← Back</button>
        {error ? <Message tone="problem">{error}</Message> : <Skeleton rows={5} />}
      </>
    );
  }

  const meaning = stateMeaning(op);
  const decidable = op.state === "pending_approval";

  return (
    <>
      {view}
      <button className="btn quiet" onClick={onBack}>← Back to changes</button>

      <h1 style={{ marginTop: "var(--s4)" }}>{op.action.replace(/[._]/g, " ")}</h1>
      <div className="row wrap" style={{ marginBottom: "var(--s5)" }}>
        <StateBadge state={op.state} />
        <RiskBadge risk={op.risk} />
        <span className="dim">on {op.plugin}</span>
      </div>

      <Message tone={meaning.tone}>{meaning.text}</Message>

      {op.impact && (
        <div className="card">
          <div className="card-body">
            <p className="eyebrow">What this does</p>
            <p style={{ margin: 0 }}>{op.impact}</p>
          </div>
        </div>
      )}

      <div className="card">
        <div className="card-body">
          <p className="eyebrow">What changes</p>
          <Diff changes={op.changes} />
        </div>
      </div>

      {decidable && (
        <div className="card">
          <div className="card-body">
            {error && <Message tone="problem">{error}</Message>}

            <div className="field">
              <label htmlFor="reason">Add a note (optional)</label>
              <textarea id="reason" rows={2} value={reason}
                        onChange={(e) => setReason(e.target.value)}
                        placeholder="Why you're approving or turning this down" />
              <p className="note">Saved with the change, so it's clear later why.</p>
            </div>

            <div className="actions">
              <button className="btn primary" disabled={busy} onClick={() => decide("approve")}>
                {busy ? "Working…" : "Approve"}
              </button>
              <button className="btn danger" disabled={busy} onClick={() => decide("reject")}>
                Turn down
              </button>
              <span className="note tight" style={{ marginLeft: "var(--s2)" }}>
                Expires {ago(op.expires_at)}
              </span>
            </div>

            <p className="note" style={{ marginTop: "var(--s4)" }}>
              Approving applies exactly what's shown above — it can't be edited
              here. If something's wrong, turn it down and ask for it again.
            </p>
          </div>
        </div>
      )}

      {op.error_code && (
        <div className="card">
          <div className="card-body">
            <p className="eyebrow">What went wrong</p>
            <p style={{ margin: 0 }}>{friendlyError(op.error_code, op.error_detail)}</p>
          </div>
        </div>
      )}

      <div className="split two" style={{ marginTop: "var(--s3)" }}>
        <div className="card">
          <div className="card-body">
            <p className="eyebrow">Before</p>
            <Json value={op.before} />
          </div>
        </div>
        <div className="card">
          <div className="card-body">
            <p className="eyebrow">{op.observed ? "After" : "Requested"}</p>
            <Json value={op.observed ?? op.desired} />
          </div>
        </div>
      </div>

      <div className="card">
        <div className="card-body">
          <p className="eyebrow">Details</p>
          <dl className="kv">
            <div><dt>Suggested by</dt><dd>{op.requested_by.replace(/^(user|svc):/, "")}</dd></div>
            <div><dt>Suggested</dt><dd>{when(op.requested_at)}</dd></div>
            {op.approved_by && (
              <div><dt>Approved by</dt><dd>{op.approved_by.replace(/^(user|svc):/, "")}</dd></div>
            )}
            {op.terminal_at && <div><dt>Finished</dt><dd>{when(op.terminal_at)}</dd></div>}
            {op.attempts > 1 && <div><dt>Attempts</dt><dd>{op.attempts}</dd></div>}
          </dl>
        </div>
      </div>

      <div className="card">
        <div className="card-head"><h3>What happened</h3></div>
        <History records={history} />
      </div>
    </>
  );
}

/** Turns a stable error code into something a person can act on. */
function friendlyError(code: string, detail?: string): string {
  switch (code) {
    case "PRECONDITION_CHANGED":
      return "The system changed after this was suggested, so applying it would " +
        "have overwritten someone else's work. Nothing was changed — ask for it again.";
    case "SELF_APPROVAL_FORBIDDEN":
      return "Changes at this level need someone other than whoever suggested them.";
    case "IDENTITY_INDISTINCT":
      return "This needs a second person to approve, but everyone currently shares " +
        "one sign-in, so mcpd can't tell who's who. Switch to individual accounts to use this.";
    case "APPROVAL_EXPIRED":
      return "The approval ran out of time before it could be applied.";
    case "INDETERMINATE":
      return "mcpd couldn't tell whether this went through. Check the system directly.";
    case "UPSTREAM_FAILED":
      return detail || "The system being managed refused the change.";
    default:
      return detail || code;
  }
}

function explain(e: ApiError): string {
  return friendlyError(e.code, e.detail);
}

/* ── history ────────────────────────────────────────────────────────────── */

function FullHistory() {
  const [records, setRecords] = useState<AuditRecord[] | null>(null);
  const [chain, setChain] = useState<{ intact: boolean; broken_at: number } | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    api.audit(200)
      .then((r) => setRecords(r.records ?? []))
      .catch((e) => { setRecords([]); setError(e instanceof Error ? e.message : "Couldn't load the history."); });
    // Needs admin rights, so failing here is normal for other people.
    api.verifyAudit().then(setChain).catch(() => setChain(null));
  }, []);

  return (
    <>
      <h1>History</h1>
      <p className="lede">
        A permanent record of everything that's happened. Entries can only be
        added — if anything is edited or removed, mcpd notices.
      </p>

      {error && <Message tone="problem">{error}</Message>}

      {chain && (chain.intact
        ? <Message tone="good">Checked — nothing has been tampered with.</Message>
        : <Message tone="problem">
            <strong>The history has been altered.</strong> Something edited the
            database directly, starting at entry {chain.broken_at}.
          </Message>)}

      {records === null ? <Skeleton rows={6} /> : (
        <div className="card"><History records={records} /></div>
      )}
    </>
  );
}

export { CodeBlock };
