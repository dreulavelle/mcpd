import { useCallback, useEffect, useState } from "react";
import {
  api, ApiError, clearToken, getToken, setToken,
  type AuditRecord, type HealthReport, type Meta,
} from "./api";
import { CodeBlock, Dot, History, Message, Skeleton, usePoll, useToasts } from "./components";
import { Plugins } from "./Plugins";
import { Settings } from "./Settings";
import { Tunnels } from "./Tunnels";

type Tab = "plugins" | "tunnels" | "settings" | "history";

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
  const [tab, setTab] = useState<Tab>("plugins");
  const [health, setHealth] = useState<HealthReport | null>(null);

  const pollHealth = useCallback(() => {
    api.health().then(setHealth).catch(() => setHealth(null));
  }, []);
  usePoll(pollHealth, 20_000);

  const tabs: [Tab, string][] = [
    ["plugins", "Plugins"],
    ["tunnels", "Tunnels"],
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

        <button className="btn quiet" onClick={onSignOut}>Sign out</button>
      </header>

      <main>
        {tab === "plugins" && <Plugins />}
        {tab === "tunnels" && <Tunnels />}
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
        <button className="btn sm" disabled={busy || !records?.length} onClick={clear}>
          {busy ? "Clearing…" : "Clear"}
        </button>
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
