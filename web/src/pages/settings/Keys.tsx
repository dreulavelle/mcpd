import { useCallback, useMemo, useState, type FormEvent } from "react";
import { KeyRound } from "lucide-react";
import {
  api, ApiError, type ApiKey, type Group, type Role,
  type Caller,
} from "@/lib/api";
import { usePoll } from "@/lib/hooks";
import { Link } from "@/lib/router";
import { Copyable, EmptyState, Loading, Notice, PageHeader } from "@/components/chrome";
import { SettingsTabs } from "./SettingsTabs";
import { ReachPicker } from "@/components/ReachPicker";
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
import { reachLabel } from "./Groups";

const ROLES: [Role, string][] = [
  ["user", "User"],
  ["admin", "Admin"],
];

/** Credentials for scripts and agents. Each one acts as itself. */
export function Keys() {
  const [keys, setKeys] = useState<ApiKey[] | null>(null);
  const [groups, setGroups] = useState<Group[]>([]);
  const [activity, setActivity] = useState<Record<string, Caller>>({});
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<ApiKey | null>(null);
  const [query, setQuery] = useState("");
  // The secret lives here and nowhere else: never in storage, and gone from
  // the page the moment the dialog closes.
  const [secret, setSecret] = useState<{ name: string; value: string } | null>(null);
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
      k.groups.some((g) => g.name.toLowerCase().includes(q)));
  }, [keys, query]);

  return (
    <>
      <SettingsTabs />
      <PageHeader
        title="API Keys"
        lede="A key lets a script call this host. Each one acts as itself, so the history says which."
        actions={keys && <Button onClick={() => setAdding(true)}>Add key</Button>}
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

      {secret && <SecretOnce name={secret.name} value={secret.value}
                             onClose={() => setSecret(null)} />}

      {editing && (
        <EditKey
          apiKey={editing}
          onClose={() => setEditing(null)}
          onSaved={() => { setEditing(null); load(); notify("good", "Key saved."); }}
        />
      )}

      {!keys ? <Loading rows={3} /> : keys.length === 0 ? (
        <EmptyState mark={<KeyRound />} title="No keys yet">
          Tokens set in the configuration file keep working; a key made here
          can be revoked and re-scoped without a restart.
        </EmptyState>
      ) : (
        <>
          {keys.length > 8 && (
            <div className="mt-4">
              <Input
                aria-label="Find a key"
                placeholder="Find by name, id or group…"
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
                      onChanged={load}
                      onEdit={() => setEditing(k)}
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

function KeyRow({ apiKey, activity, notify, onChanged, onEdit }: {
  apiKey: ApiKey;
  activity?: Caller;
  notify: Notify;
  onChanged: () => void;
  onEdit: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const dead = apiKey.status !== "active";

  async function revoke() {
    if (!confirm(
      `Revoke ${apiKey.name}? Anything using it stops working on its next call.`,
    )) return;
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

  return (
    <TableRow className={dead ? "opacity-55" : undefined}>
      <TableCell>
        <span className="flex flex-wrap items-center gap-2">
          <span className="font-medium">{apiKey.name}</span>
          {apiKey.status === "revoked" && <Chip tone="problem">revoked</Chip>}
          {apiKey.status === "expired" && <Chip tone="attention">expired</Chip>}
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
        {apiKey.role === "admin" ? "Admin" : "User"}
      </TableCell>
      <TableCell className="text-muted-foreground">
        {reachLabel(apiKey.reaches)}
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
        <Button variant="ghost" size="sm" disabled={busy || dead} onClick={onEdit}>
          Edit
        </Button>
        <Button variant="ghost" size="sm" disabled={busy || dead} onClick={revoke}>
          Revoke
        </Button>
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
function SecretOnce({ name, value, onClose }: {
  name: string;
  value: string;
  onClose: () => void;
}) {
  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
      <DialogContent showCloseButton={false}>
        <DialogHeader>
          <DialogTitle>Copy this key now</DialogTitle>
          <DialogDescription>
            This is the only time {name} will be shown. Nothing here can show it
            again, and losing it means making a new one.
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

/**
 * Re-scoping a key without reissuing it.
 *
 * Only what changed is sent, so an edit to the name does not also rewrite the
 * grant with whatever the page happened to hold. The secret is not here and
 * cannot be: nothing on the server can show it again.
 */
function EditKey({ apiKey, onClose, onSaved }: {
  apiKey: ApiKey;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [name, setName] = useState(apiKey.name);
  const [role, setRole] = useState<Role>(apiKey.role);
  const [reach, setReach] = useState<string[]>(apiKey.plugins);
  // The date input wants a day; the stored value is an instant.
  const [expires, setExpires] = useState(
    apiKey.expires_at ? toDay(apiKey.expires_at) : "",
  );
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const wasExpiry = apiKey.expires_at ? toDay(apiKey.expires_at) : "";
  const changed =
    name.trim() !== apiKey.name || role !== apiKey.role ||
    !sameSet(reach, apiKey.plugins) || expires !== wasExpiry;

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.updateKey(apiKey.id, {
        ...(name.trim() !== apiKey.name ? { name: name.trim() } : {}),
        ...(role !== apiKey.role ? { role } : {}),
        ...(!sameSet(reach, apiKey.plugins) ? { plugins: reach } : {}),
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
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Edit {apiKey.name}</DialogTitle>
          <DialogDescription>
            Takes effect on the key's next call. The secret itself does not
            change and is not shown; a key whose secret has leaked is revoked,
            not edited.
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
          <div className="space-y-1.5">
            <Label htmlFor="edit-key-role">Role</Label>
            <NativeSelect
              id="edit-key-role" value={role}
              onChange={(e) => setRole(e.target.value as Role)}
            >
              {ROLES.map(([id, label]) => <option key={id} value={id}>{label}</option>)}
            </NativeSelect>
          </div>
          <ReachPicker
            id="edit-key-reach" value={reach} onChange={setReach}
            subject="this key"
          />
          {apiKey.groups.length > 0 && (
            <p className="text-xs text-muted-foreground">
              It also reaches whatever {apiKey.groups.map((g) => g.name).join(", ")}{" "}
              {apiKey.groups.length === 1 ? "reaches" : "reach"}. Membership is
              changed on the Users &amp; Groups tab.
            </p>
          )}
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

function AddKey({ groups, onClose, onAdded }: {
  groups: Group[];
  onClose: () => void;
  onAdded: (name: string, secret: string) => void;
}) {
  const [name, setName] = useState("");
  const [role, setRole] = useState<Role>("user");
  const [reach, setReach] = useState<string[]>([]);
  const [joined, setJoined] = useState<string[]>([]);
  const [expires, setExpires] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  function toggleGroup(id: string) {
    setJoined((current) =>
      current.includes(id) ? current.filter((g) => g !== id) : [...current, id]);
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {

      const { secret } = await api.createKey({
        name: name.trim(),
        role,
        plugins: reach,
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

          <div className="space-y-1.5">
            <Label htmlFor="key-role">Role</Label>
            <NativeSelect
              id="key-role" value={role}
              onChange={(e) => setRole(e.target.value as Role)}
            >
              {ROLES.map(([id, label]) => <option key={id} value={id}>{label}</option>)}
            </NativeSelect>
            {role === "admin" && (
              <p className="text-xs text-muted-foreground">
                An admin key can change settings, manage accounts and make more
                keys. Most scripts want User.
              </p>
            )}
          </div>

          <div className="space-y-1.5">
            <ReachPicker
              id="key-reach" value={reach} onChange={setReach}
              subject="this key"
            />
          </div>

          {groups.length > 0 && (
            <fieldset className="space-y-1.5">
              <legend className="text-sm font-medium">Groups</legend>
              <p className="text-xs text-muted-foreground">
                A key also reaches whatever its groups reach.
              </p>
              {groups.map((g) => (
                <label key={g.id} className="flex items-center gap-2 text-sm">
                  <input
                    type="checkbox" checked={joined.includes(g.id)}
                    onChange={() => toggleGroup(g.id)}
                  />
                  <span>{g.name}</span>
                  <span className="text-muted-foreground">
                    — {reachLabel(g.plugins)}
                  </span>
                </label>
              ))}
            </fieldset>
          )}

          <div className="space-y-1.5">
            <Label htmlFor="key-expires">Stops working on</Label>
            <Input
              id="key-expires" type="date" value={expires}
              onChange={(e) => setExpires(e.target.value)}
            />
            <p className="text-xs text-muted-foreground">
              Leave empty for a key that does not expire.
            </p>
          </div>

          <div className="flex items-center gap-2">
            <Button type="submit" disabled={busy || !name.trim()}>
              {busy ? "Adding…" : "Add key"}
            </Button>
            <Button variant="ghost" type="button" onClick={onClose}>Cancel</Button>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}
