import { useCallback, useEffect, useState, type FormEvent } from "react";
import { MoreHorizontal, UsersRound } from "lucide-react";
import { api, ApiError, type Grant, type Group, type GroupMember } from "@/lib/api";
import { usePoll } from "@/lib/hooks";
import { useCan } from "@/lib/session";
import { EmptyState, Loading, Notice, PageHeader } from "@/components/chrome";
import { Avatar } from "@/components/Avatar";
import { GrantsPicker, grantsLabel } from "@/components/GrantsPicker";
import { RolePicker } from "@/components/RolePicker";
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
import { Sheet, SheetContent, SheetDescription, SheetTitle } from "@/components/ui/sheet";
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";
import { useConfirm } from "@/components/confirm";
import { SectionHead } from "./SectionHead";

/** Renders reach the way every other list of systems here is rendered. */
export function reachLabel(grants: Grant[]): string {
  return grantsLabel(grants);
}

/**
 * A group hands its role and its reach to everyone in it.
 *
 * The same shape as Users: a table to scan, a sheet to edit. `embedded`
 * renders it as one section of Users & Groups.
 */
export function Groups({ embedded = false }: { embedded?: boolean } = {}) {
  const [groups, setGroups] = useState<Group[] | null>(null);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<Group | null>(null);
  const mayWrite = useCan("access:write");
  const notify = useNotify();

  const load = useCallback(() => {
    api.groups()
      .then((r) => { setGroups(r.groups ?? []); setError(""); })
      .catch(() => setError("Couldn't load groups."));
  }, []);
  usePoll(load, 30_000);

  useEffect(() => {
    if (editing && groups) setEditing(groups.find((g) => g.id === editing.id) ?? null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [groups]);

  return (
    <>
      {!embedded && <PageHeader title="Groups" />}
      <SectionHead
        title="Groups"
        count={groups?.length}
        description="Everyone in a group holds its role and its reach, on top of their own."
        action={groups && mayWrite ? <Button onClick={() => setAdding(true)}>Add group</Button> : undefined}
      />

      {error && <Notice tone="problem">{error}</Notice>}

      {adding && (
        <AddGroup
          onClose={() => setAdding(false)}
          onAdded={(name) => { setAdding(false); load(); notify("good", `Added ${name}.`); }}
        />
      )}

      {editing && (
        <EditGroup group={editing} notify={notify} mayWrite={mayWrite}
                   onClose={() => setEditing(null)} onChanged={load} />
      )}

      {!groups ? <Loading rows={3} /> : groups.length === 0 ? (
        <EmptyState mark={<UsersRound />} title="No groups yet">
          A group is the quickest way to give several people, or several
          keys, the same access.
        </EmptyState>
      ) : (
        <Card className="overflow-hidden p-0">
          <div className="scroll-x">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Group</TableHead>
                  <TableHead>Adds role</TableHead>
                  <TableHead>Adds reach</TableHead>
                  <TableHead>Members</TableHead>
                  <TableHead className="w-px" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {groups.map((g) => (
                  <GroupRow key={g.id} group={g} mayWrite={mayWrite} notify={notify}
                            onChanged={load} onEdit={() => setEditing(g)} />
                ))}
              </TableBody>
            </Table>
          </div>
        </Card>
      )}
    </>
  );
}

function GroupRow({ group, mayWrite, notify, onChanged, onEdit }: {
  group: Group;
  mayWrite: boolean;
  notify: Notify;
  onChanged: () => void;
  onEdit: () => void;
}) {
  const confirm = useConfirm();
  const [error, setError] = useState("");

  async function remove() {
    const who = group.members === 1 ? "1 member" : `${group.members} members`;
    if (!(await confirm({
      title: `Delete ${group.name}?`,
      description: `${who} will lose what it adds. Nothing else about them changes.`,
    }))) return;
    setError("");
    try {
      await api.deleteGroup(group.id);
      onChanged();
      notify("good", "Group deleted.");
    } catch (e) {
      setError(e instanceof ApiError ? e.detail : "That didn't work.");
    }
  }

  return (
    <TableRow>
      <TableCell>
        <div className="flex items-center gap-3">
          <Avatar name={group.name} kind="group" />
          <div className="min-w-0">
            <div className="font-medium">{group.name}</div>
            {group.description && <div className="text-xs text-muted-foreground">{group.description}</div>}
            {error && <div className="mt-1 text-xs text-problem">{error}</div>}
          </div>
        </div>
      </TableCell>
      <TableCell className="text-muted-foreground">{group.role ? group.role_name || group.role : "—"}</TableCell>
      <TableCell className="text-muted-foreground">{group.grants.length === 0 ? "—" : grantsLabel(group.grants)}</TableCell>
      <TableCell className="whitespace-nowrap text-muted-foreground tabular-nums">{group.members}</TableCell>
      <TableCell>
        <div className="flex items-center justify-end gap-1">
          <Button variant="ghost" size="sm" onClick={onEdit}>{mayWrite ? "Edit" : "Show"}</Button>
          {mayWrite && (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="ghost" size="icon-sm" aria-label={`Actions for ${group.name}`}><MoreHorizontal /></Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuItem onSelect={onEdit}>Edit</DropdownMenuItem>
                <DropdownMenuSeparator />
                <DropdownMenuItem variant="destructive" onSelect={remove}>Delete</DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          )}
        </div>
      </TableCell>
    </TableRow>
  );
}

function EditGroup({ group, notify, mayWrite, onClose, onChanged }: {
  group: Group;
  notify: Notify;
  mayWrite: boolean;
  onClose: () => void;
  onChanged: () => void;
}) {
  const [members, setMembers] = useState<GroupMember[] | null>(null);
  const [name, setName] = useState(group.name);
  const [description, setDescription] = useState(group.description);
  const [role, setRole] = useState(group.role);
  const [grants, setGrants] = useState<Grant[]>(group.grants);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(() => {
    api.group(group.id)
      .then((r) => setMembers(r.members ?? []))
      .catch(() => setError("Couldn't load who is in this group."));
  }, [group.id]);
  usePoll(load, 30_000);

  const changed = name.trim() !== group.name || description.trim() !== group.description ||
    role !== group.role || grantsLabel(grants) !== grantsLabel(group.grants);

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.updateGroup(group.id, {
        ...(name.trim() !== group.name ? { name: name.trim() } : {}),
        ...(description.trim() !== group.description ? { description: description.trim() } : {}),
        // Role and grants together: they are one decision about the group.
        role, grants,
      });
      onChanged();
      notify("good", "Saved.");
    } catch (err) {
      setError(err instanceof ApiError ? err.detail : "Couldn't save that.");
    } finally {
      setBusy(false);
    }
  }

  async function remove(m: GroupMember) {
    setBusy(true);
    setError("");
    try {
      await api.removeGroupMember(group.id, m.kind, m.id);
      load();
      onChanged();
      notify("good", `Took ${m.label} out.`);
    } catch (err) {
      setError(err instanceof ApiError ? err.detail : "Couldn't do that.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Sheet open onOpenChange={(open) => { if (!open) onClose(); }}>
      <SheetContent className="max-w-[30rem]">
        <div className="flex items-center gap-3">
          <Avatar name={group.name} kind="group" className="size-10 text-sm" />
          <div className="min-w-0">
            <SheetTitle className="truncate">{group.name}</SheetTitle>
            <SheetDescription>{group.members === 1 ? "1 member" : `${group.members} members`}</SheetDescription>
          </div>
        </div>
        {error && <Notice tone="problem">{error}</Notice>}

        {mayWrite ? (
          <form onSubmit={save} className="space-y-5">
            <div className="grid gap-4 sm:grid-cols-2">
              <div className="space-y-1.5">
                <Label htmlFor="group-edit-name">Name</Label>
                <Input id="group-edit-name" value={name} onChange={(e) => setName(e.target.value)} />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="group-edit-description">Purpose</Label>
                <Input id="group-edit-description" value={description} placeholder="Optional"
                       onChange={(e) => setDescription(e.target.value)} />
              </div>
            </div>
            <RolePicker id={`role-${group.id}`} value={role} onChange={setRole} disabled={busy} allowNone label="Adds the role" />
            <GrantsPicker id={`reach-${group.id}`} value={grants} onChange={setGrants} disabled={busy} subject="everyone in this group" />
            <div className="flex items-center gap-2">
              <Button type="submit" disabled={busy || !changed || !name.trim()}>{busy ? "Saving…" : "Save"}</Button>
              <span className="text-xs text-muted-foreground">A group only adds. Members keep their own.</span>
            </div>
          </form>
        ) : (
          <div className="space-y-4">
            <div className="text-sm"><span className="text-muted-foreground">Adds the role</span> {group.role_name || "none"}</div>
            <GrantsPicker id={`reach-${group.id}`} value={group.grants} subject="everyone in this group" readOnly />
          </div>
        )}

        <div className="space-y-2 border-t pt-5">
          <div className="text-sm font-medium">Members</div>
          {!members ? <Loading rows={2} /> : members.length === 0 ? (
            <p className="text-sm text-muted-foreground">
              Nobody yet. Add people from their row under Users, and keys from theirs.
            </p>
          ) : (
            <ul className="divide-y rounded-lg border">
              {members.map((m) => (
                <li key={`${m.kind}:${m.id}`} className="flex items-center gap-3 px-3 py-2 text-sm">
                  <Avatar name={m.label} kind={m.kind === "key" ? "key" : "person"} className="size-6 text-[10px]" />
                  <span className="min-w-0 flex-1 truncate">{m.label}</span>
                  <Chip>{m.kind}</Chip>
                  {mayWrite && (
                    <Button variant="ghost" size="xs" disabled={busy} onClick={() => remove(m)}>Remove</Button>
                  )}
                </li>
              ))}
            </ul>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}

function AddGroup({ onClose, onAdded }: {
  onClose: () => void;
  onAdded: (name: string) => void;
}) {
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [role, setRole] = useState("");
  const [grants, setGrants] = useState<Grant[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.createGroup({ name: name.trim(), description: description.trim(), role, grants });
      onAdded(name.trim());
    } catch (err) {
      setError(err instanceof ApiError ? err.detail : "Couldn't add that group.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Add a group</DialogTitle>
          <DialogDescription>What it adds goes to everyone you put in it.</DialogDescription>
        </DialogHeader>
        {error && <Notice tone="problem">{error}</Notice>}
        <form onSubmit={submit} className="space-y-5">
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-1.5">
              <Label htmlFor="group-name">Name</Label>
              <Input id="group-name" value={name} autoComplete="off"
                     onChange={(e) => setName(e.target.value)} placeholder="Field engineers" />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="group-description">Purpose</Label>
              <Input id="group-description" value={description}
                     onChange={(e) => setDescription(e.target.value)} placeholder="Optional" />
            </div>
          </div>
          <RolePicker id="group-role" value={role} onChange={setRole} allowNone label="Adds the role" />
          <GrantsPicker id="group-reach" value={grants} onChange={setGrants} subject="everyone in this group" />
          <DialogFooter>
            <Button variant="ghost" type="button" onClick={onClose}>Cancel</Button>
            <Button type="submit" disabled={busy || !name.trim()}>{busy ? "Adding…" : "Add group"}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
