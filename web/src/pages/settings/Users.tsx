import { useCallback, useState, type FormEvent } from "react";
import { api, ApiError, type Group, type Role, type User } from "@/lib/api";
import { usePoll } from "@/lib/hooks";
import { Loading, Notice, PageHeader, Section } from "@/components/chrome";
import { SettingsTabs } from "./SettingsTabs";
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
import { ReachPicker } from "@/components/ReachPicker";
import { reachLabel } from "./Groups";

const ROLES: [Role, string][] = [
  ["user", "User"],
  ["admin", "Admin"],
];

/** An account is an email address, a role, and the systems it may reach. */
/**
 * `embedded` renders this as one section of a larger page rather than as a
 * page of its own. Users and groups are one subject -- who is here, and what
 * each of them can reach -- and a host with a dozen of each does not need two
 * destinations to say so.
 */
export function Users({ embedded = false }: { embedded?: boolean } = {}) {
  const [users, setUsers] = useState<User[] | null>(null);
  const [groups, setGroups] = useState<Group[]>([]);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);
  const notify = useNotify();

  const load = useCallback(() => {
    api.users()
      .then((r) => { setUsers(r.users ?? []); setError(""); })
      .catch(() => setError("Couldn't load accounts."));
    api.groups().then((r) => setGroups(r.groups ?? [])).catch(() => undefined);
  }, []);
  usePoll(load, 30_000);

  return (
    <>
      {!embedded && <SettingsTabs />}
      {embedded ? (
        <Section
          title="Users"
          description="Roles decide what somebody may do. Groups decide what they can reach."
          actions={users && <Button onClick={() => setAdding(true)}>Add user</Button>}
        >
          <></>
        </Section>
      ) : (
        <PageHeader
          title="Users"
          lede="Roles decide what somebody may do. Groups decide what they can reach."
          actions={users && <Button onClick={() => setAdding(true)}>Add user</Button>}
        />
      )}

      {error && <Notice tone="problem">{error}</Notice>}

      {adding && (
        <AddUser
          groups={groups}
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
                  <TableHead>Groups</TableHead>
                  <TableHead>Last signed in</TableHead>
                  <TableHead className="w-px" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {users.map((u) => (
                  <UserRow
                    key={u.id} user={u} groups={groups}
                    onChanged={load} notify={notify}
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

function UserRow({ user, groups, onChanged, notify }: {
  user: User;
  groups: Group[];
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
          {/* Waiting is not switched off. The account holds nothing until an
              administrator decides, and the decision is made on the
              Authentication page beside the rules that produced it. */}
          {user.status === "pending" && <Chip tone="attention">waiting</Chip>}
          {!user.has_password && <Chip>signs in with a provider</Chip>}
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
      {/* What the account actually reaches, which is its own list unioned
          with every group's. Showing only its own would disagree with what
          the server lets it do. */}
      <TableCell className="text-muted-foreground">
        {reachLabel(user.reaches)}
      </TableCell>
      <TableCell>
        <GroupPicker user={user} groups={groups} busy={busy} run={run} />
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

/**
 * Adding and removing one group at a time.
 *
 * A select rather than a set of checkboxes because most accounts are in none
 * or one, and the chips beside it are the whole answer to "what is this
 * account in" without opening anything.
 */
function GroupPicker({ user, groups, busy, run }: {
  user: User;
  groups: Group[];
  busy: boolean;
  /** The row's own runner, so a membership change reloads and toasts like any
      other edit on the row rather than growing its own machinery. */
  run: (what: string, fn: () => Promise<unknown>) => Promise<void>;
}) {
  const inIt = new Set(user.groups.map((g) => g.id));
  const available = groups.filter((g) => !inIt.has(g.id));

  return (
    <div className="space-y-1">
      {user.groups.length > 0 && (
        <div className="flex flex-wrap gap-1">
          {user.groups.map((g) => (
            <button
              key={g.id} type="button" disabled={busy}
              title={`Take ${user.email} out of ${g.name}`}
              onClick={() => run(`Out of ${g.name}.`,
                () => api.removeGroupMember(g.id, "user", user.id))}
            >
              <Chip tone="info">{g.name} ×</Chip>
            </button>
          ))}
        </div>
      )}
      {available.length > 0 && (
        <div className="w-40">
          <NativeSelect
            aria-label={`Add ${user.email} to a group`} value="" disabled={busy}
            onChange={(e) => {
              const id = e.target.value;
              if (!id) return;
              const name = groups.find((g) => g.id === id)?.name ?? "the group";
              run(`Added to ${name}.`,
                () => api.addGroupMember(id, "user", user.id));
            }}
          >
            <option value="">Add to a group…</option>
            {available.map((g) => (
              <option key={g.id} value={g.id}>{g.name}</option>
            ))}
          </NativeSelect>
        </div>
      )}
    </div>
  );
}

function AddUser({ groups, onClose, onAdded }: {
  groups: Group[];
  onClose: () => void;
  onAdded: (email: string) => void;
}) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState<Role>("user");
  const [reach, setReach] = useState<string[]>([]);
  const [joined, setJoined] = useState<string[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.createUser({
        email: email.trim(), password, role, plugins: reach, groups: joined,
      });
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

          <ReachPicker
            id="new-scope" value={reach} onChange={setReach}
            subject="this account"
          />

          {groups.length > 0 && (
            <fieldset className="space-y-1.5">
              <legend className="text-sm font-medium">Groups</legend>
              <p className="text-xs text-muted-foreground">
                They also reach whatever their groups reach.
              </p>
              {groups.map((g) => (
                <label key={g.id} className="flex items-center gap-2 text-sm">
                  <input
                    type="checkbox" checked={joined.includes(g.id)}
                    onChange={() => setJoined((current) =>
                      current.includes(g.id)
                        ? current.filter((id) => id !== g.id)
                        : [...current, g.id])}
                  />
                  <span>{g.name}</span>
                </label>
              ))}
            </fieldset>
          )}

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
