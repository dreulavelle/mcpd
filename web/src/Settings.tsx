import { useCallback, useEffect, useState } from "react";
import { api, type SettingsPayload } from "./api";
import { Message, Skeleton, useIsAdmin, useToasts } from "./components";
import { SettingsForm } from "./SettingsForm";

const OPENAI_API_KEYS = "https://platform.openai.com/settings/organization/api-keys";
const OPENAI_ADMIN_KEYS = "https://platform.openai.com/settings/organization/admin-keys";
const OPENAI_ORG = "https://platform.openai.com/settings/organization/general";

const LINKS = {
  "tunnel.api_key": { href: OPENAI_API_KEYS, label: "API keys" },
  "tunnel.admin_key": { href: OPENAI_ADMIN_KEYS, label: "Admin keys" },
  "tunnel.organization_id": { href: OPENAI_ORG, label: "Organization settings" },
};

export function Settings() {
  const [data, setData] = useState<SettingsPayload | null>(null);
  const [error, setError] = useState("");
  const { show, view } = useToasts();
  const admin = useIsAdmin();

  const load = useCallback(async () => {
    try {
      setData(await api.settings());
      setError("");
    } catch {
      setError("Couldn't load settings.");
    }
  }, []);

  useEffect(() => { load(); }, [load]);

  // A plugin's settings belong on its own card, not here. An integration is
  // independent of the host: someone configuring Cambium should not have to
  // find it in a list of the host's own switches, and should not meet it there
  // by accident when changing something else.
  const groups = (data?.groups ?? []).filter((g) => g.section !== "plugins");

  return (
    <>
      {view}
      <h1>Settings</h1>
      <p className="lede">
        {admin
          ? "Changes apply straight away unless a field says otherwise."
          : "What this host is configured to do. Changing it takes an administrator."}
      </p>

      {error && <Message tone="problem">{error}</Message>}

      {!data ? <Skeleton rows={5} /> : (
        <SettingsForm groups={groups} settings={data} links={LINKS}
                      onSaved={load} show={show} readOnly={!admin} />
      )}
    </>
  );
}

