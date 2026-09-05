import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react";
import { MoreHorizontal, UsersRound } from "lucide-react";
import {
  api, type Grant, type Group, type ProviderDescriptor, type User, problemText,
} from "@/lib/api";
import { usePoll } from "@/lib/hooks";
import { collect, describe } from "@/lib/permissions";
import { useCan } from "@/lib/session";
import { EmptyState, Loading, Notice, PageHeader } from "@/components/chrome";
import { Avatar } from "@/components/Avatar";
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
import { GrantsPicker, grantsLabel } from "@/components/GrantsPicker";
import { RolePicker } from "@/components/RolePicker";
import { useConfirm } from "@/components/confirm";
import { SectionHead } from "./SectionHead";

/**
 * Who can sign in, and what each of them holds.
 *
 * A table to scan and one panel to edit. The row carries the four facts an
 * audit reads -- who, which role, what that comes to, what they reach -- and
 * nothing to operate; everything that changes a person is in the sheet, so
 * the list stays a list. `embedded` renders it as one section of Users &
 * Groups rather than as a page of its own.
 */
export function Users({ embedded = false }: { embedded?: boolean } = {}) {
  const [users, setUsers] = useState<User[] | null>(null);
  const [groups, setGroups] = useState<Group[]>([]);
  // The providers this host has set up, which is what "Choose how they sign
  // in" can offer. The same list the sign-in page's buttons come from, so an
  // administrator cannot invite somebody through a provider that page will not
  // show them.
  const [providers, setProviders] = useState<ProviderDescriptor[]>([]);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<User | null>(null);
  const [query, setQuery] = useState("");
  const mayWrite = useCan("access:write");
  const notify = useNotify();

  const load = useCallback(() => {
    api.users()
      .then((r) => { setUsers(r.users ?? []); setError(""); })
      .catch(() => setError("Couldn't load accounts."));
    api.groups().then((r) => setGroups(r.groups ?? [])).catch(() => undefined);
    api.authOptions().then((r) => setProviders(r.providers ?? [])).catch(() => undefined);
  }, []);
  usePoll(load, 30_000);

  // Keep the open sheet on the latest row after a save reloads the list.
  useEffect(() => {
    if (editing && users) setEditing(users.find((u) => u.id === editing.id) ?? null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [users]);

  const shown = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q || !users) return users ?? [];
    return users.filter((u) =>
      u.email.toLowerCase().includes(q) ||
      u.name.toLowerCase().includes(q) ||
      u.role_name.toLowerCase().includes(q) ||
      u.groups.some((g) => g.name.toLowerCase().includes(q)));
  }, [users, query]);

  const head = (
    <SectionHead
      title="Users"
      count={users?.length}
      description="Each holds a role, its own reach, and whatever its groups add."
      search={users && users.length > 6 ? { value: query, onChange: setQuery, placeholder: "Find a person" } : undefined}
      action={users && mayWrite ? <Button onClick={() => setAdding(true)}>Add user</Button> : undefined}
    />
  );

  return (
    <>
      {!embedded && <PageHeader title="Users" />}
      {head}

      {error && <Notice tone="problem">{error}</Notice>}

      {adding && (
        <AddUser
          groups={groups} providers={providers}
          onClose={() => setAdding(false)}
          onAdded={(email) => { setAdding(false); load(); notify("good", `Added ${email}.`); }}
        />
      )}

      {editing && (
        <EditUser
          user={editing} groups={groups} notify={notify}
          onClose={() => setEditing(null)}
          onChanged={load}
        />
      )}

      {!users ? <Loading rows={4} /> : users.length === 0 ? (
        <EmptyState mark={<UsersRound />} title="Nobody here yet">
          Add an account, or switch on registration under Sign-in so people
          can ask for one.
        </EmptyState>
      ) : (
        <Card className="overflow-hidden p-0">
          <div className="scroll-x">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Person</TableHead>
                  <TableHead>Role</TableHead>
                  <TableHead>Reaches</TableHead>
                  <TableHead>Groups</TableHead>
                  <TableHead>Last seen</TableHead>
                  <TableHead className="w-px" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {shown.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={6} className="py-8 text-center text-muted-foreground">
                      Nobody matches that.
                    </TableCell>
                  </TableRow>
                ) : shown.map((u) => (
                  <UserRow
                    key={u.id} user={u} mayWrite={mayWrite} notify={notify}
                    onChanged={load} onEdit={() => setEditing(u)}
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

function UserRow({ user, mayWrite, notify, onChanged, onEdit }: {
  user: User;
  mayWrite: boolean;
  notify: Notify;
  onChanged: () => void;
  onEdit: () => void;
}) {
  const confirm = useConfirm();
  const [error, setError] = useState("");

  const run = async (what: string, fn: () => Promise<unknown>) => {
    setError("");
    try {
      await fn();
      onChanged();
      notify("good", what);
    } catch (e) {
      setError(problemText(e, "That didn't work."));
    }
  };

  const held = user.status === "pending" ? "Nothing until approved" : describe(collect(user.permissions));

  return (
    <TableRow className={user.disabled ? "opacity-55" : undefined}>
      <TableCell>
        <div className="flex items-center gap-3">
          <Avatar name={user.name} />
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-1.5">
              <span className="font-medium">{user.name}</span>
              {user.self && <Chip tone="info">you</Chip>}
              {user.disabled && <Chip>disabled</Chip>}
              {user.status === "pending" && <Chip tone="attention">waiting</Chip>}
              {user.invite_provider && <Chip tone="info">invited</Chip>}
            </div>
            {/* Not "no password". An invited account is one an administrator
                decided about and nobody has arrived at yet, and the thing to
                know is which provider it is waiting for. */}
            {user.invite_provider && (
              <div className="text-xs text-muted-foreground">
                Invited — waiting for a first sign-in with {user.invite_label}.
              </div>
            )}
            {/* The address always: it is what every grant and every audit
                record is keyed on, so it can never be the thing dropped. */}
            {user.name !== user.email && (
              <div className="truncate text-xs text-muted-foreground">{user.email}</div>
            )}
            {error && <div className="mt-1 text-xs text-problem">{error}</div>}
          </div>
        </div>
      </TableCell>
      <TableCell>
        <div>{user.role_name || user.role}</div>
        <div className="text-xs text-muted-foreground">{held}</div>
      </TableCell>
      <TableCell className="text-muted-foreground">{grantsLabel(user.reaches)}</TableCell>
      <TableCell>
        {user.groups.length === 0
          ? <span className="text-muted-foreground">—</span>
          : <div className="flex flex-wrap gap-1">{user.groups.map((g) => <Chip key={g.id}>{g.name}</Chip>)}</div>}
      </TableCell>
      <TableCell className="whitespace-nowrap text-muted-foreground">
        {user.last_login_at ? new Date(user.last_login_at).toLocaleDateString() : "Never"}
      </TableCell>
      <TableCell>
        {mayWrite && (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" size="icon-sm" aria-label={`Actions for ${user.email}`}>
                <MoreHorizontal />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem onSelect={onEdit}>Edit</DropdownMenuItem>
              <DropdownMenuItem onSelect={() => run(user.disabled ? "Enabled." : "Disabled.", () =>
                api.updateUser(user.id, { disabled: !user.disabled }))}>
                {user.disabled ? "Enable" : "Disable"}
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem
                variant="destructive" disabled={user.self}
                onSelect={async () => {
                  if (!(await confirm({ title: `Delete ${user.email}?`, description: "Their sessions end and this cannot be undone." }))) return;
                  run("Account deleted.", () => api.deleteUser(user.id));
                }}
              >
                Delete
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      </TableCell>
    </TableRow>
  );
}

/**
 * Everything about one person that an administrator can change, in one
 * place. Saves send only what moved.
 */
function EditUser({ user, groups, notify, onClose, onChanged }: {
  user: User;
  groups: Group[];
  notify: Notify;
  onClose: () => void;
  onChanged: () => void;
}) {
  const [role, setRole] = useState(user.role);
  const [grants, setGrants] = useState<Grant[]>(user.grants);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const inIt = new Set(user.groups.map((g) => g.id));

  const changed = role !== user.role || !sameGrants(grants, user.grants) || password !== "";

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.updateUser(user.id, {
        ...(role !== user.role ? { role } : {}),
        ...(!sameGrants(grants, user.grants) ? { grants } : {}),
        ...(password ? { password } : {}),
      });
      setPassword("");
      onChanged();
      notify("good", "Saved.");
    } catch (err) {
      setError(problemText(err, "Couldn't save that."));
    } finally {
      setBusy(false);
    }
  }

  async function membership(groupId: string, join: boolean) {
    setBusy(true);
    setError("");
    try {
      if (join) await api.addGroupMember(groupId, "user", user.id);
      else await api.removeGroupMember(groupId, "user", user.id);
      onChanged();
    } catch (err) {
      setError(problemText(err, "Couldn't change that group."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Sheet open onOpenChange={(open) => { if (!open) onClose(); }}>
      <SheetContent className="max-w-[30rem]">
        <div className="flex items-center gap-3">
          <Avatar name={user.name} className="size-10 text-sm" />
          <div className="min-w-0">
            <SheetTitle className="truncate">{user.name}</SheetTitle>
            <SheetDescription className="truncate">{user.email}</SheetDescription>
          </div>
        </div>
        {error && <Notice tone="problem">{error}</Notice>}
        <form onSubmit={save} className="space-y-5">
          <RolePicker id="edit-user-role" value={role} onChange={setRole} disabled={busy} />
          <GrantsPicker id="edit-user-grants" value={grants} onChange={setGrants} subject="this account" disabled={busy} />
          <div className="space-y-1.5">
            <Label htmlFor="edit-user-password">New password</Label>
            <Input
              id="edit-user-password" type="password" autoComplete="new-password"
              value={password} onChange={(e) => setPassword(e.target.value)}
              placeholder={user.invite_provider ? "At least 12 characters" : "Leave empty to keep the current one"}
            />
            {/* Both would leave an account somebody is already using still
                claimable by whoever holds the address at the provider. */}
            {user.invite_provider && (
              <p className="text-xs text-muted-foreground">
                Setting a password cancels the invitation to sign in with {user.invite_label}.
              </p>
            )}
          </div>
          <div className="flex items-center gap-2">
            <Button type="submit" disabled={busy || !changed}>{busy ? "Saving…" : "Save"}</Button>
            <span className="text-xs text-muted-foreground">Applies on their next request.</span>
          </div>
        </form>

        <div className="space-y-2 border-t pt-5">
          <div className="text-sm font-medium">Groups</div>
          {groups.length === 0 ? (
            <p className="text-sm text-muted-foreground">No groups yet.</p>
          ) : (
            <ul className="divide-y rounded-lg border">
              {groups.map((g) => (
                <li key={g.id} className="flex items-center gap-3 px-3 py-2 text-sm">
                  <input
                    id={`member-${g.id}`} type="checkbox" checked={inIt.has(g.id)} disabled={busy}
                    onChange={(e) => membership(g.id, e.target.checked)}
                  />
                  <label htmlFor={`member-${g.id}`} className="min-w-0 flex-1">
                    <span className="font-medium">{g.name}</span>
                    <span className="ml-2 text-xs text-muted-foreground">
                      {[g.role_name, grantsLabel(g.grants)].filter((x) => x && x !== "Nothing").join(" · ")}
                    </span>
                  </label>
                </li>
              ))}
            </ul>
          )}
          <p className="text-xs text-muted-foreground">
            Holds {describe(collect(user.permissions)).toLowerCase()}. Reaches {grantsLabel(user.reaches).toLowerCase()}.
          </p>
        </div>
      </SheetContent>
    </Sheet>
  );
}

function sameGrants(a: Grant[], b: Grant[]): boolean {
  const key = (g: Grant) => `${g.plugin}:${g.level}`;
  const x = a.map(key).sort(), y = b.map(key).sort();
  return x.length === y.length && x.every((v, i) => v === y[i]);
}

/**
 * A new person, and how they get in.
 *
 * The password half used to be the only half, which meant inventing one and
 * handing it over by some channel neither person chose. An invitation says
 * "this person signs in with Google" instead: the account is made with no
 * credential at all, and the first verified sign-in through that provider
 * claims it.
 *
 * There is deliberately no "any of them". An invitation that any provider can
 * take up is one that widens with every provider the host ever adds, and the
 * administrator is naming a person they already know how to reach.
 */
function AddUser({ groups, providers, onClose, onAdded }: {
  groups: Group[];
  providers: ProviderDescriptor[];
  onClose: () => void;
  onAdded: (email: string) => void;
}) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  // "" is a password this administrator sets; anything else is the provider
  // the invitation names.
  const [how, setHow] = useState("");
  const [role, setRole] = useState("role_operator");
  const [grants, setGrants] = useState<Grant[]>([]);
  const [joined, setJoined] = useState<string[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const inviting = how !== "";

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.createUser({
        email: email.trim(), role, grants, groups: joined,
        // One or the other, never both: an account holding an invitation and a
        // password is one whose invitation is still claimable by whoever holds
        // the address after somebody was given a password for it.
        ...(inviting ? { invite_provider: how as ProviderDescriptor["provider"] } : { password }),
      });
      onAdded(email.trim());
    } catch (err) {
      setError(problemText(err, "Couldn't add that account."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Add a user</DialogTitle>
          <DialogDescription>Choose how they sign in.</DialogDescription>
        </DialogHeader>
        {error && <Notice tone="problem">{error}</Notice>}
        <form onSubmit={submit} className="space-y-5">
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-1.5">
              <Label htmlFor="new-email">Email</Label>
              <Input id="new-email" type="email" autoComplete="off" value={email}
                     onChange={(e) => setEmail(e.target.value)} placeholder="them@example.com" />
            </div>
            {providers.length > 0 && (
              <div className="space-y-1.5">
                <Label htmlFor="new-how">How they sign in</Label>
                <NativeSelect
                  id="new-how" value={how} onChange={(e) => setHow(e.target.value)}
                >
                  <option value="">With a password you set</option>
                  {providers.map((p) => (
                    <option key={p.provider} value={p.provider}>With {p.label}</option>
                  ))}
                </NativeSelect>
              </div>
            )}
            {!inviting && (
              <div className="space-y-1.5">
                <Label htmlFor="new-password">Password</Label>
                <Input id="new-password" type="password" autoComplete="new-password"
                       value={password} onChange={(e) => setPassword(e.target.value)}
                       placeholder="At least 12 characters" />
              </div>
            )}
          </div>
          {inviting && (
            <p className="text-sm text-muted-foreground">
              They get in the first time they sign in with{" "}
              {providers.find((p) => p.provider === how)?.label}, using this address.
              The invitation lasts two weeks.
            </p>
          )}
          <RolePicker id="new-role" value={role} onChange={setRole} />
          <GrantsPicker id="new-scope" value={grants} onChange={setGrants} subject="this account" />
          {groups.length > 0 && (
            <fieldset className="space-y-1.5">
              <legend className="text-sm font-medium">Groups</legend>
              <div className="divide-y rounded-lg border">
                {groups.map((g) => (
                  <label key={g.id} className="flex items-center gap-3 px-3 py-2 text-sm">
                    <input
                      type="checkbox" checked={joined.includes(g.id)}
                      onChange={() => setJoined((c) => c.includes(g.id) ? c.filter((id) => id !== g.id) : [...c, g.id])}
                    />
                    <span className="font-medium">{g.name}</span>
                    <span className="text-xs text-muted-foreground">
                      {[g.role_name, grantsLabel(g.grants)].filter((x) => x && x !== "Nothing").join(" · ")}
                    </span>
                  </label>
                ))}
              </div>
            </fieldset>
          )}
          <DialogFooter>
            <Button variant="ghost" type="button" onClick={onClose}>Cancel</Button>
            <Button type="submit" disabled={busy || !email.trim() || (!inviting && !password) || !role}>
              {busy ? "Adding…" : inviting ? "Send the invitation" : "Add user"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
