import { useCallback, useState, type FormEvent } from "react";
import { api, ApiError, type Role, type User } from "@/lib/api";
import { usePoll } from "@/lib/hooks";
import { Loading, Notice, PageHeader } from "@/components/chrome";
import { Chip } from "@/components/status";
import { useNotify, type Notify } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";

const ROLES: [Role, string][] = [
  ["user", "User"],
  ["admin", "Admin"],
];

/** An account is an email address, a role, and the systems it may reach. */
export function Users() {
  const [users, setUsers] = useState<User[] | null>(null);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);
  const notify = useNotify();

  const load = useCallback(() => {
    api.users()
      .then((r) => { setUsers(r.users ?? []); setError(""); })
      .catch(() => setError("Couldn't load accounts."));
  }, []);
  usePoll(load, 30_000);

  return (
    <>
      <PageHeader
        title="Users"
        lede="Everyone signs in with their own email and password. Roles decide what they may do; the systems list decides what they can see."
        actions={users && <Button onClick={() => setAdding(true)}>Add user</Button>}
      />

      {error && <Notice tone="problem">{error}</Notice>}

      {adding && (
        <AddUser
          onClose={() => setAdding(false)}
          onAdded={(email) => { setAdding(false); load(); notify("good", `Added ${email}.`); }}
        />
      )}

      {!users ? <Loading rows={4} /> : (
        <Card className="mt-4 overflow-hidden p-0">
          <div className="scroll-x">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Email</TableHead>
                  <TableHead>Role</TableHead>
                  <TableHead>Can reach</TableHead>
                  <TableHead>Last signed in</TableHead>
                  <TableHead className="w-px" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {users.map((u) => (
                  <UserRow key={u.id} user={u} onChanged={load} notify={notify} />
                ))}
              </TableBody>
            </Table>
          </div>
        </Card>
      )}
    </>
  );
}

function UserRow({ user, onChanged, notify }: {
  user: User;
  onChanged: () => void;
  notify: Notify;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const run = async (what: string, fn: () => Promise<unknown>) => {
    setBusy(true);
    setError("");
    try {
      await fn();
      onChanged();
      notify("good", what);
    } catch (e) {
      // The refusals here are all actionable -- the last administrator, a
      // duplicate address -- so the server's text is shown.
      setError(e instanceof ApiError ? e.detail : "That didn't work.");
    } finally {
      setBusy(false);
    }
  };

  return (
    <TableRow className={user.disabled ? "opacity-55" : undefined}>
      <TableCell>
        <span className="flex flex-wrap items-center gap-2">
          {/* The name when there is one, and the address always. People can
              now set their own name, so the list would otherwise not show
              what the rest of the console calls them -- and the address is
              what every grant and every audit record is keyed on, so it can
              never be the thing that is dropped. */}
          {user.name !== user.email && <span className="font-medium">{user.name}</span>}
          <span className={user.name !== user.email ? "text-muted-foreground" : undefined}>
            {user.email}
          </span>
          {user.self && <Chip tone="info">you</Chip>}
          {user.disabled && <Chip>disabled</Chip>}
        </span>
        {error && <div className="mt-1 text-xs text-problem">{error}</div>}
      </TableCell>
      <TableCell>
        <div className="w-32">
          <NativeSelect
            aria-label="Role" value={user.role} disabled={busy}
            onChange={(e) => run("Role changed.", () =>
              api.updateUser(user.id, { role: e.target.value as Role }))}
          >
            {ROLES.map(([id, label]) => <option key={id} value={id}>{label}</option>)}
          </NativeSelect>
        </div>
      </TableCell>
      <TableCell className="text-muted-foreground">
        {user.plugins.includes("*") ? "Everything" : user.plugins.join(", ") || "Nothing"}
      </TableCell>
      <TableCell className="whitespace-nowrap text-muted-foreground">
        {user.last_login_at ? new Date(user.last_login_at).toLocaleString() : "Never"}
      </TableCell>
      <TableCell className="whitespace-nowrap">
        <Button variant="ghost" size="sm" disabled={busy}
                onClick={() => run(user.disabled ? "Enabled." : "Disabled.", () =>
                  api.updateUser(user.id, { disabled: !user.disabled }))}>
          {user.disabled ? "Enable" : "Disable"}
        </Button>
        <Button variant="ghost" size="sm" disabled={busy || user.self}
                title={user.self ? "You cannot delete the account you are signed in as" : undefined}
                onClick={() => {
                  if (!confirm(`Delete ${user.email}? This cannot be undone.`)) return;
                  run("Account deleted.", () => api.deleteUser(user.id));
                }}>
          Delete
        </Button>
      </TableCell>
    </TableRow>
  );
}

function AddUser({ onClose, onAdded }: {
  onClose: () => void;
  onAdded: (email: string) => void;
}) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState<Role>("user");
  const [everything, setEverything] = useState(true);
  const [plugins, setPlugins] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const granted = everything
        ? ["*"]
        : plugins.split(",").map((p) => p.trim()).filter(Boolean);
      await api.createUser({ email: email.trim(), password, role, plugins: granted });
      onAdded(email.trim());
    } catch (err) {
      setError(err instanceof ApiError ? err.detail : "Couldn't add that account.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Add a user</CardTitle>
      </CardHeader>
      <CardContent>
        {error && <Notice tone="problem">{error}</Notice>}
        <form onSubmit={submit} className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="new-email">Email</Label>
            <Input id="new-email" type="email" autoComplete="off" value={email}
                   onChange={(e) => setEmail(e.target.value)} placeholder="them@example.com" />
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="new-password">Password</Label>
            <Input id="new-password" type="password" autoComplete="new-password"
                   value={password} onChange={(e) => setPassword(e.target.value)}
                   placeholder="At least 12 characters" />
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="new-role">Role</Label>
            <NativeSelect id="new-role" value={role}
                          onChange={(e) => setRole(e.target.value as Role)}>
              {ROLES.map(([id, label]) => <option key={id} value={id}>{label}</option>)}
            </NativeSelect>
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="new-scope">Can reach</Label>
            <NativeSelect id="new-scope" value={everything ? "all" : "some"}
                          onChange={(e) => setEverything(e.target.value === "all")}>
              <option value="all">Every system on this host</option>
              <option value="some">Only the systems I list</option>
            </NativeSelect>
            {!everything && (
              <Input value={plugins} onChange={(e) => setPlugins(e.target.value)}
                     placeholder="cnmaestro, netbox" />
            )}
          </div>

          <div className="flex items-center gap-2">
            <Button type="submit" disabled={busy || !email.trim() || !password}>
              {busy ? "Adding…" : "Add user"}
            </Button>
            <Button variant="ghost" type="button" onClick={onClose}>Cancel</Button>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}
