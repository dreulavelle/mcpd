import { useCallback, useEffect, useState } from "react";
import {
  api,
  ApiError,
  clearToken,
  getToken,
  setToken,
  type AuditRecord,
  type HealthReport,
  type Meta,
  type Operation,
  type Plugin,
} from "./api";
import {
  AuditTrail,
  Banner,
  Diff,
  Json,
  RiskChip,
  StateChip,
  formatTime,
  relativeTime,
  stateMeaning,
} from "./components";

type Tab = "operations" | "plugins" | "audit";

export default function App() {
  const [authed, setAuthed] = useState(() => getToken() !== null);
  const [meta, setMeta] = useState<Meta | null>(null);

  useEffect(() => {
    api.meta().then(setMeta).catch(() => setMeta(null));
  }, []);

  if (!authed) {
    return <Login meta={meta} onAuthenticated={() => setAuthed(true)} />;
  }
  return <Dashboard meta={meta} onSignOut={() => { clearToken(); setAuthed(false); }} />;
}

/* --- login ---------------------------------------------------------------- */

function Login({ meta, onAuthenticated }: { meta: Meta | null; onAuthenticated: () => void }) {
  const [token, setTokenValue] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    setToken(token.trim());
    try {
      // Any authenticated endpoint proves the token works. Using one the
      // dashboard needs anyway avoids a login-only endpoint that could drift
      // out of step with real authorization.
      await api.health();
      onAuthenticated();
    } catch (err) {
      clearToken();
      setError(
        err instanceof ApiError && err.status === 401
          ? "That token was not accepted."
          : "Could not reach mcpd.",
      );
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="login-wrap">
      <div className="card login-card">
        <div className="card-head">mcpd — sign in</div>
        <div className="card-body">
          {error && <Banner tone="error">{error}</Banner>}
          <form onSubmit={submit}>
            <div className="field">
              <label htmlFor="token">Access token</label>
              <input
                id="token"
                type="password"
                autoComplete="off"
                autoFocus
                value={token}
                onChange={(e) => setTokenValue(e.target.value)}
                placeholder="Paste a bearer token"
              />
            </div>
            <div className="actions">
              <button className="btn primary" type="submit" disabled={busy || !token.trim()}>
                {busy ? "Checking…" : "Sign in"}
              </button>
            </div>
          </form>
          {meta && (
            <p className="muted" style={{ marginTop: 18, fontSize: 13 }}>
              mcpd {meta.version} · authentication: {meta.auth_mode}
            </p>
          )}
        </div>
      </div>
    </div>
  );
}

/* --- shell ---------------------------------------------------------------- */

function Dashboard({ meta, onSignOut }: { meta: Meta | null; onSignOut: () => void }) {
  const [tab, setTab] = useState<Tab>("operations");
  const [selected, setSelected] = useState<string | null>(null);
  const [health, setHealth] = useState<HealthReport | null>(null);

  useEffect(() => {
    const poll = () => api.health().then(setHealth).catch(() => setHealth(null));
    poll();
    const timer = setInterval(poll, 15_000);
    return () => clearInterval(timer);
  }, []);

  return (
    <div className="shell">
      <header className="topbar">
        <span className="brand">mcpd</span>
        <nav className="tabs">
          {(["operations", "plugins", "audit"] as Tab[]).map((t) => (
            <button
              key={t}
              onClick={() => { setTab(t); setSelected(null); }}
              aria-current={tab === t ? "page" : undefined}
            >
              {t[0]!.toUpperCase() + t.slice(1)}
            </button>
          ))}
        </nav>
        <span className="spacer" />
        {health && (
          <span className={`status-dot ${health.status}`} title={describeHealth(health)}>
            {health.status}
          </span>
        )}
        <button className="btn" onClick={onSignOut}>Sign out</button>
      </header>

      <main>
        {tab === "operations" &&
          (selected ? (
            <OperationDetail id={selected} onBack={() => setSelected(null)} />
          ) : (
            <Operations onSelect={setSelected} />
          ))}
        {tab === "plugins" && <Plugins />}
        {tab === "audit" && <Audit />}
      </main>
      {meta && <div style={{ display: "none" }}>{meta.version}</div>}
    </div>
  );
}

function describeHealth(h: HealthReport): string {
  return h.checks.map((c) => `${c.name}: ${c.status}`).join("\n");
}

/* --- operations ----------------------------------------------------------- */

function Operations({ onSelect }: { onSelect: (id: string) => void }) {
  const [operations, setOperations] = useState<Operation[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);

  const load = useCallback(async () => {
    try {
      const res = await api.operations();
      setOperations(res.operations ?? []);
      setError("");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not load operations.");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
    // Operations move through states without the operator doing anything, so
    // the list refreshes on its own.
    const timer = setInterval(load, 5_000);
    return () => clearInterval(timer);
  }, [load]);

  // Anything awaiting a decision or needing a human comes first: this page
  // exists to surface work, not to be a chronological log.
  const needsAttention = operations.filter(
    (o) => o.state === "pending_approval" || o.state === "indeterminate",
  );
  const rest = operations.filter(
    (o) => o.state !== "pending_approval" && o.state !== "indeterminate",
  );

  if (loading) return <p className="empty">Loading…</p>;

  return (
    <>
      <h1>Operations</h1>
      <p className="subtitle">
        Every proposed change to managed infrastructure, and what became of it.
      </p>
      {error && <Banner tone="error">{error}</Banner>}

      {needsAttention.length > 0 && (
        <>
          <h2>Needs attention</h2>
          <OperationTable operations={needsAttention} onSelect={onSelect} />
        </>
      )}

      <h2>{needsAttention.length > 0 ? "Everything else" : "All operations"}</h2>
      {rest.length === 0 ? (
        <div className="card"><p className="empty">No operations yet.</p></div>
      ) : (
        <OperationTable operations={rest} onSelect={onSelect} />
      )}
    </>
  );
}

function OperationTable({
  operations,
  onSelect,
}: {
  operations: Operation[];
  onSelect: (id: string) => void;
}) {
  return (
    <div className="card">
      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              <th>State</th>
              <th>Risk</th>
              <th>Plugin</th>
              <th>Action</th>
              <th>Requested by</th>
              <th>When</th>
            </tr>
          </thead>
          <tbody>
            {operations.map((op) => (
              <tr key={op.id} className="clickable" onClick={() => onSelect(op.id)}>
                <td><StateChip state={op.state} /></td>
                <td><RiskChip risk={op.risk} /></td>
                <td className="mono">{op.plugin}</td>
                <td className="mono">{op.action}</td>
                <td className="mono muted">{op.requested_by}</td>
                <td className="muted num">{relativeTime(op.requested_at)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function OperationDetail({ id, onBack }: { id: string; onBack: () => void }) {
  const [operation, setOperation] = useState<Operation | null>(null);
  const [audit, setAudit] = useState<AuditRecord[]>([]);
  const [reason, setReason] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const res = await api.operation(id);
      setOperation(res.operation);
      setAudit(res.audit ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not load the operation.");
    }
  }, [id]);

  useEffect(() => {
    load();
    const timer = setInterval(load, 4_000);
    return () => clearInterval(timer);
  }, [load]);

  async function decide(action: "approve" | "reject") {
    setBusy(true);
    setError("");
    try {
      await (action === "approve" ? api.approve(id, reason) : api.reject(id, reason));
      setReason("");
      await load();
    } catch (err) {
      // A refusal is the system working, so its stable code is shown rather
      // than a generic failure message.
      setError(
        err instanceof ApiError
          ? `${err.code}: ${err.detail}`
          : "The decision could not be recorded.",
      );
    } finally {
      setBusy(false);
    }
  }

  if (!operation) {
    return (
      <>
        <button className="btn" onClick={onBack}>← Back</button>
        {error ? <Banner tone="error">{error}</Banner> : <p className="empty">Loading…</p>}
      </>
    );
  }

  const meaning = stateMeaning(operation);
  const decidable = operation.state === "pending_approval";

  return (
    <>
      <button className="btn" onClick={onBack}>← Back</button>

      <h1 style={{ marginTop: 18 }}>
        <span className="mono">{operation.action}</span>
      </h1>
      <p className="subtitle">
        <span className="mono">{operation.id}</span> · {operation.plugin}
      </p>

      <div className="row" style={{ marginBottom: 16 }}>
        <StateChip state={operation.state} />
        <RiskChip risk={operation.risk} />
        {operation.verified !== undefined && (
          <span className="muted" style={{ fontSize: 13 }}>
            {operation.verified ? "verified by observation" : "not verified"}
          </span>
        )}
      </div>

      <Banner tone={meaning.tone}>{meaning.text}</Banner>

      {operation.impact && (
        <div className="card">
          <div className="card-head">Impact</div>
          <div className="card-body">{operation.impact}</div>
        </div>
      )}

      <div className="card">
        <div className="card-head">Requested change</div>
        <div className="card-body"><Diff changes={operation.changes} /></div>
      </div>

      {decidable && (
        <div className="card">
          <div className="card-head">Decision</div>
          <div className="card-body">
            {error && <Banner tone="error">{error}</Banner>}
            <div className="field">
              <label htmlFor="reason">Reason (recorded in the audit trail)</label>
              <textarea
                id="reason"
                rows={2}
                value={reason}
                onChange={(e) => setReason(e.target.value)}
                placeholder="Why are you approving or rejecting this?"
              />
            </div>
            <div className="actions">
              <button className="btn primary" disabled={busy} onClick={() => decide("approve")}>
                Approve
              </button>
              <button className="btn danger" disabled={busy} onClick={() => decide("reject")}>
                Reject
              </button>
            </div>
            <p className="muted" style={{ marginTop: 14, fontSize: 13 }}>
              Approving authorises this change exactly as proposed. The stored
              parameters cannot be edited here — reject it and propose again
              if something is wrong. Expires {relativeTime(operation.expires_at)}.
            </p>
          </div>
        </div>
      )}

      {operation.error_code && (
        <div className="card">
          <div className="card-head">Failure</div>
          <div className="card-body">
            <p className="mono">{operation.error_code}</p>
            {operation.error_detail && <p className="muted">{operation.error_detail}</p>}
          </div>
        </div>
      )}

      <div className="grid two" style={{ marginTop: 16 }}>
        <div className="card">
          <div className="card-head">Before</div>
          <div className="card-body"><Json value={operation.before} /></div>
        </div>
        <div className="card">
          <div className="card-head">{operation.observed ? "Observed after" : "Requested"}</div>
          <div className="card-body">
            <Json value={operation.observed ?? operation.desired} />
          </div>
        </div>
      </div>

      <div className="card">
        <div className="card-head">Timeline</div>
        <div className="card-body">
          <dl style={{ margin: 0, fontSize: 14 }}>
            <Row label="Requested" value={`${operation.requested_by} · ${formatTime(operation.requested_at)}`} />
            {operation.approved_by && (
              <Row
                label="Approved"
                value={`${operation.approved_by} · ${operation.approved_at ? formatTime(operation.approved_at) : ""}`}
              />
            )}
            {operation.terminal_at && (
              <Row label="Completed" value={formatTime(operation.terminal_at)} />
            )}
            <Row label="Attempts" value={String(operation.attempts)} />
          </dl>
        </div>
      </div>

      <div className="card">
        <div className="card-head">Audit trail</div>
        <AuditTrail records={audit} />
      </div>
    </>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div style={{ display: "flex", gap: 12, padding: "5px 0" }}>
      <dt className="muted" style={{ minWidth: 110 }}>{label}</dt>
      <dd className="mono" style={{ margin: 0 }}>{value}</dd>
    </div>
  );
}

/* --- plugins -------------------------------------------------------------- */

function Plugins() {
  const [plugins, setPlugins] = useState<Plugin[]>([]);
  const [error, setError] = useState("");

  useEffect(() => {
    api
      .plugins()
      .then((r) => setPlugins(r.plugins ?? []))
      .catch((e) => setError(e instanceof Error ? e.message : "Could not load plugins."));
  }, []);

  return (
    <>
      <h1>Plugins</h1>
      <p className="subtitle">
        Integrations mounted on this host. Each serves its own MCP endpoint.
      </p>
      {error && <Banner tone="error">{error}</Banner>}

      {plugins.length === 0 && !error && (
        <div className="card"><p className="empty">No plugins are mounted.</p></div>
      )}

      {plugins.map((p) => (
        <div className="card" key={p.name}>
          <div className="card-head">
            {p.title} · {p.name} {p.version}
          </div>
          <div className="card-body">
            <div className="row" style={{ marginBottom: 12 }}>
              <span className={`status-dot ${p.health === "healthy" ? "up" : p.health === "degraded" ? "degraded" : "down"}`}>
                {p.health}
              </span>
              {p.required && <span className="chip risk-medium">required</span>}
              <span className="mono muted">{p.endpoint}</span>
            </div>
            {p.health_message && <Banner tone="warn">{p.health_message}</Banner>}
            <p className="muted">{p.description}</p>

            <h2 style={{ fontSize: 13, marginTop: 18 }}>Tools ({p.tools.length})</h2>
            <p className="mono muted">{p.tools.join(", ") || "none"}</p>

            {p.mutations.length > 0 && (
              <>
                <h2 style={{ fontSize: 13 }}>Approval-gated changes ({p.mutations.length})</h2>
                <p className="mono muted">{p.mutations.join(", ")}</p>
              </>
            )}
          </div>
        </div>
      ))}
    </>
  );
}

/* --- audit ---------------------------------------------------------------- */

function Audit() {
  const [records, setRecords] = useState<AuditRecord[]>([]);
  const [chain, setChain] = useState<{ intact: boolean; broken_at: number } | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    api
      .audit(200)
      .then((r) => setRecords(r.records ?? []))
      .catch((e) => setError(e instanceof Error ? e.message : "Could not load the audit trail."));
    // Chain verification needs admin rights, so a failure here is expected for
    // non-admin viewers and is not surfaced as an error.
    api.verifyAudit().then(setChain).catch(() => setChain(null));
  }, []);

  return (
    <>
      <h1>Audit trail</h1>
      <p className="subtitle">
        Append-only and hash-chained. Altering any entry invalidates every one
        after it, which is what makes this evidence rather than a log.
      </p>

      {error && <Banner tone="error">{error}</Banner>}
      {chain &&
        (chain.intact ? (
          <Banner tone="ok">Hash chain verified intact.</Banner>
        ) : (
          <Banner tone="error">
            Hash chain broken at sequence {chain.broken_at}. The audit trail has
            been altered outside mcpd.
          </Banner>
        ))}

      <div className="card">
        <AuditTrail records={records} />
      </div>
    </>
  );
}
