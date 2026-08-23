import { useCallback } from "react";
import { api } from "@/lib/api";
import { useLoader } from "@/lib/hooks";
import { useCan } from "@/lib/session";
import { Loading, Notice, PageHeader } from "@/components/chrome";
import { SettingsForm } from "@/components/SettingsForm";

const OPENAI_API_KEYS = "https://platform.openai.com/settings/organization/api-keys";
const OPENAI_ADMIN_KEYS = "https://platform.openai.com/settings/organization/admin-keys";
const OPENAI_ORG = "https://platform.openai.com/settings/organization/general";

const LINKS = {
  "tunnel.api_key": { href: OPENAI_API_KEYS, label: "API keys" },
  "tunnel.admin_key": { href: OPENAI_ADMIN_KEYS, label: "Admin keys" },
  "tunnel.organization_id": { href: OPENAI_ORG, label: "Organization settings" },
};

/**
 * The host's own configuration: read to see, admin to change, and read-only
 * rather than a form that meets a 403. A plugin's settings live on its page.
 */
export function General() {
  const mayWrite = useCan("admin");
  const load = useCallback(() => api.settings(), []);
  const { data, error, reload } = useLoader(load, "Couldn't load settings.");

  const groups = (data?.groups ?? []).filter((g) => g.section !== "plugins");

  return (
    <>
      <PageHeader
        title="Settings"
        lede={mayWrite
          ? "Changes apply straight away unless a field says otherwise."
          : "What this host is configured to do. Changing it takes an administrator."}
      />

      {error && <Notice tone="problem">{error}</Notice>}

      {!data ? <Loading rows={6} /> : (
        <SettingsForm
          groups={groups} settings={data} links={LINKS}
          onSaved={reload} readOnly={!mayWrite}
        />
      )}
    </>
  );
}
