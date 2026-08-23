import { useCallback } from "react";
import { api, type BootstrapSetting } from "@/lib/api";
import { useLoader } from "@/lib/hooks";
import { useCan } from "@/lib/session";
import { Loading, Notice, PageHeader } from "@/components/chrome";
import { SettingsForm } from "@/components/SettingsForm";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

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

  // A plugin's settings live on its page, and the sign-in ones live on
  // Authentication beside the queue of people waiting to be let in — which is
  // the question they raise.
  const groups = (data?.groups ?? [])
    .filter((g) => g.section !== "plugins" && g.section !== "authentication");

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
        <>
          <SettingsForm
            groups={groups} settings={data} links={LINKS}
            onSaved={reload} readOnly={!mayWrite}
          />
          <StartupFile values={data.bootstrap} />
        </>
      )}
    </>
  );
}

/**
 * The handful of values that are not on this page, and where they are instead.
 *
 * Everything else moved into the database so a change could be recorded
 * against whoever made it. These four could not, and each says why. Showing
 * them is the point: "everything is on this page" is only useful to know if
 * the exceptions are named, and an operator hunting for a setting that isn't
 * here should find out where it lives rather than conclude it doesn't exist.
 */
function StartupFile({ values }: { values: BootstrapSetting[] }) {
  if (values.length === 0) return null;
  return (
    <Card className="mt-4">
      <CardHeader>
        <CardTitle className="text-base">In the startup file</CardTitle>
        <p className="text-sm text-muted-foreground">
          These four can't live in the database, so they stay in{" "}
          <code className="font-mono">config.yaml</code> and take a restart.
          Everything else on this page is stored here, and every change to it is
          recorded against whoever made it.
        </p>
      </CardHeader>
      <CardContent>
        <dl className="space-y-4">
          {values.map((v) => (
            <div key={v.key} className="space-y-0.5">
              <dt className="text-sm font-medium">{v.label}</dt>
              <dd className="break-all font-mono text-sm text-muted-foreground">
                {v.value}
              </dd>
              {v.help && (
                <dd className="text-xs text-muted-foreground">{v.help}</dd>
              )}
            </div>
          ))}
        </dl>
      </CardContent>
    </Card>
  );
}
