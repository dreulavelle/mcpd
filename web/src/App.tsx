import { useCallback, useEffect, useState } from "react";
import {
  api, ApiError, setCSRFToken,
  type AuditRecord, type HealthReport, type Meta, type Session,
} from "./api";
import { CodeBlock, Dot, History, Message, SessionContext, Skeleton, useIsAdmin, usePoll, useToasts } from "./components";
import { Plugins } from "./Plugins";
import { Settings } from "./Settings";
import { Tunnels } from "./Tunnels";
import { Users } from "./Users";

type Tab = "plugins" | "tunnels" | "users" | "settings" | "history";

export default function App() {
  const [session, setSession] = useState<Session | null>(null);
  const [meta, setMeta] = useState<Meta | null>(null);
  // Undecided until the cookie has been checked. Rendering the sign-in form
  // first would flash it at everyone whose session is still good.
  const [checked, setChecked] = useState(false);

  const adopt = useCallback((s: Session) => {
    setCSRFToken(s.csrf_token);
    setSession(s);
  }, []);

  useEffect(() => {
    api.meta().then(setMeta).catch(() => setMeta(null));
    // The cookie survives a reload; the CSRF token does not, so it is fetched
    // back rather than requiring another sign-in.
    api.session().then(adopt).catch(() => undefined).finally(() => setChecked(true));
  }, [adopt]);

  const signOut = useCallback(() => {
    api.signOut().catch(() => undefined).finally(() => {
      setCSRFToken(null);
      setSession(null);
    });
  }, []);

  if (!checked || !meta) return null;
  // An instance nobody has claimed shows a way to claim it. Offering a sign-in
  // form instead would ask for credentials that cannot exist yet.
  if (!session && meta.needs_setup) return <FirstRun meta={meta} onDone={adopt} />;
  if (!session) return <SignIn meta={meta} onDone={adopt} />;
  return (
    <SessionContext.Provider value={{ role: session.role }}>
      <Console session={session} onSignOut={signOut} />
    </SessionContext.Provider>
  );
}

/* ── sign in ────────────────────────────────────────────────────────────── */

function SignIn({ meta, onDone }: { meta: Meta | null; onDone: (s: Session) => void }) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      onDone(await api.signIn(email.trim(), password));
    } catch (err) {
      setError(
        err instanceof ApiError && err.status === 401
          ? "That email and password did not match."
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
                <label htmlFor="email">Email</label>
                <input
                  id="email" type="email" autoComplete="username" autoFocus
                  value={email} onChange={(e) => setEmail(e.target.value)}
                  placeholder="you@example.com"
                />
              </div>

              <div className="field">
                <label htmlFor="password">Password</label>
                <input
                  id="password" type="password" autoComplete="current-password"
                  value={password} onChange={(e) => setPassword(e.target.value)}
                  placeholder="Your password"
                />
              </div>

              <button className="btn primary" type="submit"
                      disabled={busy || !email.trim() || !password}
                      style={{ width: "100%" }}>
                {busy ? "Signing in…" : "Sign in"}
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

/* ── first run ──────────────────────────────────────────────────────────── */

/**
 * Claiming a new instance.
 *
 * The first account is an administrator because there is nobody to grant it
 * the role afterwards. Registration stops being offered the moment an account
 * exists, so this is a door that closes behind the first person through it.
 */
function FirstRun({ meta, onDone }: { meta: Meta | null; onDone: (s: Session) => void }) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const mismatch = confirm !== "" && password !== confirm;
  const ready = email.trim() !== "" && password.length >= 12 && !mismatch && confirm !== "";

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      onDone(await api.registerFirst(email.trim(), password));
    } catch (err) {
      setError(
        err instanceof ApiError
          ? (err.status === 409
            ? "Someone already claimed this instance. Reload and sign in."
            : err.detail)
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
            <h3 style={{ margin: "0 0 var(--s2)" }}>Create the first account</h3>
            <p className="note">
              Nobody has claimed this host yet. This account will be an
              administrator; you can add others once you are in.
            </p>

            {error && <Message tone="problem">{error}</Message>}

            <form onSubmit={submit}>
              <div className="field">
                <label htmlFor="su-email">Email</label>
                <input
                  id="su-email" type="email" autoComplete="username" autoFocus
                  value={email} onChange={(e) => setEmail(e.target.value)}
                  placeholder="you@example.com"
                />
              </div>

              <div className="field">
                <label htmlFor="su-password">Password</label>
                <input
                  id="su-password" type="password" autoComplete="new-password"
                  value={password} onChange={(e) => setPassword(e.target.value)}
                  placeholder="At least 12 characters"
                />
              </div>

              <div className="field">
                <label htmlFor="su-confirm">Confirm password</label>
                <input
                  id="su-confirm" type="password" autoComplete="new-password"
                  value={confirm} onChange={(e) => setConfirm(e.target.value)}
                  placeholder="Type it again"
                />
                {mismatch && <p className="note" style={{ color: "var(--problem)" }}>
                  These do not match.
                </p>}
              </div>

              <button className="btn primary" type="submit" disabled={busy || !ready}
                      style={{ width: "100%" }}>
                {busy ? "Creating…" : "Create account"}
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

function Console({ session, onSignOut }: { session: Session; onSignOut: () => void }) {
  const [tab, setTab] = useState<Tab>("plugins");
  const [health, setHealth] = useState<HealthReport | null>(null);

  const pollHealth = useCallback(() => {
    api.health().then(setHealth).catch(() => setHealth(null));
  }, []);
  usePoll(pollHealth, 20_000);

  // Accounts are an administrator's business. The API refuses the calls
  // regardless; hiding the tab keeps the console from offering a page that can
  // only answer 403.
  const admin = session.role === "admin";
  const tabs: [Tab, string][] = [
    ["plugins", "Plugins"],
    ["tunnels", "Tunnels"],
    ...(admin ? [["users", "Users"] as [Tab, string]] : []),
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
                    onClick={() => setTab(id)}>
              {label}
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

        <span className="note" style={{ marginRight: "var(--s3)" }}>
          {session.display_name || session.email}
        </span>
        <button className="btn quiet" onClick={onSignOut}>Sign out</button>
      </header>

      <main>
        {tab === "plugins" && <Plugins />}
        {tab === "tunnels" && <Tunnels />}
        {tab === "users" && <Users />}
        {tab === "settings" && <Settings />}
        {tab === "history" && <FullHistory />}
      </main>
    </div>
  );
}

/* ── changes ────────────────────────────────────────────────────────────── */

function FullHistory() {
  const [records, setRecords] = useState<AuditRecord[] | null>(null);
  const [broken, setBroken] = useState<number | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const { show, view } = useToasts();
  const admin = useIsAdmin();

  const load = useCallback(() => {
    api.audit(200)
      .then((r) => setRecords(r.records ?? []))
      .catch((e) => {
        setRecords([]);
        setError(e instanceof Error ? e.message : "Couldn't load the history.");
      });
    // Silence when the chain is intact. A check that announces success on
    // every visit trains people to skip the one time it does not.
    api.verifyAudit()
      .then((c) => setBroken(c.intact ? null : c.broken_at))
      .catch(() => setBroken(null));
  }, []);

  useEffect(() => { load(); }, [load]);

  async function clear() {
    if (!confirm("Clear the history? The record of everything so far is removed.")) return;
    setBusy(true);
    try {
      const r = await api.clearAudit();
      show("good", `Cleared ${r.removed} ${r.removed === 1 ? "entry" : "entries"}.`);
    } catch (e) {
      show("problem", e instanceof ApiError ? e.detail : "Couldn't clear it.");
    } finally {
      setBusy(false);
      load();
    }
  }

  return (
    <>
      {view}
      <div className="row" style={{ alignItems: "flex-start" }}>
        <div style={{ flex: 1 }}>
          <h1>History</h1>
          <p className="lede">Append-only. mcpd notices if anything is altered.</p>
        </div>
        {admin && (
          <button className="btn sm" disabled={busy || !records?.length} onClick={clear}>
            {busy ? "Clearing…" : "Clear"}
          </button>
        )}
      </div>

      {error && <Message tone="problem">{error}</Message>}

      {broken !== null && (
        <Message tone="problem">
          <span>
            <strong>The history has been altered.</strong> Something edited the
            database directly, starting at entry {broken}.
          </span>
        </Message>
      )}

      {records === null ? <Skeleton rows={6} /> : (
        <div className="card"><History records={records} /></div>
      )}
    </>
  );
}

export { CodeBlock };
