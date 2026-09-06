import { useCallback, useState, type FormEvent, type ReactNode } from "react";
import {
  api,
  problemText,
  type BackupDestination,
  type BackupDestinationBody,
  type BackupDestinationSettings,
  type BackupKind,
  type BackupPolicy,
  type BackupTestResult,
} from "@/lib/api";
import {
  DEFAULT_POLICY, KIND_LABELS, kindShort, lastRunWords, retentionWords,
} from "@/lib/backup";
import { usePoll } from "@/lib/hooks";
import { useCan } from "@/lib/session";
import { CodeBlock, Loading, Notice } from "@/components/chrome";
import { useConfirm } from "@/components/confirm";
import { Disclosure } from "@/components/disclosure";
import { Evidence } from "@/components/evidence";
import { Chip, StatusDot, type Tone } from "@/components/status";
import { useNotify } from "@/components/toast";
import { Button } from "@/components/ui/button";
import {
  Card, CardContent, CardHeader, CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import {
  Sheet, SheetContent, SheetDescription, SheetTitle,
} from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";

/**
 * Where backups go when nobody is watching.
 *
 * A destination is a row rather than a setting because there can be several,
 * each with its own credential and its own retention: a NAS with room for a
 * year and a bucket billed by the gigabyte are not the same policy.
 *
 * The credential is write-only in both directions. The list never asks for it
 * and the host never sends it, so an edit that changes only the retention
 * carries no secret at all -- which is why the body below omits the field
 * rather than sending an empty string.
 */
export function BackupDestinations({ onChanged }: { onChanged?: () => void }) {
  const admin = useCan("system:write");
  const [rows, setRows] = useState<BackupDestination[] | null>(null);
  const [kinds, setKinds] = useState<BackupKind[]>([]);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);

  const load = useCallback(() => {
    api.backupDestinations()
      .then((r) => {
        setRows(r.destinations ?? []);
        setKinds(r.kinds ?? []);
        setError("");
      })
      .catch((e) => setError(
        problemText(e, "Couldn't read where backups are sent.")));
  }, []);
  usePoll(load, 60_000);

  const changed = () => { load(); onChanged?.(); };

  return (
    <Card>
      <CardHeader className="flex flex-row items-start justify-between gap-4">
        <div className="space-y-1.5">
          <CardTitle className="text-base">Destinations</CardTitle>
          <p className="text-sm text-muted-foreground">
            One archive is taken and sent to every destination that is switched
            on. Each keeps its own number of backups.
          </p>
        </div>
        {admin && rows && (
          <Button onClick={() => setAdding(true)}>Add destination</Button>
        )}
      </CardHeader>
      <CardContent>
        {error && <Notice tone="problem">{error}</Notice>}

        {!rows ? <Loading rows={2} /> : rows.length === 0 ? (
          <Notice tone="neutral">
            Nothing is set up yet, so a scheduled backup has nowhere to go. Add
            a folder on this machine, a NAS over SFTP, a bucket, or a WebDAV
            address.
          </Notice>
        ) : (
          <ul className="divide-y rounded-lg border">
            {rows.map((d) => (
              <DestinationRow
                key={d.id} destination={d} admin={admin} onChanged={changed}
              />
            ))}
          </ul>
        )}
      </CardContent>

      {adding && (
        <DestinationSheet
          kinds={kinds}
          onClose={() => setAdding(false)}
          onSaved={() => { setAdding(false); changed(); }}
        />
      )}
    </Card>
  );
}

function DestinationRow({ destination, admin, onChanged }: {
  destination: BackupDestination;
  admin: boolean;
  onChanged: () => void;
}) {
  const confirm = useConfirm();
  const notify = useNotify();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [editing, setEditing] = useState(false);
  // The answer, and whether a key was already pinned when it was asked for.
  //
  // The second half cannot be read off the response and cannot be read off the
  // row afterwards: a successful test on a pinned destination proves the
  // server presented that key, and a successful test on an unpinned one proves
  // nothing about it -- and by the time the answer lands, the row has been
  // reloaded and may itself carry a key this test never saw.
  const [test, setTest] = useState<Tested | null>(null);

  const last = lastRunWords(destination);

  async function act(run: () => Promise<unknown>, done?: string) {
    setBusy(true);
    setError("");
    try {
      await run();
      if (done) notify("good", done);
      onChanged();
    } catch (e) {
      setError(problemText(e, "That didn't work."));
    } finally {
      setBusy(false);
    }
  }

  /**
   * Reaches the destination, and for SFTP with nothing pinned, records the key
   * the server presented. That is the one path that learns an identity, and it
   * is deliberately the one with somebody watching: a run that learned a key
   * would be trusting whatever answered on the night.
   */
  async function runTest() {
    setBusy(true);
    setError("");
    setTest(null);
    // Read before the request, not after: the reload below can give this row a
    // key that arrived while the test was running.
    const hadPin = Boolean(destination.host_key);
    try {
      const result = await api.testBackupDestination(destination.id);
      setTest({ result, hadPin });
      // The list is reloaded either way: a recorded host key changes the row.
      onChanged();
    } catch (e) {
      setError(problemText(e, "Couldn't reach that destination."));
    } finally {
      setBusy(false);
    }
  }

  async function remove() {
    if (!(await confirm(
      `Remove ${destination.name}? No backup is sent here again. Nothing `
      + `already written there is touched.`,
    ))) return;
    await act(
      () => api.removeBackupDestination(destination.id),
      `${destination.name} removed. What is already there stays.`,
    );
  }

  return (
    <li className={destination.enabled ? "p-4" : "p-4 opacity-60"}>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-medium">{destination.name}</span>
            <Chip>{kindShort(destination.kind)}</Chip>
            {!destination.enabled && <Chip tone="neutral">Switched off</Chip>}
          </div>
          <p className="font-mono text-xs break-all text-muted-foreground">
            {destination.where}
          </p>
          <p className={cn(
            "flex items-start gap-1.5 text-xs",
            last.tone === "problem" ? "text-problem" : "text-muted-foreground",
          )}>
            <StatusDot tone={last.tone} className="mt-1" />
            {last.words}
          </p>
          {/* The sentence is above; the host's own words for it are here. */}
          <Evidence detail={destination.last_detail} />
          <p className="text-xs text-muted-foreground">
            {retentionWords(destination.policy)}
          </p>
          {error && <p className="text-xs text-problem">{error}</p>}
        </div>

        {admin && (
          <div className="flex shrink-0 items-center gap-2">
            <Switch
              checked={destination.enabled}
              disabled={busy}
              aria-label={`Send backups to ${destination.name}`}
              onCheckedChange={(on) => void act(
                () => api.updateBackupDestination(destination.id, { enabled: on }),
                on
                  ? `${destination.name} will get the next backup.`
                  : `${destination.name} is switched off. No backup is sent there.`,
              )}
            />
            <Button variant="ghost" size="sm" disabled={busy} onClick={() => void runTest()}>
              {busy ? "Testing…" : "Test connection"}
            </Button>
            <Button variant="ghost" size="sm" disabled={busy} onClick={() => setEditing(true)}>
              Edit
            </Button>
            <Button variant="ghost" size="sm" disabled={busy} onClick={() => void remove()}>
              Remove
            </Button>
          </div>
        )}
      </div>

      {test && (
        <div className="mt-3 rounded-md border bg-muted/30 p-3 text-xs">
          <p className="flex items-start gap-1.5">
            <StatusDot tone={testTone(test)} className="mt-1" />
            <span>{test.result.message}</span>
          </p>
          {test.result.host_key && (
            <div className="mt-2 space-y-1">
              <p className="text-muted-foreground">{keyWords(test)}</p>
              <CodeBlock>{test.result.host_key}</CodeBlock>
              <p className="text-muted-foreground">
                Ask the server, from a machine you trust:
              </p>
              <CodeBlock>
                {`ssh-keyscan ${destination.settings.host || "nas.example.com"} | ssh-keygen -lf -`}
              </CodeBlock>
            </div>
          )}
          <Evidence detail={test.result.detail} />
        </div>
      )}

      {editing && (
        <DestinationSheet
          destination={destination}
          kinds={[destination.kind]}
          onClose={() => setEditing(false)}
          onSaved={() => { setEditing(false); onChanged(); }}
        />
      )}
    </li>
  );
}

/**
 * Add or edit one destination.
 *
 * One form for both, because they ask the same questions. The differences are
 * the two that matter: an empty credential box on an edit means "keep the one
 * you have" rather than "there isn't one", and the kind cannot be changed --
 * changing it would keep a name, a retention and a credential that belonged to
 * a different sort of thing entirely.
 */
export function DestinationSheet({ destination, kinds, onClose, onSaved }: {
  destination?: BackupDestination;
  /** What this build can talk to, from the host rather than written down twice. */
  kinds: BackupKind[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const editing = destination !== undefined;
  const confirm = useConfirm();
  const notify = useNotify();

  const [kind, setKind] = useState<BackupKind>(
    destination?.kind ?? kinds[0] ?? "local");
  const [name, setName] = useState(destination?.name ?? "");
  const [settings, setSettings] = useState<BackupDestinationSettings>(
    destination?.settings ?? {});
  const [secret, setSecret] = useState("");
  const [policy, setPolicy] = useState<BackupPolicy>(
    destination?.policy ?? DEFAULT_POLICY);
  const [enabled, setEnabled] = useState(destination?.enabled ?? false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const set = <K extends keyof BackupDestinationSettings>(
    key: K, value: BackupDestinationSettings[K],
  ) => setSettings((s) => ({ ...s, [key]: value }));

  const keep = (key: keyof BackupPolicy) => (value: string) =>
    setPolicy((p) => ({ ...p, [key]: Number(value) || 0 }));

  // An SFTP destination cannot be switched on with nothing pinned, because
  // mcpd would have no way to tell the server apart from anything else
  // answering at that address. Test connection is what records it.
  const pinned = kind !== "sftp" || Boolean(destination?.host_key);
  const ready = name.trim() !== "" && addressed(kind, settings);

  async function forgetHostKey() {
    if (!destination) return;
    if (!(await confirm({
      title: "Forget this server's key?",
      description: "The destination is switched off at the same time, because "
        + "mcpd will not send a backup to a server it cannot recognise. Press "
        + "Test connection to record the key again.",
      action: "Forget",
    }))) return;
    setBusy(true);
    setError("");
    try {
      // Both in one request. Clearing the key alone would be refused: the host
      // will not hold an enabled SFTP destination with nothing pinned.
      await api.updateBackupDestination(destination.id, { host_key: "", enabled: false });
      notify("good", "The recorded key is gone and this destination is switched off.");
      onSaved();
    } catch (e) {
      setError(problemText(e, "That didn't work."));
    } finally {
      setBusy(false);
    }
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");

    const body: BackupDestinationBody = {
      name: name.trim(),
      settings,
      policy,
      enabled: enabled && pinned,
    };
    // The kind is sent only when adding. A PATCH carrying one is refused, and
    // rightly: it cannot be changed.
    if (!editing) body.kind = kind;
    // Omitted rather than sent empty. On an edit, an absent credential means
    // "keep the stored one"; an empty string is what erases it.
    if (secret !== "") body.secret = secret;

    try {
      const saved = editing
        ? await api.updateBackupDestination(destination.id, body)
        : await api.addBackupDestination(body);
      notify("good", editing
        ? `${saved.name} saved.`
        : `${saved.name} added. Press Test connection before switching it on.`);
      onSaved();
    } catch (e) {
      setError(problemText(e, "That destination couldn't be saved."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Sheet open onOpenChange={(o) => { if (!o) onClose(); }}>
      <SheetContent aria-describedby={undefined}>
        <div className="space-y-1.5">
          <SheetTitle>
            {editing ? `Edit ${destination.name}` : "Add a destination"}
          </SheetTitle>
          <SheetDescription>
            The password or key is stored encrypted and is never shown again.
            Nothing already written at a destination is ever removed by editing
            one.
          </SheetDescription>
        </div>

        <form onSubmit={submit} aria-label="Destination" className="space-y-5">
          <Row label="Name" id="dest-name">
            <Input
              id="dest-name" autoFocus value={name}
              placeholder="NAS"
              onChange={(e) => setName(e.target.value)}
            />
          </Row>

          <Row
            label="Kind" id="dest-kind"
            help={editing
              ? "A destination's kind cannot be changed. Remove it and add the new one."
              : undefined}
          >
            <NativeSelect
              id="dest-kind" value={kind} disabled={editing}
              onChange={(e) => {
                // The settings of the kind being left do not belong to the one
                // arriving, and a hidden host name would still be submitted.
                setKind(e.target.value as BackupKind);
                setSettings({});
                setSecret("");
              }}
            >
              {kinds.map((k) => (
                <option key={k} value={k}>{KIND_LABELS[k] ?? k}</option>
              ))}
            </NativeSelect>
          </Row>

          {kind === "local" && (
            <Row
              label="Folder" id="dest-path"
              help={"A directory on this machine that mcpd can write to, "
                + "including a share the host already mounts. It cannot be "
                + "mcpd's own data directory, or a directory holding it, and "
                + "mcpd will not create it."}
            >
              <Input
                id="dest-path" value={settings.path ?? ""}
                placeholder="/mnt/nas/mcpd"
                onChange={(e) => set("path", e.target.value)}
              />
            </Row>
          )}

          {kind === "sftp" && (
            <>
              <div className="grid gap-4 sm:grid-cols-[1fr_7rem]">
                <Row label="Address" id="dest-host">
                  <Input
                    id="dest-host" value={settings.host ?? ""}
                    placeholder="nas.example.com"
                    onChange={(e) => set("host", e.target.value)}
                  />
                </Row>
                <Row label="Port" id="dest-port">
                  <Input
                    id="dest-port" type="number" min={1} max={65535}
                    value={settings.port ?? ""} placeholder="22"
                    onChange={(e) => set("port", Number(e.target.value) || undefined)}
                  />
                </Row>
              </div>
              <Row label="User name" id="dest-user">
                <Input
                  id="dest-user" value={settings.username ?? ""}
                  placeholder="mcpd"
                  onChange={(e) => set("username", e.target.value)}
                />
              </Row>
              <div className="flex items-start gap-3">
                <Switch
                  id="dest-keyauth"
                  checked={settings.key_auth ?? false}
                  onCheckedChange={(on) => { set("key_auth", on); setSecret(""); }}
                />
                <div className="space-y-0.5">
                  <Label htmlFor="dest-keyauth">Sign in with a private key</Label>
                  <p className="text-xs text-muted-foreground">
                    Off means a password. A destination has one credential
                    either way.
                  </p>
                </div>
              </div>
              {settings.key_auth ? (
                <Row
                  label="Private key" id="dest-key"
                  help={secretHelp(editing, destination?.has_secret,
                    "The whole key, including its BEGIN and END lines. Stored encrypted.")}
                >
                  <Textarea
                    id="dest-key" rows={4} value={secret}
                    placeholder={placeholderFor(editing, destination?.has_secret,
                      "-----BEGIN OPENSSH PRIVATE KEY-----")}
                    onChange={(e) => setSecret(e.target.value)}
                  />
                </Row>
              ) : (
                <Row
                  label="Password" id="dest-password"
                  help={secretHelp(editing, destination?.has_secret, "Stored encrypted.")}
                >
                  <Input
                    id="dest-password" type="password" autoComplete="new-password"
                    value={secret}
                    placeholder={placeholderFor(editing, destination?.has_secret, "")}
                    onChange={(e) => setSecret(e.target.value)}
                  />
                </Row>
              )}
              <Row
                label="Folder" id="dest-remote"
                help="The path on the server, inside what the user can write to."
              >
                <Input
                  id="dest-remote" value={settings.remote_path ?? ""}
                  placeholder="/volume1/backups/mcpd"
                  onChange={(e) => set("remote_path", e.target.value)}
                />
              </Row>

              <HostKey
                fingerprint={destination?.host_key}
                host={settings.host}
                onForget={editing ? () => void forgetHostKey() : undefined}
                busy={busy}
              />
            </>
          )}

          {kind === "s3" && (
            <>
              <Row
                label="Address" id="dest-endpoint"
                help="The service's host, with no https:// in front of it."
              >
                <Input
                  id="dest-endpoint" value={settings.endpoint ?? ""}
                  placeholder="s3.amazonaws.com"
                  onChange={(e) => set("endpoint", e.target.value)}
                />
              </Row>
              <div className="grid gap-4 sm:grid-cols-2">
                <Row
                  label="Region" id="dest-region"
                  help="Cloudflare R2 has one region and calls it auto. Left empty, mcpd uses auto."
                >
                  <Input
                    id="dest-region" value={settings.region ?? ""}
                    placeholder="auto"
                    onChange={(e) => set("region", e.target.value)}
                  />
                </Row>
                <Row label="Bucket" id="dest-bucket">
                  <Input
                    id="dest-bucket" value={settings.bucket ?? ""}
                    placeholder="mcpd-backups"
                    onChange={(e) => set("bucket", e.target.value)}
                  />
                </Row>
              </div>
              <Row
                label="Folder" id="dest-prefix"
                help="Optional. Everything mcpd writes goes under it."
              >
                <Input
                  id="dest-prefix" value={settings.prefix ?? ""}
                  placeholder="hosts/mcpd"
                  onChange={(e) => set("prefix", e.target.value)}
                />
              </Row>
              <Row
                label="Access key" id="dest-access"
                help={"Give mcpd a credential scoped to this one bucket, allowed "
                  + "to put, list and delete. It never downloads an object."}
              >
                <Input
                  id="dest-access" value={settings.access_key ?? ""}
                  onChange={(e) => set("access_key", e.target.value)}
                />
              </Row>
              <Row
                label="Secret key" id="dest-secret"
                help={secretHelp(editing, destination?.has_secret, "Stored encrypted.")}
              >
                <Input
                  id="dest-secret" type="password" autoComplete="new-password"
                  value={secret}
                  placeholder={placeholderFor(editing, destination?.has_secret, "")}
                  onChange={(e) => setSecret(e.target.value)}
                />
              </Row>
              <Toggle
                id="dest-pathstyle"
                label="Address the bucket by path"
                help="On for MinIO and Backblaze B2. Off for AWS."
                checked={settings.path_style ?? false}
                onChange={(on) => set("path_style", on)}
              />
              <Toggle
                id="dest-insecure"
                label="Send this over plain HTTP"
                help={"The archive is encrypted, but the key signing every "
                  + "request is not. mcpd allows it only when the service is on "
                  + "this machine."}
                checked={settings.allow_insecure ?? false}
                onChange={(on) => set("allow_insecure", on)}
              />
              <Notice tone="attention">
                Backblaze B2 keeps a version of every file it deletes, and keeps
                charging for it. Retention will look like it is working and the
                bill will not agree. Add a lifecycle rule on the bucket to
                remove hidden versions after a few days.
              </Notice>
            </>
          )}

          {kind === "webdav" && (
            <>
              <Row
                label="Address" id="dest-url"
                help="The whole address of the folder. Put the user name and password in their own boxes."
              >
                <Input
                  id="dest-url" value={settings.url ?? ""}
                  placeholder="https://nas.example.com/backups/mcpd"
                  onChange={(e) => set("url", e.target.value)}
                />
              </Row>
              <Row label="User name" id="dest-dav-user">
                <Input
                  id="dest-dav-user" value={settings.username ?? ""}
                  onChange={(e) => set("username", e.target.value)}
                />
              </Row>
              <Row
                label="Password" id="dest-dav-password"
                help={secretHelp(editing, destination?.has_secret, "Stored encrypted.")}
              >
                <Input
                  id="dest-dav-password" type="password" autoComplete="new-password"
                  value={secret}
                  placeholder={placeholderFor(editing, destination?.has_secret, "")}
                  onChange={(e) => setSecret(e.target.value)}
                />
              </Row>
              <Toggle
                id="dest-dav-insecure"
                label="Send this over plain HTTP"
                help={"The archive is encrypted, but the password is not, and it "
                  + "crosses the network on every run."}
                checked={settings.allow_insecure ?? false}
                onChange={(on) => set("allow_insecure", on)}
              />
            </>
          )}

          <div className="space-y-4 border-t pt-4">
            <Row
              label="Keep the last" id="dest-keeplast"
              help="Archives. Whatever the numbers say, the newest is never removed."
            >
              <Input
                id="dest-keeplast" type="number" min={1}
                value={String(policy.keep_last)}
                onChange={(e) => keep("keep_last")(e.target.value)}
              />
            </Row>
            <Disclosure summary="Advanced">
              <p className="text-xs text-muted-foreground">
                Also keep the newest archive in each of the last so many days,
                weeks and months. The rules are added together: anything any of
                them keeps is kept. Zero switches one off.
              </p>
              <div className="grid gap-4 sm:grid-cols-3">
                <Row label="Days" id="dest-keepdaily">
                  <Input
                    id="dest-keepdaily" type="number" min={0}
                    value={String(policy.keep_daily)}
                    onChange={(e) => keep("keep_daily")(e.target.value)}
                  />
                </Row>
                <Row label="Weeks" id="dest-keepweekly">
                  <Input
                    id="dest-keepweekly" type="number" min={0}
                    value={String(policy.keep_weekly)}
                    onChange={(e) => keep("keep_weekly")(e.target.value)}
                  />
                </Row>
                <Row label="Months" id="dest-keepmonthly">
                  <Input
                    id="dest-keepmonthly" type="number" min={0}
                    value={String(policy.keep_monthly)}
                    onChange={(e) => keep("keep_monthly")(e.target.value)}
                  />
                </Row>
              </div>
            </Disclosure>
          </div>

          <div className="border-t pt-4">
            <Toggle
              id="dest-enabled"
              label="Send backups here"
              help={pinned
                ? "Off means the destination is kept and skipped."
                : "Save this first, then press Test connection. mcpd records the "
                  + "key the server presents, and will not send a backup to a "
                  + "server it cannot recognise."}
              checked={enabled && pinned}
              disabled={!pinned}
              onChange={setEnabled}
            />
          </div>

          {error && <Notice tone="problem">{error}</Notice>}

          <div className="flex gap-2 border-t pt-4">
            <Button type="submit" disabled={busy || !ready}>
              {busy ? "Saving…" : editing ? "Save" : "Add destination"}
            </Button>
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
          </div>
        </form>
      </SheetContent>
    </Sheet>
  );
}

/** One Test connection answer, with what was pinned when it was asked for. */
interface Tested {
  result: BackupTestResult;
  hadPin: boolean;
}

/**
 * What may honestly be said about the key a server just presented.
 *
 * Three cases, and only one of them is a match. A destination that already had
 * a key pinned was checked against it by the handshake, so a test that worked
 * does prove the server presented that key. A destination that had none was
 * reached with checking turned off, so nothing about the key is proved -- and
 * when the host declines to store it, because somebody pinned a different one
 * in between, the fingerprint on screen is not the one a backup will be
 * checked against. That last case used to read as a match.
 */
function keyWords({ result, hadPin }: Tested): string {
  if (result.host_key_recorded) {
    return "This is the key the server presented, and it is now the only one "
      + "mcpd will accept. Compare it with what the server says of itself "
      + "before switching this destination on.";
  }
  if (hadPin && result.ok) {
    return "This is the key the server presented, and it is the one already "
      + "recorded for this destination.";
  }
  return "This is the key the server presented. It has not been recorded, so "
    + "it is not what a backup will be checked against.";
}

/**
 * How the answer is coloured.
 *
 * A test that reached the destination but did not record the key it was asked
 * to record is not a success: the sentence above it says a different key is
 * pinned, and a green dot beside that would have somebody switch the
 * destination on and every backup afterwards refused.
 */
function testTone({ result, hadPin }: Tested): Tone {
  if (!result.ok) return "problem";
  if (!hadPin && result.host_key && !result.host_key_recorded) return "attention";
  return "good";
}

/**
 * The server's identity, once mcpd has one.
 *
 * Read-only on purpose. It is not a field somebody fills in: it is what the
 * server presented while a person was watching, and the act that replaces it
 * is deliberately separate and deliberately switches the destination off.
 */
function HostKey({ fingerprint, host, onForget, busy }: {
  fingerprint?: string;
  host?: string;
  onForget?: () => void;
  busy: boolean;
}) {
  if (!fingerprint) {
    return (
      <p className="text-xs text-muted-foreground">
        No key is recorded for this server yet. Press Test connection once this
        is saved; mcpd records the key the server presents and shows it to you.
      </p>
    );
  }
  return (
    <div className="space-y-1.5">
      <Label>Server key</Label>
      <CodeBlock>{fingerprint}</CodeBlock>
      <p className="text-xs text-muted-foreground">
        A server presenting anything else is refused and the backup is not sent.
        Ask the server itself, from a machine you trust, and compare the two:
      </p>
      <CodeBlock>{`ssh-keyscan ${host || "nas.example.com"} | ssh-keygen -lf -`}</CodeBlock>
      {onForget && (
        <Button type="button" variant="outline" size="sm" disabled={busy} onClick={onForget}>
          Forget host key
        </Button>
      )}
    </div>
  );
}

function Row({ label, id, help, children }: {
  label: string;
  id: string;
  help?: string;
  children: ReactNode;
}) {
  return (
    <div className="space-y-1.5">
      <Label htmlFor={id}>{label}</Label>
      {children}
      {help && <p className="text-xs text-muted-foreground">{help}</p>}
    </div>
  );
}

function Toggle({ id, label, help, checked, disabled, onChange }: {
  id: string;
  label: string;
  help?: string;
  checked: boolean;
  disabled?: boolean;
  onChange: (on: boolean) => void;
}) {
  return (
    <div className="flex items-start gap-3">
      <Switch id={id} checked={checked} disabled={disabled} onCheckedChange={onChange} />
      <div className="space-y-0.5">
        <Label htmlFor={id}>{label}</Label>
        {help && <p className="text-xs text-muted-foreground">{help}</p>}
      </div>
    </div>
  );
}

/** What an empty credential box means, which is not the same on both paths. */
function secretHelp(editing: boolean, hasSecret: boolean | undefined, base: string): string {
  if (editing && hasSecret) return `Leave it blank to keep the stored one. ${base}`;
  return base;
}

function placeholderFor(
  editing: boolean, hasSecret: boolean | undefined, fallback: string,
): string {
  return editing && hasSecret ? "leave blank to keep the stored one" : fallback;
}

/**
 * Whether the one field naming where this destination points is filled in.
 *
 * The same rule the host applies, and no stricter. A form that refuses more
 * than the server does is a form somebody cannot get past with a
 * configuration that would have worked.
 */
function addressed(kind: BackupKind, s: BackupDestinationSettings): boolean {
  switch (kind) {
    case "local": return (s.path ?? "").trim() !== "";
    case "sftp": return (s.host ?? "").trim() !== "" && (s.username ?? "").trim() !== "";
    case "s3": return (s.endpoint ?? "").trim() !== "" && (s.bucket ?? "").trim() !== "";
    case "webdav": return (s.url ?? "").trim() !== "";
    default: return false;
  }
}
