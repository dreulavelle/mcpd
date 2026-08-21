import { useCallback, useEffect, useState } from "react";
import { api, type Meta, type SettingsPayload } from "./api";
import { Message, Skeleton, useToasts } from "./components";
import { SettingsForm } from "./SettingsForm";

/**
 * Settings.
 *
 * Only what belongs to no particular page. Anything about ChatGPT lives on the
 * Tunnels page beside the connectors it configures, because a setting people
 * cannot find is a setting that does not work.
 */
export function Settings() {
  const [data, setData] = useState<SettingsPayload | null>(null);
  const [meta, setMeta] = useState<Meta | null>(null);
  const [error, setError] = useState("");
  const { show, view } = useToasts();

  const load = useCallback(async () => {
    try {
      setData(await api.settings());
      setError("");
    } catch {
      setError("Couldn't load settings.");
    }
  }, []);

  useEffect(() => {
    load();
    api.meta().then(setMeta).catch(() => setMeta(null));
  }, [load]);

  const groups = data?.groups.filter((g) => g.section !== "tunnels") ?? [];

  return (
    <>
      {view}
      <h1>Settings</h1>
      <p className="lede">Changes apply straight away unless a field says otherwise.</p>

      {error && <Message tone="problem">{error}</Message>}

      {meta?.auth_mode === "static" && (
        <Message tone="attention">
          <span>
            <strong>Everyone shares one sign-in.</strong> mcpd can't tell two
            people apart, so it refuses changes that need a second approver.
            Set <code>auth.mode</code> to <code>mixed</code> to give people
            their own accounts.
          </span>
        </Message>
      )}

      {!data ? <Skeleton rows={5} /> : (
        <SettingsForm groups={groups} settings={data} onSaved={load} show={show} />
      )}
    </>
  );
}
