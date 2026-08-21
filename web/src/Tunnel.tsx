import { useCallback, useEffect, useState } from "react";
import { api, ApiError, type TunnelInfo } from "./api";
import { Banner } from "./components";

/**
 * Tunnel card.
 *
 * The tunnel is the thing most likely to be wrong during setup and the hardest
 * to diagnose from outside, so its state is shown plainly and it can be
 * started and stopped here rather than by restarting mcpd.
 */
export function TunnelCard() {
  const [info, setInfo] = useState<TunnelInfo | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const load = useCallback(
    () => api.tunnel().then(setInfo).catch(() => setInfo(null)),
    [],
  );

  useEffect(() => {
    load();
    const timer = setInterval(load, 10_000);
    return () => clearInterval(timer);
  }, [load]);

  async function act(action: "start" | "stop") {
    setBusy(true);
    setError("");
    try {
      await (action === "start" ? api.tunnelStart() : api.tunnelStop());
      await load();
    } catch (err) {
      setError(
        err instanceof ApiError ? err.detail : "Could not reach mcpd.",
      );
      await load();
    } finally {
      setBusy(false);
    }
  }

  if (!info) return null;

  const { status, version } = info;
  const disabled = status.state === "disabled";

  return (
    <div className="card tunnel-card">
      <div className="card-body">
        <div className="tunnel-head">
          <span className={`dot ${toneFor(status.state)}`} aria-hidden="true" />
          <div>
            <h3>ChatGPT tunnel</h3>
            <p className="hint" style={{ marginBottom: 0 }}>{describe(status.state)}</p>
          </div>
          <span className="spacer" />
          {!disabled && (
            <button
              className={`btn ${status.state === "connected" ? "" : "primary"}`}
              disabled={busy}
              onClick={() => act(status.state === "connected" ? "stop" : "start")}
            >
              {busy
                ? "Working…"
                : status.state === "connected"
                  ? "Disconnect"
                  : "Connect"}
            </button>
          )}
        </div>

        {error && <Banner tone="error">{error}</Banner>}

        {status.state === "failed" && status.message && (
          <Banner tone="error">{status.message}</Banner>
        )}

        {disabled ? (
          <p className="hint" style={{ marginTop: 12, marginBottom: 0 }}>
            No tunnel is set up yet. See the Setup tab — it's how ChatGPT
            reaches mcpd without opening anything to the internet.
          </p>
        ) : (
          <dl className="settings" style={{ marginTop: 14 }}>
            {status.tunnel_id && (
              <div className="setting">
                <dt>tunnel</dt>
                <dd><code>{status.tunnel_id}</code></dd>
              </div>
            )}
            {status.principal && (
              <div className="setting">
                <dt>acts as</dt>
                <dd>
                  <code>{status.principal}</code>
                  {status.role && <span className="pill">{status.role}</span>}
                </dd>
              </div>
            )}
            {status.plugins && (
              <div className="setting">
                <dt>can reach</dt>
                <dd>
                  <code>
                    {status.plugins.includes("*")
                      ? "everything"
                      : status.plugins.join(", ")}
                  </code>
                </dd>
              </div>
            )}
          </dl>
        )}

        {version?.update_available && (
          <Banner tone="warn">
            A newer tunnel is available ({version.latest}, you have{" "}
            {version.embedded}). It's built into mcpd, so picking it up means
            rebuilding — nothing updates itself behind your back.
          </Banner>
        )}
      </div>
    </div>
  );
}

function toneFor(state: TunnelInfo["status"]["state"]): string {
  switch (state) {
    case "connected":
      return "up";
    case "starting":
      return "warn";
    case "failed":
      return "down";
    default:
      return "";
  }
}

function describe(state: TunnelInfo["status"]["state"]): string {
  switch (state) {
    case "connected":
      return "ChatGPT can reach mcpd right now.";
    case "starting":
      return "Connecting…";
    case "stopped":
      return "Set up, but not connected.";
    case "failed":
      return "Something went wrong connecting.";
    default:
      return "Not set up.";
  }
}
