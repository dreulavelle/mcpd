import { useCallback, useMemo, useRef, useState, type FormEvent, type ReactNode } from "react";
import {
  api, problemText, type BootstrapSetting, type CertificateInfo, type TLSStatus,
} from "@/lib/api";
import { useLoader } from "@/lib/hooks";
import { useCan } from "@/lib/session";
import { CodeBlock, Notice } from "@/components/chrome";
import { useConfirm } from "@/components/confirm";
import { Evidence } from "@/components/evidence";
import { Chip } from "@/components/status";
import { useNotify } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Label } from "@/components/ui/label";
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
  const { data: tls, reload: reloadTLS } = useLoader(loadTLS, "");
  // Under the addresses and the certificate settings, which are what it is
  // about.
  const extras = useMemo(
    () => tls
      ? { server: { footer: <DashboardCertificate status={tls} onChanged={reloadTLS} /> } }
      : undefined,
    [tls, reloadTLS],
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

function day(iso: string): string {
  return new Date(iso).toLocaleDateString();
}

function Warnings({ of }: { of?: CertificateInfo }) {
  if (!of?.warnings?.length) return null;
  return (
    <>
      {of.warnings.map((w) => <p key={w} className="text-xs text-attention">{w}</p>)}
    </>
  );
}

/**
 * What this dashboard presents, and what to do about it: install mcpd's
 * authority once, or upload a certificate of your own.
 *
 * What is served rather than what is set. The field above says what the next
 * restart will do; between a change and that restart the two differ, and this
 * is the half that says which one people are getting.
 */
export function DashboardCertificate({ status, onChanged }: {
  status: TLSStatus;
  onChanged: () => void;
}) {
  const { dashboard } = status;
  const mayWrite = useCan("settings:write");
  const own = !!status.own;
  // Offered to an administrator whatever the setting says, because uploading
  // the certificate and choosing to serve it are two acts and the natural
  // order is that one. It used to appear only once the setting had already
  // been changed, so somebody looking for where to upload found nothing.
  const yours = mayWrite || !!status.provided;
  if (!dashboard.problem && !status.restart_needed && !own && !yours) return null;

  return (
    <div className="space-y-3">
      {dashboard.problem && (
        <Notice tone="problem">
          {dashboard.problem}
          <Evidence detail={dashboard.detail} />
        </Notice>
      )}
      {status.restart_needed && (
        <Notice tone="attention">
          Restart mcpd to change the certificate this dashboard serves.
        </Notice>
      )}
      {own && <OwnCertificate status={status} />}
      {yours && (
        <YourCertificate status={status} mayWrite={mayWrite} onChanged={onChanged} />
      )}
    </div>
  );
}

/**
 * mcpd's own certificate. The authority is the point of it: a self-signed
 * certificate is a warning on every visit until the authority is trusted, and
 * once it is -- pushed to every company computer by Group Policy or Intune --
 * it stays trusted through every renewal, because only the leaf is reissued.
 */
function OwnCertificate({ status }: { status: TLSStatus }) {
  const own = status.own!;
  const serving = [
    status.dashboard.source === "own" && "this dashboard",
    status.assistants.on && "the address assistants use",
  ].filter(Boolean).join(" and ");
  const reached = own.hosts.filter((h) => !LOOPBACK.has(h));

  return (
    <div className="space-y-3 rounded-md border bg-muted/40 p-3">
      <div className="space-y-1">
        <p className="text-sm font-medium">mcpd's own certificate</p>
        <p className="text-sm text-muted-foreground">
          Serving https for {serving || "nothing yet"}. It covers{" "}
          {reached.length > 0
            ? <span className="font-mono text-xs text-foreground">{reached.join(", ")}</span>
            : "only this machine"}
          {" "}and runs until {day(own.not_after)}. mcpd renews it a month before that.
        </p>
      </div>
      <Warnings of={own} />

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

/**
 * A certificate somebody else issued: most usefully one from the company's
 * own authority, which its computers already trust, so nobody installs
 * anything. Nothing renews it, so what it covers and when it runs out are on
 * the page rather than in a file somebody has to open.
 */
function YourCertificate({ status, mayWrite, onChanged }: {
  status: TLSStatus;
  /** A reader sees what is installed; only a writer uploads or removes. */
  mayWrite: boolean;
  onChanged: () => void;
}) {
  const p = status.provided;
  const inUse = status.dashboard.source === "provided";
  // What is left before it is served. The setting above decides it, so this
  // says which way it is set rather than leaving somebody to work out why an
  // uploaded certificate is not in use.
  const toServe = status.dashboard_mode === "custom"
    ? "Restart mcpd to serve it."
    : "To serve it, set Certificate for this dashboard to Your own certificate, above, then restart mcpd.";
  const confirm = useConfirm();
  const notify = useNotify();
  const [problem, setProblem] = useState("");

  async function remove() {
    if (!(await confirm({
      title: "Remove your certificate?",
      description: "It isn't in use. You can upload it, or another, again.",
      action: "Remove",
    }))) return;
    setProblem("");
    try {
      await api.removeDashboardCertificate();
      notify("good", "Removed.");
      onChanged();
    } catch (e) {
      setProblem(problemText(e, "Couldn't remove it."));
    }
  }

  return (
    <div className="space-y-3 rounded-md border bg-muted/40 p-3">
      <div className="flex flex-wrap items-center gap-2">
        <p className="text-sm font-medium">Your certificate</p>
        {p && <Chip tone={inUse ? "good" : "neutral"}>{inUse ? "In use" : "Not in use yet"}</Chip>}
      </div>

      {p ? (
        <>
          <dl className="grid gap-x-4 gap-y-1 text-sm sm:grid-cols-[auto_1fr]">
            <dt className="text-muted-foreground">Issued to</dt>
            <dd>{p.subject}</dd>
            <dt className="text-muted-foreground">Issued by</dt>
            <dd>{p.issuer}</dd>
            <dt className="text-muted-foreground">Covers</dt>
            <dd className="font-mono text-xs break-all">{p.hosts.join(", ") || "Nothing by name"}</dd>
            <dt className="text-muted-foreground">Runs until</dt>
            <dd>{day(p.not_after)}</dd>
          </dl>
          <Warnings of={p} />
          <Evidence detail={`SHA-256 fingerprint ${p.fingerprint}`} />
        </>
      ) : (
        <p className="text-sm text-muted-foreground">
          None uploaded yet. Ask your company's certificate authority, or any
          public one, for a server certificate covering the address this page is
          on, then paste it here or choose the file.
        </p>
      )}
      {!inUse && <p className="text-sm text-muted-foreground">{toServe}</p>}

      {problem && <Notice tone="problem">{problem}</Notice>}
      {mayWrite && <UploadCertificate replacing={!!p} onSaved={onChanged} />}
      {mayWrite && p && !inUse && (
        <Button type="button" variant="ghost" size="sm" onClick={remove}>
          Remove
        </Button>
      )}
    </div>
  );
}

/**
 * The upload. Two boxes because a certificate and its key usually arrive as
 * two files, but one file holding everything can go in the first: the server
 * looks for each piece wherever it is.
 */
function UploadCertificate({ replacing, onSaved }: {
  replacing: boolean;
  onSaved: () => void;
}) {
  const [open, setOpen] = useState(!replacing);
  const [cert, setCert] = useState("");
  const [key, setKey] = useState("");
  const [binary, setBinary] = useState(false);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState("");
  const notify = useNotify();

  if (!open) {
    return (
      <Button type="button" variant="outline" size="sm" onClick={() => setOpen(true)}>
        Replace it
      </Button>
    );
  }

  // A .pfx is the usual thing a Windows authority hands over, and it is
  // binary. It is caught here, where the command to convert it can be shown
  // for copying, rather than sent and refused.
  async function pick(file: File, into: (text: string) => void) {
    const text = await file.text();
    if (!text.includes("-----BEGIN")) {
      setBinary(true);
      return;
    }
    setBinary(false);
    into(text);
  }

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setProblem("");
    try {
      const status = await api.setDashboardCertificate(cert, key);
      notify("good", status.dashboard.source === "provided"
        ? "Saved. The dashboard is serving it now."
        : "Saved.");
      setCert("");
      setKey("");
      if (replacing) setOpen(false);
      onSaved();
    } catch (err) {
      setProblem(problemText(err, "Couldn't save that certificate."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={save} className="space-y-3">
      <p className="text-sm text-muted-foreground">
        Paste the certificate issued for this dashboard, with its authority's
        certificates after it, and its private key. One file holding all of it
        can go in the first box.
      </p>
      <PemBox
        id="dashboard-certificate" label="Certificate" value={cert} onChange={setCert}
        onPick={(f) => pick(f, setCert)} placeholder="-----BEGIN CERTIFICATE-----"
      />
      <PemBox
        id="dashboard-key" label="Private key" value={key} onChange={setKey}
        onPick={(f) => pick(f, setKey)} placeholder="-----BEGIN PRIVATE KEY-----"
      />
      {binary && (
        <div className="space-y-1.5">
          <p className="text-xs text-attention">
            That file isn't PEM text. Convert a .pfx or .p12 file first, then
            choose the file it makes:
          </p>
          <CodeBlock>{"openssl pkcs12 -in certificate.pfx -nodes -out certificate.pem"}</CodeBlock>
        </div>
      )}
      {problem && <Notice tone="problem">{problem}</Notice>}
      <div className="flex gap-2">
        <Button type="submit" size="sm" disabled={busy || !cert.trim()}>
          {busy ? "Saving…" : "Save certificate"}
        </Button>
        {replacing && (
          <Button type="button" variant="ghost" size="sm" onClick={() => setOpen(false)}>
            Cancel
          </Button>
        )}
      </div>
    </form>
  );
}

function PemBox({ id, label, value, onChange, onPick, placeholder }: {
  id: string;
  label: string;
  value: string;
  onChange: (text: string) => void;
  onPick: (file: File) => void;
  placeholder: string;
}) {
  const picker = useRef<HTMLInputElement>(null);
  return (
    <div className="space-y-1.5">
      <div className="flex items-center justify-between gap-2">
        <Label htmlFor={id}>{label}</Label>
        <Button type="button" variant="ghost" size="sm" onClick={() => picker.current?.click()}>
          Choose a file
        </Button>
        <input
          ref={picker} type="file" className="hidden" aria-hidden="true" tabIndex={-1}
          accept=".pem,.crt,.cer,.key,.txt"
          onChange={(e) => {
            const chosen = e.target.files?.[0];
            if (chosen) onPick(chosen);
            e.target.value = "";
          }}
        />
      </div>
      <textarea
        id={id} value={value} spellCheck={false} autoComplete="off" placeholder={placeholder}
        onChange={(e) => onChange(e.target.value)}
        className="h-28 w-full rounded-md border bg-background p-2 font-mono text-xs"
      />
    </div>
  );
}
