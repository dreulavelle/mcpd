import { useCallback, useMemo, type ReactNode } from "react";
import { api, type BootstrapSetting, type TLSStatus } from "@/lib/api";
import { useLoader } from "@/lib/hooks";
import { Notice } from "@/components/chrome";
import { Evidence } from "@/components/evidence";
import { Button } from "@/components/ui/button";
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
  const loadTLS = useCallback(() => api.tlsStatus(), []);
  const { data: tls } = useLoader(loadTLS, "");
  // Under the addresses and the certificate settings, which are what it is
  // about.
  const extras = useMemo(
    () => tls ? { server: { footer: <OwnCertificate status={tls} /> } } : undefined,
    [tls],
  );

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
      extras={extras}
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

const LOOPBACK = new Set(["localhost", "127.0.0.1", "::1"]);

function B({ children }: { children: ReactNode }) {
  return <strong className="font-medium text-foreground">{children}</strong>;
}

/**
 * mcpd's own certificate: what it is serving now, and the one thing to do so
 * browsers stop warning about it.
 *
 * What is served rather than what is set. The fields above say what the next
 * restart will do; between a change and that restart the two differ, and this
 * is the half that says which one people are getting.
 *
 * The authority is the point of the panel. A self-signed certificate is a
 * warning on every visit until the authority is trusted, and once it is --
 * pushed to every company computer by Group Policy or Intune -- it stays
 * trusted through every renewal, because only the leaf is ever reissued.
 */
export function OwnCertificate({ status }: { status: TLSStatus }) {
  const { dashboard, assistants } = status;
  if (dashboard.problem) {
    return (
      <Notice tone="problem">
        {dashboard.problem}
        <Evidence detail={dashboard.detail} />
      </Notice>
    );
  }
  if (!dashboard.on && !assistants.on) return null;

  const serving = [
    dashboard.on && "this dashboard",
    assistants.on && "the address assistants use",
  ].filter(Boolean).join(" and ");
  const reached = status.hosts.filter((h) => !LOOPBACK.has(h));
  const until = status.expires ? new Date(status.expires).toLocaleDateString() : "";

  return (
    <div className="space-y-3 rounded-md border bg-muted/40 p-3">
      <div className="space-y-1">
        <p className="text-sm font-medium">mcpd's own certificate</p>
        <p className="text-sm text-muted-foreground">
          Serving https for {serving}. It covers{" "}
          {reached.length > 0
            ? <span className="font-mono text-xs text-foreground">{reached.join(", ")}</span>
            : "only this machine"}
          {until && <> and runs until {until}. mcpd renews it a month before that</>}.
        </p>
      </div>
      {dashboard.warning && <p className="text-xs text-attention">{dashboard.warning}</p>}

      {status.authority && (
        <div className="space-y-2">
          <p className="text-sm text-muted-foreground">
            Browsers warn about it until they trust the authority that signed it.
            Install that once on each computer, and it stays trusted when mcpd
            renews the certificate.
          </p>
          <Button asChild variant="outline" size="sm">
            <a href="/api/tls/ca" download>Download mcpd's certificate authority</a>
          </Button>
          <ul className="list-disc space-y-1 pl-5 text-sm text-muted-foreground">
            <li>
              <B>Every company computer:</B> add it with Group Policy under
              Computer Configuration › Policies › Windows Settings › Security
              Settings › Public Key Policies › Trusted Root Certification
              Authorities, or as a trusted certificate profile in Intune.
            </li>
            <li>
              <B>One Windows computer:</B> open the file, choose Install
              Certificate, then Local Machine, and place it in Trusted Root
              Certification Authorities.
            </li>
            <li>
              <B>A Mac:</B> open the file, add it to the System keychain, and set
              it to Always Trust.
            </li>
          </ul>
        </div>
      )}
    </div>
  );
}
