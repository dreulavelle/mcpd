import { useCallback, useEffect, useState } from "react";
import { api, type Meta, type SettingGroup, type SettingsPayload, type TunnelInfo } from "./api";
import { Message, Skeleton, useToasts } from "./components";
import { SettingsForm } from "./SettingsForm";

const OPENAI_TUNNELS = "https://platform.openai.com/settings/organization/tunnels";
const OPENAI_API_KEYS = "https://platform.openai.com/settings/organization/api-keys";
const OPENAI_ADMIN_KEYS = "https://platform.openai.com/settings/organization/admin-keys";
const OPENAI_ORG = "https://platform.openai.com/settings/organization/general";

const LINKS = {
  "tunnel.tunnel_id": { href: OPENAI_TUNNELS, label: "Tunnels" },
  "tunnel.api_key": { href: OPENAI_API_KEYS, label: "API keys" },
  "tunnel.admin_key": { href: OPENAI_ADMIN_KEYS, label: "Admin keys" },
  "tunnel.organization_id": { href: OPENAI_ORG, label: "Organization settings" },
};

export function Settings() {
  const [data, setData] = useState<SettingsPayload | null>(null);
  const [meta, setMeta] = useState<Meta | null>(null);
  const [tunnels, setTunnels] = useState<TunnelInfo | null>(null);
  const [error, setError] = useState("");
  const { show, view } = useToasts();

  const load = useCallback(async () => {
    try {
      setData(await api.settings());
      setError("");
    } catch {
      setError("Couldn't load settings.");
    }
    api.tunnel().then(setTunnels).catch(() => setTunnels(null));
  }, []);

  useEffect(() => {
    load();
    api.meta().then(setMeta).catch(() => setMeta(null));
  }, [load]);

  const groups = (data?.groups ?? []).map((g) =>
    relevant(g, tunnels?.can_manage ?? false, data?.values ?? {}));

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
        <SettingsForm groups={groups} settings={data} links={LINKS}
                      onSaved={load} show={show} />
      )}
    </>
  );
}

/**
 * Hides fields this deployment cannot act on.
 *
 * A form offering settings that do nothing is worse than one that omits them:
 * it invites someone to fill one in and then ignores the value. Two things
 * decide it here. With an admin key, tunnel IDs come from the tunnel you made
 * on the Tunnels page rather than being typed. And the identity ChatGPT acts
 * as is read only when the tunnel carries it -- once each person signs in,
 * their own account decides, and a configured identity is never consulted.
 */
function relevant(group: SettingGroup, canManage: boolean, values: Record<string, unknown>): SettingGroup {
  const identity = ["tunnel.principal", "tunnel.role", "tunnel.plugins"];
  const signIn = String(values["tunnel.each_person_signs_in"] ?? "") === "true";

  return {
    ...group,
    fields: group.fields.filter((f) => {
      if (canManage && f.key === "tunnel.tunnel_id") return false;
      if (signIn && identity.includes(f.key)) return false;
      return true;
    }),
  };
}
