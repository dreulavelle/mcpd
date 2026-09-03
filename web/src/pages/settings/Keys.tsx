import { useCallback, useMemo, useState, type FormEvent } from "react";
import { KeyRound } from "lucide-react";
import {
  api, ApiError, type ApiKey, type Grant, type Group, type Caller,
} from "@/lib/api";
import { usePoll } from "@/lib/hooks";
import { collect, describe } from "@/lib/permissions";
import { Link } from "@/lib/router";
import { useCan } from "@/lib/session";
import { Copyable, EmptyState, Loading, Notice, PageHeader } from "@/components/chrome";
import { SettingsTabs } from "./SettingsTabs";
import { GrantsPicker, grantsLabel } from "@/components/GrantsPicker";
import { RolePicker } from "@/components/RolePicker";
import { Chip } from "@/components/status";
import { useNotify, type Notify } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";
import { useConfirm } from "@/components/confirm";

/**
 * The shapes a key usually takes, offered before the form so that the common
 * case is two clicks. Each is a role and a level; the systems are still
 * chosen. "Custom" leaves everything as it is.
 */
const TEMPLATES: { id: string; label: string; hint: string; role: string; level: Grant["level"] }[] = [
  { id: "readonly", label: "Read-only", hint: "Calls read tools, proposes nothing. A monitor, a report.", role: "role_reader", level: "read" },
  { id: "operator", label: "Operator", hint: "Reads and proposes changes, decides within the inline ceiling. Claude Code, Codex.", role: "role_operator", level: "write" },
  { id: "custom", label: "Custom", hint: "Pick the role and the level yourself.", role: "", level: "read" },
];

/** Ninety days, as the expiry a new key starts with. */
function defaultExpiry(): string {
  const d = new Date();
  d.setDate(d.getDate() + 90);
  return toDay(d.toISOString());
}

/** Credentials for scripts and agents. Each one acts as itself. */
export function Keys() {
  const [keys, setKeys] = useState<ApiKey[] | null>(null);
  const [groups, setGroups] = useState<Group[]>([]);
  const [activity, setActivity] = useState<Record<string, Caller>>({});
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<ApiKey | null>(null);
  const [rotating, setRotating] = useState<ApiKey | null>(null);
  const [query, setQuery] = useState("");
  // The secret lives here and nowhere else: never in storage, and gone from
  // the page the moment the dialog closes.
  const [secret, setSecret] = useState<{ name: string; value: string; rotated?: boolean } | null>(null);
  const mayWrite = useCan("access:write");
  const notify = useNotify();

  const load = useCallback(() => {
    api.keys()
      .then((r) => { setKeys(r.keys ?? []); setError(""); })
      .catch(() => setError("Couldn't load keys."));
    api.groups().then((r) => setGroups(r.groups ?? [])).catch(() => undefined);
    // One request for the page rather than one per key. A host not keeping a
    // call record answers 501, and the column reads "Nothing yet" -- which is
    // the truthful thing to say when nothing is being recorded.
    api.callers(30)
      .then((r) => setActivity(Object.fromEntries(
        (r.callers ?? []).map((c) => [c.principal, c]))))
      .catch(() => setActivity({}));
  }, []);
  usePoll(load, 30_000);

  const shown = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q || !keys) return keys ?? [];
    return keys.filter((k) =>
      k.name.toLowerCase().includes(q) ||
      k.id.toLowerCase().includes(q) ||
      k.role_name.toLowerCase().includes(q) ||
      k.groups.some((g) => g.name.toLowerCase().includes(q)));
  }, [keys, query]);

  return (
    <>
      <SettingsTabs />
      <PageHeader
        title="API Keys"
        lede="A key lets a script call this host. Each one acts as itself, so the history says which."
        actions={keys && mayWrite && <Button onClick={() => setAdding(true)}>Add key</Button>}
      />

      {error && <Notice tone="problem">{error}</Notice>}

      {adding && (
        <AddKey
          groups={groups}
          onClose={() => setAdding(false)}
          onAdded={(name, value) => {
            setAdding(false);
            setSecret({ name, value });
            load();
          }}
        />
      )}

      {secret && (
        <SecretOnce
          name={secret.name} value={secret.value} rotated={secret.rotated}
          onClose={() => setSecret(null)}
        />
      )}

      {editing && (
        <EditKey
          apiKey={editing}
          groups={groups}
          onClose={() => setEditing(null)}
          onSaved={() => { setEditing(null); load(); notify("good", "Key saved."); }}
        />
      )}

      {rotating && (
        <RotateKey
          apiKey={rotating}
          onClose={() => setRotating(null)}
          onRotated={(value) => {
            setRotating(null);
            setSecret({ name: rotating.name, value, rotated: true });
            load();
          }}
        />
      )}

      {!keys ? <Loading rows={3} /> : keys.length === 0 ? (
        <EmptyState mark={<KeyRound />} title="No keys yet">
          Tokens set in the configuration file keep working; a key made here
          can be revoked, rotated and re-scoped without a restart.
        </EmptyState>
      ) : (
        <>
          {keys.length > 8 && (
            <div className="mt-4">
              <Input
                aria-label="Find a key"
                placeholder="Find by name, id, role or group…"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                className="max-w-sm"
              />
            </div>
          )}
          <Card className="mt-4 overflow-hidden p-0">
            <div className="scroll-x">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Name</TableHead>
                    <TableHead>Role</TableHead>
                    <TableHead>Can reach</TableHead>
                    <TableHead>Has reached</TableHead>
                    <TableHead>Last used</TableHead>
                    <TableHead className="w-px" />
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {shown.length === 0 ? (
                    <TableRow>
                      <TableCell colSpan={6} className="py-6 text-center text-muted-foreground">
                        No key matches that.
                      </TableCell>
                    </TableRow>
                  ) : shown.map((k) => (
                    <KeyRow
                      key={k.id}
                      apiKey={k}
                      activity={activity[`key:${k.id}`]}
                      notify={notify}
                      mayWrite={mayWrite}
                      onChanged={load}
                      onEdit={() => setEditing(k)}
                      onRotate={() => setRotating(k)}
                    />
                  ))}
                </TableBody>
              </Table>
            </div>
          </Card>
        </>
      )}
    </>
  );
}

/**
 * What a key has actually reached, from the call ledger.
 *
 * Empty is a real answer and not a gap: a key that has never called anything
 * is the clearest candidate for revoking, so it says so rather than showing a
 * blank cell.
 */
function Reached({ activity, principal }: { activity?: Caller; principal: string }) {
  if (!activity) {
    return <span className="text-xs">Nothing yet</span>;
  }
  return (
    <span className="text-xs">
      {(activity.plugins ?? []).join(", ") || "Nothing yet"}
      <span className="block text-muted-foreground">
        <Link
          to={`/activity?principal=${encodeURIComponent(principal)}&hours=720`}
          className="hover:underline"
          title="Every call this key made, on Activity"
        >
          {activity.calls} call{activity.calls === 1 ? "" : "s"}
        </Link>
        {activity.denied > 0 && `, ${activity.denied} refused`}
      </span>
    </span>
  );
}

function KeyRow({ apiKey, activity, notify, mayWrite, onChanged, onEdit, onRotate }: {
  apiKey: ApiKey;
  activity?: Caller;
  notify: Notify;
  mayWrite: boolean;
  onChanged: () => void;
  onEdit: () => void;
  onRotate: () => void;
}) {
  const confirm = useConfirm();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const dead = apiKey.status !== "active";

  async function revoke() {
    if (!(await confirm(
      `Revoke ${apiKey.name}? Anything using it stops working on its next call.`,
    ))) return;
    setBusy(true);
    setError("");
    try {
      await api.revokeKey(apiKey.id);
      onChanged();
      notify("good", "Key revoked.");
    } catch (e) {
      setError(e instanceof ApiError ? e.detail : "That didn't work.");
    } finally {
      setBusy(false);
    }
  }

  const held = describe(collect(apiKey.permissions));

  return (
    <TableRow className={dead ? "opacity-55" : undefined}>
      <TableCell>
        <span className="flex flex-wrap items-center gap-2">
          <span className="font-medium">{apiKey.name}</span>
          {apiKey.status === "revoked" && <Chip tone="problem">revoked</Chip>}
          {apiKey.status === "expired" && <Chip tone="attention">expired</Chip>}
          {apiKey.previous_until && (
            <span title={`The old secret works until ${new Date(apiKey.previous_until).toLocaleString()}`}>
              <Chip tone="info">rotating</Chip>
            </span>
          )}
          {apiKey.groups.map((g) => <Chip key={g.id}>{g.name}</Chip>)}
        </span>
        <div className="font-mono text-xs text-muted-foreground">{apiKey.id}</div>
        {apiKey.expires_at && apiKey.status === "active" && (
          <div className="text-xs text-muted-foreground">
            Expires {new Date(apiKey.expires_at).toLocaleString()}
          </div>
        )}
        {error && <div className="mt-1 text-xs text-problem">{error}</div>}
      </TableCell>
      <TableCell className="text-muted-foreground">
        <div>{apiKey.role_name || apiKey.role}</div>
        {/* The role's name is somebody's shorthand; what the key may do is
            the union with its groups, in words the reader does not have to
            look up. */}
        <div className="text-xs">{held}</div>
      </TableCell>
      <TableCell className="text-muted-foreground">
        {grantsLabel(apiKey.reaches)}
      </TableCell>
      {/* What it is permitted to reach and what it has actually reached are
          different facts, and the gap between them is the whole of a grant
          review. A key permitted three integrations that has only ever touched
          one is the case worth seeing. */}
      <TableCell className="text-muted-foreground">
        <Reached activity={activity} principal={`key:${apiKey.id}`} />
      </TableCell>
      <TableCell className="whitespace-nowrap text-muted-foreground">
        {apiKey.last_used_at
          ? new Date(apiKey.last_used_at).toLocaleString()
          : "Never"}
      </TableCell>
      <TableCell className="whitespace-nowrap">
        {mayWrite && (
          <>
            <Button variant="ghost" size="sm" disabled={busy || dead} onClick={onEdit}>
              Edit
            </Button>
            <Button variant="ghost" size="sm" disabled={busy || dead} onClick={onRotate}>
              Rotate
            </Button>
            <Button variant="ghost" size="sm" disabled={busy || dead} onClick={revoke}>
              Revoke
            </Button>
          </>
        )}
      </TableCell>
    </TableRow>
  );
}

/**
 * The one place a secret is ever shown.
 *
 * It is rendered only while it is on screen — closing unmounts it, so it is
 * gone from the DOM rather than hidden in it — and it is never written to
 * storage of any kind. There is no endpoint that would return it again, which
 * is what the copy underneath says.
 */
function SecretOnce({ name, value, rotated, onClose }: {
  name: string;
  value: string;
  rotated?: boolean;
  onClose: () => void;
}) {
  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
      <DialogContent showCloseButton={false}>
        <DialogHeader>
          <DialogTitle>Copy this key now</DialogTitle>
          <DialogDescription>
            This is the only time {name}{rotated ? "'s new secret" : ""} will be
            shown. Nothing here can show it again, and losing it means
            {rotated ? " rotating again" : " making a new one"}.
          </DialogDescription>
        </DialogHeader>
        <Copyable value={value} label="key" />
        <DialogFooter>
          <Button onClick={onClose}>I have copied it</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

const GRACE: [number, string][] = [
  [0, "Right away"],
  [3600, "An hour"],
  [86_400, "A day"],
  [7 * 86_400, "A week"],
];

/**
 * A new secret for the same key.
 *
 * Nothing else about the key moves: its id, role, grants and groups stay, so
 * every rule and every audit entry naming it keeps meaning it. The old secret
 * keeps working for the grace chosen here, which is what lets a deployment be
 * told the new one and restarted without a window in which neither works.
 */
function RotateKey({ apiKey, onClose, onRotated }: {
  apiKey: ApiKey;
  onClose: () => void;
  onRotated: (secret: string) => void;
}) {
  const [grace, setGrace] = useState(3600);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const { secret } = await api.rotateKey(apiKey.id, grace);
      onRotated(secret);
    } catch (err) {
      setError(err instanceof ApiError ? err.detail : "Couldn't rotate that key.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Rotate {apiKey.name}</DialogTitle>
          <DialogDescription>
            A new secret for the same key. Everything else about it stays, and
            the history goes on naming it.
          </DialogDescription>
        </DialogHeader>
        {error && <Notice tone="problem">{error}</Notice>}
        <form onSubmit={submit} className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="rotate-grace">The old secret stops working</Label>
            <NativeSelect id="rotate-grace" value={String(grace)} onChange={(e) => setGrace(Number(e.target.value))}>
              {GRACE.map(([seconds, label]) => (
                <option key={seconds} value={seconds}>{label}</option>
              ))}
            </NativeSelect>
            <p className="text-xs text-muted-foreground">
              Long enough to put the new secret where the old one was. If the
              old one has leaked, choose right away.
            </p>
          </div>
          <DialogFooter>
            <Button variant="ghost" type="button" onClick={onClose}>Cancel</Button>
            <Button type="submit" disabled={busy}>{busy ? "Rotating…" : "Rotate"}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/**
 * Re-scoping a key without reissuing it.
 *
 * Only what changed is sent, so an edit to the name does not also rewrite the
 * grant with whatever the page happened to hold. The secret is not here and
 * cannot be: nothing on the server can show it again.
 */
function EditKey({ apiKey, groups, onClose, onSaved }: {
  apiKey: ApiKey;
  groups: Group[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const [name, setName] = useState(apiKey.name);
  const [role, setRole] = useState(apiKey.role);
  const [grants, setGrants] = useState<Grant[]>(apiKey.grants);
  const [joined, setJoined] = useState<string[]>(apiKey.groups.map((g) => g.id));
  // The date input wants a day; the stored value is an instant.
  const [expires, setExpires] = useState(
    apiKey.expires_at ? toDay(apiKey.expires_at) : "",
  );
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const wasExpiry = apiKey.expires_at ? toDay(apiKey.expires_at) : "";
  const wasGroups = apiKey.groups.map((g) => g.id);
  const changed =
    name.trim() !== apiKey.name || role !== apiKey.role ||
    !sameGrants(grants, apiKey.grants) || !sameSet(joined, wasGroups) ||
    expires !== wasExpiry;

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.updateKey(apiKey.id, {
        ...(name.trim() !== apiKey.name ? { name: name.trim() } : {}),
        ...(role !== apiKey.role ? { role } : {}),
        ...(!sameGrants(grants, apiKey.grants) ? { grants } : {}),
        ...(!sameSet(joined, wasGroups) ? { groups: joined } : {}),
        ...(expires !== wasExpiry
          ? { expires_at: expires ? new Date(`${expires}T00:00:00`).toISOString() : "" }
          : {}),
      });
      onSaved();
    } catch (err) {
      setError(err instanceof ApiError ? err.detail : "Couldn't save that key.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
      <DialogContent className="max-h-[90vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>Edit {apiKey.name}</DialogTitle>
          <DialogDescription>
            Takes effect on the key's next call. The secret itself does not
            change and is not shown; rotate it for a new one.
          </DialogDescription>
        </DialogHeader>
        {error && <Notice tone="problem">{error}</Notice>}
        <form onSubmit={submit} className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="edit-key-name">Name</Label>
            <Input
              id="edit-key-name" value={name} autoComplete="off"
              onChange={(e) => setName(e.target.value)}
            />
          </div>
          <RolePicker id="edit-key-role" value={role} onChange={setRole} />
          <GrantsPicker
            id="edit-key-reach" value={grants} onChange={setGrants}
            subject="this key"
          />
          <GroupBoxes groups={groups} joined={joined} onChange={setJoined} subject="A key" />
          <div className="space-y-1.5">
            <Label htmlFor="edit-key-expires">Stops working on</Label>
            <Input
              id="edit-key-expires" type="date" value={expires}
              onChange={(e) => setExpires(e.target.value)}
            />
            <p className="text-xs text-muted-foreground">
              Clear it for a key that does not expire.
            </p>
          </div>
          <DialogFooter>
            <Button variant="ghost" type="button" onClick={onClose}>Cancel</Button>
            <Button type="submit" disabled={busy || !changed || !name.trim()}>
              {busy ? "Saving…" : changed ? "Save" : "Nothing to save"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function GroupBoxes({ groups, joined, onChange, subject }: {
  groups: Group[];
  joined: string[];
  onChange: (next: string[]) => void;
  subject: string;
}) {
  if (groups.length === 0) return null;
  return (
    <fieldset className="space-y-1.5">
      <legend className="text-sm font-medium">Groups</legend>
      <p className="text-xs text-muted-foreground">
        {subject} also holds whatever its groups hold: their role and their reach.
      </p>
      {groups.map((g) => (
        <label key={g.id} className="flex items-center gap-2 text-sm">
          <input
            type="checkbox" checked={joined.includes(g.id)}
            onChange={() => onChange(joined.includes(g.id) ? joined.filter((id) => id !== g.id) : [...joined, g.id])}
          />
          <span>{g.name}</span>
          <span className="text-muted-foreground">
            — {[g.role_name, grantsLabel(g.grants)].filter(Boolean).join(", ")}
          </span>
        </label>
      ))}
    </fieldset>
  );
}

/** The local day an instant falls on, in the form a date input takes. */
function toDay(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

function sameSet(a: string[], b: string[]): boolean {
  return a.length === b.length && [...a].sort().every((v, i) => v === [...b].sort()[i]);
}

function sameGrants(a: Grant[], b: Grant[]): boolean {
  const key = (g: Grant) => `${g.plugin}:${g.level}`;
  return sameSet(a.map(key), b.map(key));
}

function AddKey({ groups, onClose, onAdded }: {
  groups: Group[];
  onClose: () => void;
  onAdded: (name: string, secret: string) => void;
}) {
  const [name, setName] = useState("");
  const [template, setTemplate] = useState("operator");
  const [role, setRole] = useState("role_operator");
  const [grants, setGrants] = useState<Grant[]>([]);
  const [joined, setJoined] = useState<string[]>([]);
  const [expires, setExpires] = useState(defaultExpiry);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  function chooseTemplate(id: string) {
    setTemplate(id);
    const t = TEMPLATES.find((x) => x.id === id);
    if (!t || !t.role) return;
    setRole(t.role);
    // The level the template means, applied to whatever systems are chosen;
    // a system chosen afterwards takes the level it is given.
    setGrants((current) => current.map((g) => ({ ...g, level: t.level })));
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const { secret } = await api.createKey({
        name: name.trim(),
        role,
        grants,
        groups: joined,
        // A date input gives a day; the key dies at the start of it, in the
        // operator's own time zone, so that the row renders the day they
        // picked. As UTC midnight it read as the evening before anywhere
        // west of Greenwich.
        ...(expires ? { expires_at: new Date(`${expires}T00:00:00`).toISOString() } : {}),
      });
      onAdded(name.trim(), secret);
    } catch (err) {
      setError(err instanceof ApiError ? err.detail : "Couldn't add that key.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Add a key</CardTitle>
      </CardHeader>
      <CardContent>
        {error && <Notice tone="problem">{error}</Notice>}
        <form onSubmit={submit} className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="key-name">Name</Label>
            <Input
              id="key-name" value={name} autoComplete="off"
              onChange={(e) => setName(e.target.value)}
              placeholder="Nightly report script"
            />
          </div>

          <fieldset className="space-y-1.5">
            <legend className="text-sm font-medium">Shape</legend>
            <div className="grid gap-2 sm:grid-cols-3">
              {TEMPLATES.map((t) => (
                <label
                  key={t.id}
                  className={`cursor-pointer rounded-md border p-2 text-sm ${template === t.id ? "border-primary bg-primary/5" : ""}`}
                >
                  <input
                    type="radio" name="key-template" value={t.id} className="sr-only"
                    checked={template === t.id} onChange={() => chooseTemplate(t.id)}
                  />
                  <div className="font-medium">{t.label}</div>
                  <div className="text-xs text-muted-foreground">{t.hint}</div>
                </label>
              ))}
            </div>
          </fieldset>

          {template === "custom" && (
            <RolePicker id="key-role" value={role} onChange={setRole} />
          )}

          <GrantsPicker
            id="key-reach" value={grants}
            onChange={(next) => {
              const t = TEMPLATES.find((x) => x.id === template);
              setGrants(t && t.role ? next.map((g) => ({ ...g, level: g.level === "write" && t.level === "read" ? "read" : g.level })) : next);
            }}
            subject="this key"
          />

          <GroupBoxes groups={groups} joined={joined} onChange={setJoined} subject="A key" />

          <div className="space-y-1.5">
            <Label htmlFor="key-expires">Stops working on</Label>
            <Input
              id="key-expires" type="date" value={expires}
              onChange={(e) => setExpires(e.target.value)}
            />
            <p className="text-xs text-muted-foreground">
              Ninety days to start with. Clear it for a key that does not
              expire, which is a choice rather than a field left blank.
            </p>
          </div>

          <div className="flex items-center gap-2">
            <Button type="submit" disabled={busy || !name.trim() || !role}>
              {busy ? "Adding…" : "Add key"}
            </Button>
            <Button variant="ghost" type="button" onClick={onClose}>Cancel</Button>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}
