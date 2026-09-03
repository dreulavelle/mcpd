import { useCallback, useMemo } from "react";
import { api, type BootstrapSetting } from "@/lib/api";
import { useLoader } from "@/lib/hooks";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { SettingsSection } from "./SettingsSection";

/**
 * The settings an operator sets when this host goes up, and rarely again:
 * where it is reachable, whether it checks for releases, how long the history
 * is kept.
 *
 * It used to be every setting this host has -- thirty-one of them in one
 * column, behind a row of filter chips that sat under a row of tabs. What is
 * here now is eight, and the rest are on the tab that owns them.
 */
export function General() {
  const load = useCallback(() => api.settings(), []);
  const { data } = useLoader(load, "Couldn't load settings.");
  const loadEndpoints = useCallback(() => api.endpoints(), []);
  const { data: endpoints } = useLoader(loadEndpoints, "");

  // What an empty address means, as this browser sees it: the host this
  // page was reached on, at the MCP port for one and this page's own
  // address for the other. Shown in the field rather than saved, because
  // what is right from this desk may be wrong through a proxy.
  const placeholders = useMemo(() => {
    const host = window.location.hostname;
    return {
      "server.public_url": `http://${host}:${endpoints?.port ?? "8080"}`,
      "server.frontend_public_url": `${window.location.protocol}//${window.location.host}`,
    };
  }, [endpoints]);

  return (
    <SettingsSection
      section="settings"
      title="Settings"
      lede="Addresses and housekeeping. Changes apply at once unless a field says otherwise."
      placeholders={placeholders}
    >
      {data && <StartupFile values={data.bootstrap} />}
    </SettingsSection>
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
    <Card className="mb-4">
      <CardHeader>
        <CardTitle className="text-base">In the startup file</CardTitle>
        <p className="text-sm text-muted-foreground">
          These four can't live in the database, so they stay in{" "}
          <code className="font-mono">config.yaml</code> and take a restart.
          Everything else is stored here, and every change to it is recorded
          against whoever made it.
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
