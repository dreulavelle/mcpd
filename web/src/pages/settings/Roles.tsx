import { useCallback, useEffect, useState, type FormEvent } from "react";
import { Lock, Plus } from "lucide-react";
import { api, ApiError, type RoleDef } from "@/lib/api";
import { usePoll } from "@/lib/hooks";
import { BUILTIN_ROLES, describe, type PermissionSet } from "@/lib/permissions";
import { useCan } from "@/lib/session";
import { Loading, Notice, PageHeader } from "@/components/chrome";
import { PermissionMatrix } from "@/components/PermissionMatrix";
import { Chip } from "@/components/status";
import { useNotify } from "@/components/toast";
import { Button } from "@/components/ui/button";
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { useConfirm } from "@/components/confirm";
import { cn } from "@/lib/utils";

/**
 * What each role means on this host.
 *
 * A list on the left and one role open on the right, because a role is
 * eight rows and the question is always "what does this one allow", not
 * "show me every role's every row". Three are built in and cannot change,
 * so "what does Operator mean here" has one answer everywhere; anything
 * else starts as a copy of one and changes a line.
 */
export function Roles() {
  const [roles, setRoles] = useState<RoleDef[] | null>(null);
  const [error, setError] = useState("");
  const [chosen, setChosen] = useState<string>("role_operator");
  const [adding, setAdding] = useState(false);
  const mayWrite = useCan("access:write");
  const notify = useNotify();

  const load = useCallback(() => {
    api.roles()
      .then((r) => { setRoles(r.roles ?? []); setError(""); })
      .catch(() => setError("Couldn't load roles."));
  }, []);
  usePoll(load, 30_000);

  // A deleted role must not stay open.
  useEffect(() => {
    if (roles && !roles.some((r) => r.id === chosen)) setChosen(roles[0]?.id ?? "");
  }, [roles, chosen]);

  const current = roles?.find((r) => r.id === chosen) ?? null;

  return (
    <>
      <PageHeader
        title="Roles"
        lede="What someone may do here. Which systems they reach is granted separately."
        actions={roles && mayWrite ? (
          <Button onClick={() => setAdding(true)}><Plus /> New role</Button>
        ) : undefined}
      />

      {error && <Notice tone="problem">{error}</Notice>}

      {adding && roles && (
        <AddRole
          roles={roles}
          onClose={() => setAdding(false)}
          onAdded={(id, name) => { setAdding(false); setChosen(id); load(); notify("good", `Added ${name}.`); }}
        />
      )}

      {!roles ? <Loading rows={4} /> : (
        <div className="grid gap-6 lg:grid-cols-[16rem_1fr]">
          <ul className="space-y-1" aria-label="Roles">
            {roles.map((r) => {
              const on = r.id === chosen;
              return (
                <li key={r.id}>
                  <button
                    type="button"
                    aria-current={on ? "true" : undefined}
                    onClick={() => setChosen(r.id)}
                    className={cn(
                      "w-full rounded-lg border px-3 py-2.5 text-left transition-colors",
                      on ? "border-primary/40 bg-primary/5" : "border-transparent hover:bg-accent/60",
                    )}
                  >
                    <div className="flex items-center justify-between gap-2">
                      <span className="truncate text-sm font-medium">{r.name}</span>
                      {r.builtin && <Lock className="size-3.5 shrink-0 text-muted-foreground" aria-label="Built in" />}
                    </div>
                    <div className="mt-0.5 truncate text-xs text-muted-foreground">
                      {r.assigned === 0 ? "Held by nobody" : r.assigned === 1 ? "Held by 1" : `Held by ${r.assigned}`}
                    </div>
                  </button>
                </li>
              );
            })}
          </ul>

          {current ? (
            <RoleDetail key={current.id} role={current} mayWrite={mayWrite} onChanged={load} />
          ) : (
            <Notice tone="neutral">No role chosen.</Notice>
          )}
        </div>
      )}
    </>
  );
}

function RoleDetail({ role, mayWrite, onChanged }: {
  role: RoleDef;
  mayWrite: boolean;
  onChanged: () => void;
}) {
  const confirm = useConfirm();
  const notify = useNotify();
  const editable = mayWrite && !role.builtin;
  const [name, setName] = useState(role.name);
  const [description, setDescription] = useState(role.description);
  const [perms, setPerms] = useState<PermissionSet>({ ...role.permissions });
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const changed = name.trim() !== role.name || description.trim() !== role.description ||
    JSON.stringify(sorted(perms)) !== JSON.stringify(sorted(role.permissions));

  async function save(e: FormEvent) {
    e.preventDefault();
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
    } catch (err) {
      setError(err instanceof ApiError ? err.detail : "Couldn't save that.");
    } finally {
      setBusy(false);
    }
  }

  async function remove() {
    if (!(await confirm({ title: `Delete ${role.name}?`, description: "Nothing holds it, so nobody loses anything." }))) return;
    setBusy(true);
    setError("");
    try {
      await api.deleteRole(role.id);
      onChanged();
      notify("good", "Role deleted.");
    } catch (err) {
      setError(err instanceof ApiError ? err.detail : "That didn't work.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={save} className="min-w-0 space-y-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <h2 className="text-lg font-semibold tracking-tight">{role.name}</h2>
            {role.builtin && <Chip>Built in</Chip>}
          </div>
          <p className="mt-0.5 text-sm text-muted-foreground">{role.description || describe(role.permissions)}</p>
        </div>
        {editable && (
          <div className="flex items-center gap-2">
            <Button
              type="button" variant="ghost" size="sm" disabled={busy || role.assigned > 0}
              title={role.assigned > 0 ? "Move whoever holds it to another role first" : undefined}
              onClick={remove}
            >
              Delete
            </Button>
            <Button type="submit" size="sm" disabled={busy || !changed || !name.trim()}>
              {busy ? "Saving…" : "Save"}
            </Button>
          </div>
        )}
      </div>

      {error && <Notice tone="problem">{error}</Notice>}

      {editable && (
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-1.5">
            <Label htmlFor={`role-name-${role.id}`}>Name</Label>
            <Input id={`role-name-${role.id}`} value={name} onChange={(e) => setName(e.target.value)} />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor={`role-desc-${role.id}`}>Purpose</Label>
            <Input id={`role-desc-${role.id}`} value={description} placeholder="Optional"
                   onChange={(e) => setDescription(e.target.value)} />
          </div>
        </div>
      )}

      <PermissionMatrix id={`perms-${role.id}`} value={perms} onChange={setPerms} disabled={busy} readOnly={!editable} />

      <p className="text-xs text-muted-foreground">
        {role.builtin
          ? "Built in, so it means the same on every host. To change a line of it, make a new role from it."
          : editable && role.assigned > 0
            ? `Held by ${role.assigned}. A change applies on their next request.`
            : null}
      </p>
    </form>
  );
}

function sorted(p: PermissionSet): [string, string][] {
  return Object.entries(p).filter(([, v]) => v && v !== "none").sort(([a], [b]) => a.localeCompare(b)) as [string, string][];
}

function AddRole({ roles, onClose, onAdded }: {
  roles: RoleDef[];
  onClose: () => void;
  onAdded: (id: string, name: string) => void;
}) {
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [from, setFrom] = useState("role_operator");
  const [perms, setPerms] = useState<PermissionSet>({ ...(BUILTIN_ROLES.role_operator?.permissions ?? {}) });
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  function startFrom(id: string) {
    setFrom(id);
    setPerms(id === "" ? {} : { ...(roles.find((r) => r.id === id)?.permissions ?? {}) });
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const made = await api.createRole({ name: name.trim(), description: description.trim(), permissions: perms });
      onAdded(made.id, made.name);
    } catch (err) {
      setError(err instanceof ApiError ? err.detail : "Couldn't add that role.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>New role</DialogTitle>
          <DialogDescription>Start from one that exists and change what differs.</DialogDescription>
        </DialogHeader>
        {error && <Notice tone="problem">{error}</Notice>}
        <form onSubmit={submit} className="space-y-5">
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-1.5">
              <Label htmlFor="role-name">Name</Label>
              <Input id="role-name" value={name} autoComplete="off" onChange={(e) => setName(e.target.value)} placeholder="Tunnel desk" />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="role-from">Start from</Label>
              <NativeSelect id="role-from" value={from} onChange={(e) => startFrom(e.target.value)}>
                {roles.map((r) => <option key={r.id} value={r.id}>{r.name}</option>)}
                <option value="">Nothing</option>
              </NativeSelect>
            </div>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="role-description">Purpose</Label>
            <Input id="role-description" value={description} onChange={(e) => setDescription(e.target.value)} placeholder="Optional" />
          </div>
          <PermissionMatrix id="role-new" value={perms} onChange={setPerms} disabled={busy} />
          <p className="text-xs text-muted-foreground">Reads as: {describe(perms).toLowerCase()}.</p>
          <DialogFooter>
            <Button variant="ghost" type="button" onClick={onClose}>Cancel</Button>
            <Button type="submit" disabled={busy || !name.trim()}>{busy ? "Adding…" : "Add role"}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
