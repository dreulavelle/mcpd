import { useCallback, useRef, useState, type FormEvent } from "react";
import {
  api,
  downloadBackup,
  stageRestore,
  type BackupPending,
  type BackupStatus,
  problemText,
} from "@/lib/api";
import { usePoll } from "@/lib/hooks";
import { Loading, Notice, PageHeader } from "@/components/chrome";
import { useNotify } from "@/components/toast";
import { Button } from "@/components/ui/button";
import {
  Card, CardContent, CardHeader, CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { BackupDestinations } from "./BackupDestinations";
import { BackupHistory } from "./BackupHistory";
import { BackupSchedule } from "./BackupSchedule";

/**
 * One instance, in one file -- taken by hand, or taken for you.
 *
 * The halves are deliberately not symmetrical. Taking a backup is immediate
 * and changes nothing, so it is a form and a button. Restoring replaces
 * everything this host knows, so the archive is checked first, while nothing
 * has moved -- a bad one is refused with the working instance still running --
 * and the host then restarts to apply it, because the database cannot be
 * swapped under a process holding it open.
 *
 * The restart is not a second thing to press. A checked archive is an operator
 * who has said what they want, and a restore left waiting would apply itself
 * on the next start of any kind: a reboot, a compose up, a crash loop. That is
 * a change firing at a moment nobody connected to a restore.
 *
 * The scheduled half sits above Restore and below the download, in the order
 * the questions get asked: take one now, arrange for one to be taken, see
 * whether the arrangement worked, and put one back.
 */
export function BackupRestore() {
  const [status, setStatus] = useState<BackupStatus | null>(null);
  const [error, setError] = useState("");

  const load = useCallback(() => {
    api.backupStatus()
      .then((s) => { setStatus(s); setError(""); })
      .catch((e) => setError(
        problemText(e, "Couldn't read the backup status.")));
  }, []);
  usePoll(load, 60_000);

  return (
    <>
      <PageHeader
        title="Backup & Restore"
        lede="A copy of this host, and putting one back."
      />

      {error && <Notice tone="problem">{error}</Notice>}

      {!status ? <Loading rows={3} /> : (
        <div className="space-y-4">
          {status.pending && (
            <Staged pending={status.pending} onChanged={load} />
          )}
          <TakeBackup status={status} />
          {/* Each of the three loads and reloads itself. `load` is passed on
              so that a change in one -- a destination switched on, a schedule
              saved, a run started -- refreshes the summary the other two read
              their next-run line and their counts from. */}
          <BackupDestinations onChanged={load} />
          <BackupSchedule schedule={status.schedule} onSaved={load} />
          <BackupHistory onRan={load} />
          <Restore status={status} onStaged={load} />
        </div>
      )}
    </>
  );
}

/**
 * A restore that was checked but has not been applied.
 *
 * Not the ordinary path any more -- a restore restarts the host itself. This
 * is what is left when that restart could not be asked for, or when the
 * process went between the two. It has to be visible, because until it is
 * applied or cancelled it is what the next start will do.
 */
function Staged({ pending, onChanged }: {
  pending: BackupPending;
  onChanged: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const notify = useNotify();

  async function act(run: () => Promise<unknown>, done: string) {
    setBusy(true);
    try {
      await run();
      notify("good", done);
      onChanged();
    } catch (e) {
      notify("problem", problemText(e, "That didn't work."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Notice tone="attention">
      <div className="space-y-3">
        <p>
          A checked restore has not been applied yet, and it will be the next
          time mcpd starts — for any reason. Nothing has changed so far. It was
          taken from{" "}
          <strong>{pending.manifest.instance || "another instance"}</strong> on{" "}
          {new Date(pending.manifest.created_at).toLocaleString()}.
        </p>
        <div className="flex flex-wrap gap-2">
          <Button
            size="sm"
            disabled={busy}
            onClick={() => act(
              () => api.restart(),
              "Restarting. This page will reconnect on its own.",
            )}
          >
            Restart and apply
          </Button>
          <Button
            size="sm"
            variant="outline"
            disabled={busy}
            onClick={() => act(() => api.cancelRestore(), "Restore cancelled.")}
          >
            Cancel it
          </Button>
        </div>
      </div>
    </Notice>
  );
}

function TakeBackup({ status }: { status: BackupStatus }) {
  const [passphrase, setPassphrase] = useState("");
  const [again, setAgain] = useState("");
  const [busy, setBusy] = useState(false);
  const notify = useNotify();

  const tooShort = passphrase.length > 0 && passphrase.length < status.min_passphrase;
  const mismatched = again.length > 0 && again !== passphrase;
  const ready = passphrase.length >= status.min_passphrase && again === passphrase;

  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    try {
      const { blob, filename } = await downloadBackup(passphrase);
      save(blob, filename);
      setPassphrase("");
      setAgain("");
      notify("good", "Backup downloaded.");
    } catch (e) {
      notify("problem",
        problemText(e, "Couldn't write the backup."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Back up</CardTitle>
        <p className="text-sm text-muted-foreground">
          Everything this host holds: settings, accounts, groups, stored
          credentials, and the history. The file is encrypted with the
          passphrase you choose, and there is no way back into it without one.
        </p>
      </CardHeader>
      <CardContent>
        <form onSubmit={submit} aria-label="Back up" className="space-y-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-1.5">
              <Label htmlFor="backup-passphrase">Passphrase</Label>
              <Input
                id="backup-passphrase"
                type="password"
                autoComplete="new-password"
                value={passphrase}
                onChange={(e) => setPassphrase(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">
                {tooShort
                  ? `At least ${status.min_passphrase} characters.`
                  : `${status.min_passphrase} characters or more. Keep it with the file.`}
              </p>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="backup-again">Again</Label>
              <Input
                id="backup-again"
                type="password"
                autoComplete="new-password"
                value={again}
                onChange={(e) => setAgain(e.target.value)}
              />
              {mismatched && (
                <p className="text-xs text-destructive">These don't match.</p>
              )}
            </div>
          </div>

          <Button type="submit" disabled={!ready || busy}>
            {busy ? "Writing…" : "Download backup"}
          </Button>
        </form>

        <Contents status={status} />
      </CardContent>
    </Card>
  );
}

/**
 * What travels, in one line each.
 *
 * The key fingerprint is here rather than buried, because it is the one fact
 * that decides whether an archive will restore somewhere else: the credentials
 * inside are encrypted with this host's key, and a host with a different one
 * cannot read them.
 */
function Contents({ status }: { status: BackupStatus }) {
  return (
    <dl className="mt-6 space-y-1.5 border-t pt-4 text-sm">
      <Row label="Database">
        {megabytes(status.database_bytes)} — settings, accounts, groups,
        credentials, approvals and the audit trail
      </Row>
      {status.tls_files > 0 && (
        <Row label="Certificates">
          This host's own authority, so clients keep trusting it
        </Row>
      )}
      {status.plugin_files > 0 && (
        <Row label="Plugins">
          {status.plugin_files} file{status.plugin_files === 1 ? "" : "s"},{" "}
          {megabytes(status.plugin_bytes)} — the plugins installed by hand, so
          a restored host is not configured for systems that are not on it
        </Row>
      )}
      {status.config_included && (
        <Row label="config.yaml">
          Carried for reference. Not restored — it holds this machine's paths
          and ports
        </Row>
      )}
      <Row label="Encryption key">
        {status.key_fingerprint ? (
          <>
            <code className="rounded bg-muted px-1 py-0.5 text-xs">
              {status.key_fingerprint}
            </code>{" "}
            — not in the file. Restoring elsewhere needs a host using this same
            key
          </>
        ) : (
          "None configured, so this host stores no credentials"
        )}
      </Row>
    </dl>
  );
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-0.5 sm:flex-row sm:gap-3">
      <dt className="w-36 shrink-0 text-muted-foreground">{label}</dt>
      <dd className="text-foreground">{children}</dd>
    </div>
  );
}

function Restore({ status, onStaged }: {
  status: BackupStatus;
  onStaged: () => void;
}) {
  const [file, setFile] = useState<File | null>(null);
  const [passphrase, setPassphrase] = useState("");
  const [busy, setBusy] = useState(false);
  const input = useRef<HTMLInputElement>(null);
  const notify = useNotify();

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!file) return;
    setBusy(true);
    try {
      const { status, note } = await stageRestore(file, passphrase);
      setFile(null);
      setPassphrase("");
      if (input.current) input.current.value = "";
      // The host says which of the two happened: it is restarting, or it could
      // not and the restore is waiting. Reporting the first when it was the
      // second would leave somebody watching for a reconnect that never comes.
      notify(status === "restoring" ? "good" : "attention", note);
      onStaged();
    } catch (e) {
      notify("problem",
        problemText(e, "Couldn't read that archive."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Restore</CardTitle>
        <p className="text-sm text-muted-foreground">
          Replaces everything on this host with the contents of an archive. The
          archive is checked first. If it is sound, mcpd restarts to apply it,
          and the database it replaces is kept.
        </p>
      </CardHeader>
      <CardContent>
        <form onSubmit={submit} aria-label="Restore" className="space-y-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-1.5">
              <Label htmlFor="restore-file">Archive</Label>
              <Input
                id="restore-file"
                ref={input}
                type="file"
                accept=".mcpdbak"
                onChange={(e) => setFile(e.target.files?.[0] ?? null)}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="restore-passphrase">Passphrase</Label>
              <Input
                id="restore-passphrase"
                type="password"
                autoComplete="off"
                value={passphrase}
                onChange={(e) => setPassphrase(e.target.value)}
              />
            </div>
          </div>

          <Button
            type="submit"
            variant="destructive"
            disabled={!file || !passphrase || busy || Boolean(status.pending)}
          >
            {busy ? "Checking…" : "Restore and restart"}
          </Button>
          {status.pending && (
            <p className="text-xs text-muted-foreground">
              A restore is already staged. Cancel it first.
            </p>
          )}
        </form>
      </CardContent>
    </Card>
  );
}

/**
 * Hands the archive to the browser.
 *
 * The object URL is revoked on the next frame rather than immediately: some
 * browsers have not started reading it when the click returns, and revoking
 * too early gives an empty file with no error anywhere.
 */
function save(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  document.body.appendChild(link);
  link.click();
  link.remove();
  setTimeout(() => URL.revokeObjectURL(url), 0);
}

function megabytes(bytes: number): string {
  if (bytes <= 0) return "empty";
  const mib = bytes / (1024 * 1024);
  return mib < 0.1 ? "under 0.1 MB" : `${mib.toFixed(1)} MB`;
}
