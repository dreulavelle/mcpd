import { useCallback, useState, type FormEvent } from "react";
import { ShieldCheck } from "lucide-react";
import { api, ApiError, type RoleDef } from "@/lib/api";
import { usePoll } from "@/lib/hooks";
import { describe, type PermissionSet } from "@/lib/permissions";
import { useCan } from "@/lib/session";
import { EmptyState, Loading, Notice, PageHeader } from "@/components/chrome";
import { SettingsTabs } from "./SettingsTabs";
import { PermissionMatrix } from "@/components/PermissionMatrix";
import { Chip } from "@/components/status";
import { useNotify, type Notify } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";
import { useConfirm } from "@/components/confirm";

/**
 * What each role means on this host.
 *
 * Three are built in and cannot change, so "what does Operator mean here"
 * has one answer everywhere. Anything else is composed from the same eight
 * rows, usually by copying a built-in and changing one line: a Reader who
 * may also make tunnels, an Operator who may not decide approvals.
 */
export function Roles() {
  const [roles, setRoles] = useState<RoleDef[] | null>(null);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState<PermissionSet | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  const mayWrite = useCan("access:write");
  const notify = useNotify();

  const load = useCallback(() => {
    api.roles()
      .then((r) => { setRoles(r.roles ?? []); setError(""); })
      .catch(() => setError("Couldn't load roles."));
  }, []);
  usePoll(load, 30_000);

  return (
    <>
      <SettingsTabs />
      <PageHeader
        title="Roles"
        lede="A role is what somebody may do here. Which systems they reach is granted separately."
        actions={roles && mayWrite && (
          <Button onClick={() => setAdding({})}>Add role</Button>
        )}
      />

      {error && <Notice tone="problem">{error}</Notice>}

      {adding && (
        <AddRole
          start={adding}
          onClose={() => setAdding(null)}
          onAdded={(name) => { setAdding(null); load(); notify("good", `Added ${name}.`); }}
        />
      )}

      {!roles ? <Loading rows={3} /> : roles.length === 0 ? (
        <EmptyState mark={<ShieldCheck />} title="No roles">
          This host has not written its built-in roles yet. Restart it.
        </EmptyState>
      ) : (
        <Card className="mt-4 overflow-hidden p-0">
          <div className="scroll-x">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Role</TableHead>
                  <TableHead>May</TableHead>
                  <TableHead>Held by</TableHead>
                  <TableHead className="w-px" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {roles.map((r) => (
                  <RoleRow
                    key={r.id} role={r} notify={notify} onChanged={load}
                    mayWrite={mayWrite}
                    open={open === r.id}
                    onToggle={() => setOpen(open === r.id ? null : r.id)}
                    onCopy={() => { setAdding({ ...r.permissions }); window.scrollTo({ top: 0 }); }}
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

function RoleRow({ role, notify, onChanged, mayWrite, open, onToggle, onCopy }: {
  role: RoleDef;
  notify: Notify;
  onChanged: () => void;
  mayWrite: boolean;
  open: boolean;
  onToggle: () => void;
  onCopy: () => void;
}) {
  const confirm = useConfirm();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function remove() {
    if (!(await confirm({
      title: `Delete ${role.name}?`,
      description: "Nothing holds it, so nobody loses anything. It cannot be undone.",
    }))) return;
    setBusy(true);
    setError("");
    try {
      await api.deleteRole(role.id);
      onChanged();
      notify("good", "Role deleted.");
    } catch (e) {
      setError(e instanceof ApiError ? e.detail : "That didn't work.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <TableRow>
        <TableCell>
          <span className="flex flex-wrap items-center gap-2">
            <span className="font-medium">{role.name}</span>
            {role.builtin && <Chip>built in</Chip>}
          </span>
          {role.description && (
            <div className="text-xs text-muted-foreground">{role.description}</div>
          )}
          {error && <div className="mt-1 text-xs text-problem">{error}</div>}
        </TableCell>
        <TableCell className="text-muted-foreground">{describe(role.permissions)}</TableCell>
        <TableCell className="whitespace-nowrap text-muted-foreground">
          {role.assigned === 0 ? "Nobody" : role.assigned === 1 ? "1 subject" : `${role.assigned} subjects`}
        </TableCell>
        <TableCell className="whitespace-nowrap">
          <Button variant="ghost" size="sm" onClick={onToggle}>
            {open ? "Done" : role.builtin || !mayWrite ? "Show" : "Edit"}
          </Button>
          {mayWrite && (
            <Button variant="ghost" size="sm" onClick={onCopy} title="Start a new role from this one">
              Copy
            </Button>
          )}
          {mayWrite && !role.builtin && (
            <Button
              variant="ghost" size="sm" disabled={busy || role.assigned > 0}
              title={role.assigned > 0 ? "Move whoever holds it to another role first" : undefined}
              onClick={remove}
            >
              Delete
            </Button>
          )}
        </TableCell>
      </TableRow>
      {open && (
        <TableRow>
          <TableCell colSpan={4} className="bg-muted/30">
            <RoleDetail role={role} notify={notify} onChanged={onChanged} editable={mayWrite && !role.builtin} />
          </TableCell>
        </TableRow>
      )}
    </>
  );
}

function RoleDetail({ role, notify, onChanged, editable }: {
  role: RoleDef;
  notify: Notify;
  onChanged: () => void;
  editable: boolean;
}) {
  const [name, setName] = useState(role.name);
  const [description, setDescription] = useState(role.description);
  const [perms, setPerms] = useState<PermissionSet>({ ...role.permissions });
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function save() {
    setBusy(true);
    setError("");
    try {
      await api.updateRole(role.id, {
        ...(name.trim() !== role.name ? { name: name.trim() } : {}),
        ...(description.trim() !== role.description ? { description: description.trim() } : {}),
        permissions: perms,
      });
      onChanged();
      notify("good", "Saved.");
    } catch (e) {
      setError(e instanceof ApiError ? e.detail : "Couldn't save that.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="space-y-4 py-2">
      {error && <Notice tone="problem">{error}</Notice>}
      {editable && (
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-1.5">
            <Label htmlFor={`role-name-${role.id}`}>Name</Label>
            <Input id={`role-name-${role.id}`} value={name} onChange={(e) => setName(e.target.value)} />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor={`role-desc-${role.id}`}>What it is for</Label>
            <Input id={`role-desc-${role.id}`} value={description} onChange={(e) => setDescription(e.target.value)} placeholder="Optional" />
          </div>
        </div>
      )}
      <PermissionMatrix id={`perms-${role.id}`} value={perms} onChange={setPerms} disabled={busy} readOnly={!editable} />
      {role.builtin ? (
        <p className="text-xs text-muted-foreground">
          Built in, so it means the same on every host. To change one line of
          it, copy it and change the copy.
        </p>
      ) : editable && (
        <div className="pt-1">
          <Button size="sm" disabled={busy || !name.trim()} onClick={save}>Save</Button>
          {role.assigned > 0 && (
            <span className="ml-3 text-xs text-muted-foreground">
              Takes effect on the next request of everything holding it.
            </span>
          )}
        </div>
      )}
    </div>
  );
}

function AddRole({ start, onClose, onAdded }: {
  start: PermissionSet;
  onClose: () => void;
  onAdded: (name: string) => void;
}) {
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [perms, setPerms] = useState<PermissionSet>(start);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.createRole({ name: name.trim(), description: description.trim(), permissions: perms });
      onAdded(name.trim());
    } catch (err) {
      setError(err instanceof ApiError ? err.detail : "Couldn't add that role.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Add a role</CardTitle>
      </CardHeader>
      <CardContent>
        {error && <Notice tone="problem">{error}</Notice>}
        <form onSubmit={submit} className="space-y-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-1.5">
              <Label htmlFor="role-name">Name</Label>
              <Input id="role-name" value={name} autoComplete="off" onChange={(e) => setName(e.target.value)} placeholder="Tunnel desk" />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="role-description">What it is for</Label>
              <Input id="role-description" value={description} onChange={(e) => setDescription(e.target.value)} placeholder="Optional" />
            </div>
          </div>
          <PermissionMatrix id="role-new" value={perms} onChange={setPerms} disabled={busy} />
          <p className="text-xs text-muted-foreground">
            Reads: {describe(perms)}. Which systems a holder reaches is not
            part of a role; that is granted on the person, key or group.
          </p>
          <div className="flex items-center gap-2">
            <Button type="submit" disabled={busy || !name.trim()}>
              {busy ? "Adding…" : "Add role"}
            </Button>
            <Button variant="ghost" type="button" onClick={onClose}>Cancel</Button>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}
