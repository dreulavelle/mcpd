import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react";
import { KeyRound, MoreHorizontal } from "lucide-react";
import { api, type ApiKey, type Grant, type Group, type Caller, problemText } from "@/lib/api";
import { usePoll } from "@/lib/hooks";
import { collect, describe } from "@/lib/permissions";
import { Link } from "@/lib/router";
import { useCan } from "@/lib/session";
import { Copyable, EmptyState, Loading, Notice, PageHeader } from "@/components/chrome";
import { Avatar } from "@/components/Avatar";
import { GrantsPicker, grantsLabel } from "@/components/GrantsPicker";
import { RolePicker } from "@/components/RolePicker";
import { Segmented } from "@/components/Segmented";
import { Chip } from "@/components/status";
import { useNotify, type Notify } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from "@/components/ui/dialog";
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { Sheet, SheetContent, SheetDescription, SheetTitle } from "@/components/ui/sheet";
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";
import { useConfirm } from "@/components/confirm";
import { SectionHead } from "./SectionHead";

/**
 * The shapes a key usually takes. Each is a role and a level; the systems
 * are still chosen. Custom leaves both to the form.
 */
type Shape = "readonly" | "operator" | "custom";
const SHAPES: { value: Shape; label: string; title: string }[] = [
  { value: "readonly", label: "Read-only", title: "Reads, proposes nothing. A monitor, a report." },
  { value: "operator", label: "Operator", title: "Reads and proposes changes. Claude Code, Codex." },
  { value: "custom", label: "Custom", title: "Choose the role and level yourself." },
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

  useEffect(() => {
    if (editing && keys) setEditing(keys.find((k) => k.id === editing.id) ?? null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [keys]);

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
      <PageHeader
        title="API Keys"
        lede="A key lets a script or an agent call this host as itself, so the history says which."
      />
      <SectionHead
        title="Keys"
        count={keys?.length}
        search={keys && keys.length > 6 ? { value: query, onChange: setQuery, placeholder: "Find a key" } : undefined}
        action={keys && mayWrite ? <Button onClick={() => setAdding(true)}>Add key</Button> : undefined}
      />

      {error && <Notice tone="problem">{error}</Notice>}

      {adding && (
        <AddKey
          groups={groups}
          onClose={() => setAdding(false)}
          onAdded={(name, value) => { setAdding(false); setSecret({ name, value }); load(); }}
        />
      )}
      {secret && (
        <SecretOnce name={secret.name} value={secret.value} rotated={secret.rotated} onClose={() => setSecret(null)} />
      )}
      {editing && (
        <EditKey apiKey={editing} groups={groups} onClose={() => setEditing(null)}
                 onSaved={() => { load(); notify("good", "Key saved."); }} />
      )}
      {rotating && (
        <RotateKey
          apiKey={rotating}
          onClose={() => setRotating(null)}
          onRotated={(value) => { setRotating(null); setSecret({ name: rotating.name, value, rotated: true }); load(); }}
        />
      )}

      {!keys ? <Loading rows={3} /> : keys.length === 0 ? (
        <EmptyState mark={<KeyRound />} title="No keys yet">
          Keys in the configuration file keep working. Add one here for a
          script or an agent — it can be replaced, given different access or
          switched off without a restart.
        </EmptyState>
      ) : (
        <Card className="overflow-hidden p-0">
          <div className="scroll-x">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Key</TableHead>
                  <TableHead>Role</TableHead>
                  <TableHead>Reaches</TableHead>
                  <TableHead>Used</TableHead>
                  <TableHead>Expires</TableHead>
                  <TableHead className="w-px" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {shown.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={6} className="py-8 text-center text-muted-foreground">No key matches that.</TableCell>
                  </TableRow>
                ) : shown.map((k) => (
                  <KeyRow
                    key={k.id} apiKey={k} activity={activity[`key:${k.id}`]} notify={notify}
                    mayWrite={mayWrite} onChanged={load}
                    onEdit={() => setEditing(k)} onRotate={() => setRotating(k)}
                  />
                ))}
              </TableBody>
            </Table>
          </div>
        </Card>
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
function Used({ activity, principal, lastUsed }: { activity?: Caller; principal: string; lastUsed?: string }) {
  if (!activity) return <span className="text-muted-foreground">Never</span>;
  return (
    <div>
      <Link
        to={`/activity?principal=${encodeURIComponent(principal)}&hours=720`}
        className="hover:underline"
        title="Every call this key made, on Activity"
      >
        {activity.calls} call{activity.calls === 1 ? "" : "s"}
      </Link>
      {activity.denied > 0 && <span className="text-attention">, {activity.denied} refused</span>}
      <div className="text-xs text-muted-foreground">
        {(activity.plugins ?? []).join(", ")}
        {lastUsed ? ` · ${new Date(lastUsed).toLocaleDateString()}` : ""}
      </div>
    </div>
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
  const [error, setError] = useState("");
  const dead = apiKey.status !== "active";

  async function revoke() {
    if (!(await confirm({ title: `Revoke ${apiKey.name}?`, description: "Anything using it stops on its next call." }))) return;
    setError("");
    try {
      await api.revokeKey(apiKey.id);
      onChanged();
      notify("good", "Key revoked.");
    } catch (e) {
      setError(problemText(e, "That didn't work."));
    }
  }

  return (
    <TableRow className={dead ? "opacity-55" : undefined}>
      <TableCell>
        <div className="flex items-center gap-3">
          <Avatar name={apiKey.name} kind="key" />
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-1.5">
              <span className="font-medium">{apiKey.name}</span>
              {apiKey.status === "revoked" && <Chip tone="problem">revoked</Chip>}
              {apiKey.status === "expired" && <Chip tone="attention">expired</Chip>}
              {apiKey.previous_until && (
                <span title={`The old secret works until ${new Date(apiKey.previous_until).toLocaleString()}`}>
                  <Chip tone="info">rotating</Chip>
                </span>
              )}
              {apiKey.groups.map((g) => <Chip key={g.id}>{g.name}</Chip>)}
            </div>
            <div className="font-mono text-xs text-muted-foreground">{apiKey.id}</div>
            {error && <div className="mt-1 text-xs text-problem">{error}</div>}
          </div>
        </div>
      </TableCell>
      <TableCell>
        <div>{apiKey.role_name || apiKey.role}</div>
        <div className="text-xs text-muted-foreground">{describe(collect(apiKey.permissions))}</div>
      </TableCell>
      <TableCell className="text-muted-foreground">{grantsLabel(apiKey.reaches)}</TableCell>
      <TableCell><Used activity={activity} principal={`key:${apiKey.id}`} lastUsed={apiKey.last_used_at} /></TableCell>
      <TableCell className="whitespace-nowrap text-muted-foreground">
        {apiKey.expires_at ? new Date(apiKey.expires_at).toLocaleDateString() : "Never"}
      </TableCell>
      <TableCell>
        {mayWrite && !dead && (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" size="icon-sm" aria-label={`Actions for ${apiKey.name}`}><MoreHorizontal /></Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem onSelect={onEdit}>Edit</DropdownMenuItem>
              <DropdownMenuItem onSelect={onRotate}>Rotate secret</DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem variant="destructive" onSelect={revoke}>Revoke</DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
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
 * storage of any kind. There is no endpoint that would return it again.
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
            {rotated ? `${name}'s new secret` : name} is shown once. It cannot be shown again.
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
  [3600, "In an hour"],
  [86_400, "In a day"],
  [7 * 86_400, "In a week"],
];

/**
 * A new secret for the same key. Nothing else about the key moves, so every
 * rule and every audit entry naming it keeps meaning it.
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
      setError(problemText(err, "Couldn't rotate that key."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Rotate {apiKey.name}</DialogTitle>
          <DialogDescription>A new secret for the same key. Its role, reach and history stay.</DialogDescription>
        </DialogHeader>
        {error && <Notice tone="problem">{error}</Notice>}
        <form onSubmit={submit} className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="rotate-grace">The old secret stops working</Label>
            <NativeSelect id="rotate-grace" value={String(grace)} onChange={(e) => setGrace(Number(e.target.value))}>
              {GRACE.map(([seconds, label]) => <option key={seconds} value={seconds}>{label}</option>)}
            </NativeSelect>
            <p className="text-xs text-muted-foreground">Enough time to swap it in. If it has leaked, right away.</p>
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
 * Re-scoping a key without reissuing it. Only what changed is sent. The
 * secret is not here and cannot be: nothing on the server can show it again.
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
  const [expires, setExpires] = useState(apiKey.expires_at ? toDay(apiKey.expires_at) : "");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const wasExpiry = apiKey.expires_at ? toDay(apiKey.expires_at) : "";
  const wasGroups = apiKey.groups.map((g) => g.id);
  const changed =
    name.trim() !== apiKey.name || role !== apiKey.role ||
    !sameGrants(grants, apiKey.grants) || !sameSet(joined, wasGroups) || expires !== wasExpiry;

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
      setError(problemText(err, "Couldn't save that key."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Sheet open onOpenChange={(open) => { if (!open) onClose(); }}>
      <SheetContent className="max-w-[30rem]">
        <div className="flex items-center gap-3">
          <Avatar name={apiKey.name} kind="key" className="size-10 text-sm" />
          <div className="min-w-0">
            <SheetTitle className="truncate">{apiKey.name}</SheetTitle>
            <SheetDescription className="truncate font-mono text-xs">{apiKey.id}</SheetDescription>
          </div>
        </div>
        {error && <Notice tone="problem">{error}</Notice>}
        <form onSubmit={submit} className="space-y-5">
          <div className="space-y-1.5">
            <Label htmlFor="edit-key-name">Name</Label>
            <Input id="edit-key-name" value={name} autoComplete="off" onChange={(e) => setName(e.target.value)} />
          </div>
          <RolePicker id="edit-key-role" value={role} onChange={setRole} />
          <GrantsPicker id="edit-key-reach" value={grants} onChange={setGrants} subject="this key" />
          <GroupBoxes groups={groups} joined={joined} onChange={setJoined} />
          <div className="space-y-1.5">
            <Label htmlFor="edit-key-expires">Expires</Label>
            <Input id="edit-key-expires" type="date" value={expires} onChange={(e) => setExpires(e.target.value)} />
            <p className="text-xs text-muted-foreground">Clear it for a key that does not expire.</p>
          </div>
          <div className="flex items-center gap-2">
            <Button type="submit" disabled={busy || !changed || !name.trim()}>{busy ? "Saving…" : "Save"}</Button>
            <span className="text-xs text-muted-foreground">Applies on its next call.</span>
          </div>
        </form>
      </SheetContent>
    </Sheet>
  );
}

function GroupBoxes({ groups, joined, onChange }: {
  groups: Group[];
  joined: string[];
  onChange: (next: string[]) => void;
}) {
  if (groups.length === 0) return null;
  return (
    <fieldset className="space-y-1.5">
      <legend className="text-sm font-medium">Groups</legend>
      <div className="divide-y rounded-lg border">
        {groups.map((g) => (
          <label key={g.id} className="flex items-center gap-3 px-3 py-2 text-sm">
            <input
              type="checkbox" checked={joined.includes(g.id)}
              onChange={() => onChange(joined.includes(g.id) ? joined.filter((id) => id !== g.id) : [...joined, g.id])}
            />
            <span className="font-medium">{g.name}</span>
            <span className="text-xs text-muted-foreground">
              {[g.role_name, grantsLabel(g.grants)].filter((x) => x && x !== "Nothing").join(" · ")}
            </span>
          </label>
        ))}
      </div>
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
  const [shape, setShape] = useState<Shape>("operator");
  const [role, setRole] = useState("role_operator");
  const [grants, setGrants] = useState<Grant[]>([]);
  const [joined, setJoined] = useState<string[]>([]);
  const [expires, setExpires] = useState(defaultExpiry);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  function chooseShape(next: Shape) {
    setShape(next);
    if (next === "readonly") {
      setRole("role_reader");
      setGrants((g) => g.map((x) => ({ ...x, level: "read" })));
    } else if (next === "operator") {
      setRole("role_operator");
    }
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const { secret } = await api.createKey({
        name: name.trim(), role, grants, groups: joined,
        // A date input gives a day; the key dies at the start of it, in the
        // operator's own time zone, so that the row renders the day they
        // picked.
        ...(expires ? { expires_at: new Date(`${expires}T00:00:00`).toISOString() } : {}),
      });
      onAdded(name.trim(), secret);
    } catch (err) {
      setError(problemText(err, "Couldn't add that key."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Add a key</DialogTitle>
          <DialogDescription>The secret is shown once, after this.</DialogDescription>
        </DialogHeader>
        {error && <Notice tone="problem">{error}</Notice>}
        <form onSubmit={submit} className="space-y-5">
          <div className="space-y-1.5">
            <Label htmlFor="key-name">Name</Label>
            <Input id="key-name" value={name} autoComplete="off"
                   onChange={(e) => setName(e.target.value)} placeholder="Nightly report" />
          </div>
          <div className="space-y-1.5">
            <Label>Shape</Label>
            <div>
              <Segmented<Shape> label="Shape" value={shape} options={SHAPES} onChange={chooseShape} size="md" />
            </div>
            <p className="text-xs text-muted-foreground">{SHAPES.find((s) => s.value === shape)?.title}</p>
          </div>
          {shape === "custom" && <RolePicker id="key-role" value={role} onChange={setRole} />}
          <GrantsPicker
            id="key-reach" value={grants} subject="this key"
            onChange={(next) => setGrants(shape === "readonly" ? next.map((g) => ({ ...g, level: "read" })) : next)}
          />
          <GroupBoxes groups={groups} joined={joined} onChange={setJoined} />
          <div className="space-y-1.5">
            <Label htmlFor="key-expires">Expires</Label>
            <Input id="key-expires" type="date" value={expires} onChange={(e) => setExpires(e.target.value)} />
            <p className="text-xs text-muted-foreground">Ninety days to start. Clear it for a key that never expires.</p>
          </div>
          <DialogFooter>
            <Button variant="ghost" type="button" onClick={onClose}>Cancel</Button>
            <Button type="submit" disabled={busy || !name.trim() || !role}>{busy ? "Adding…" : "Add key"}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
