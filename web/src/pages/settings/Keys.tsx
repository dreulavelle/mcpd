import { useCallback, useState, type FormEvent } from "react";
import {
  api, ApiError, type ApiKey, type Group, type Role,
} from "@/lib/api";
import { usePoll } from "@/lib/hooks";
import { Copyable, Loading, Notice, PageHeader } from "@/components/chrome";
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
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);
  // The secret lives here and nowhere else: never in storage, and gone from
  // the page the moment the dialog closes.
  const [secret, setSecret] = useState<{ name: string; value: string } | null>(null);
  const notify = useNotify();

  const load = useCallback(() => {
    api.keys()
      .then((r) => { setKeys(r.keys ?? []); setError(""); })
      .catch(() => setError("Couldn't load keys."));
    api.groups().then((r) => setGroups(r.groups ?? [])).catch(() => undefined);
  }, []);
  usePoll(load, 30_000);

  return (
    <>
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

      {!keys ? <Loading rows={3} /> : keys.length === 0 ? (
        <Notice tone="neutral">
          No keys yet. Tokens set in the configuration file keep working; a key
          made here can be revoked and re-scoped without a restart.
        </Notice>
      ) : (
        <Card className="mt-4 overflow-hidden p-0">
          <div className="scroll-x">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Role</TableHead>
                  <TableHead>Can reach</TableHead>
                  <TableHead>Last used</TableHead>
                  <TableHead className="w-px" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {keys.map((k) => (
                  <KeyRow key={k.id} apiKey={k} notify={notify} onChanged={load} />
                ))}
              </TableBody>
            </Table>
          </div>
        </Card>
      )}
    </>
  );
}

function KeyRow({ apiKey, notify, onChanged }: {
  apiKey: ApiKey;
  notify: Notify;
  onChanged: () => void;
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
      <TableCell className="whitespace-nowrap text-muted-foreground">
        {apiKey.last_used_at
          ? new Date(apiKey.last_used_at).toLocaleString()
          : "Never"}
      </TableCell>
      <TableCell className="whitespace-nowrap">
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

function AddKey({ groups, onClose, onAdded }: {
  groups: Group[];
  onClose: () => void;
  onAdded: (name: string, secret: string) => void;
}) {
  const [name, setName] = useState("");
  const [role, setRole] = useState<Role>("user");
  const [everything, setEverything] = useState(false);
  const [plugins, setPlugins] = useState("");
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
      const granted = everything
        ? ["*"]
        : plugins.split(",").map((p) => p.trim()).filter(Boolean);
      const { secret } = await api.createKey({
        name: name.trim(),
        role,
        plugins: granted,
        groups: joined,
        // A date input gives a day; the key dies at the start of it, in UTC.
        ...(expires ? { expires_at: new Date(`${expires}T00:00:00Z`).toISOString() } : {}),
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
            <Label htmlFor="key-reach">Can reach</Label>
            <NativeSelect
              id="key-reach" value={everything ? "all" : "some"}
              onChange={(e) => setEverything(e.target.value === "all")}
            >
              <option value="some">Only the systems I list</option>
              <option value="all">Every system on this host</option>
            </NativeSelect>
            {!everything && (
              <Input
                value={plugins} onChange={(e) => setPlugins(e.target.value)}
                placeholder="cnmaestro, netbox"
                aria-label="Systems this key can reach"
              />
            )}
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
