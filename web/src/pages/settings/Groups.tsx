import { useCallback, useState, type FormEvent } from "react";
import { api, ApiError, type Group, type GroupMember } from "@/lib/api";
import { usePoll } from "@/lib/hooks";
import { Loading, Notice, PageHeader } from "@/components/chrome";
import { ReachPicker } from "@/components/ReachPicker";
import { Chip } from "@/components/status";
import { useNotify, type Notify } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";

/** Renders a grant the way every other list of systems here is rendered. */
export function reachLabel(plugins: string[]): string {
  if (plugins.includes("*")) return "Everything";
  return plugins.join(", ") || "Nothing";
}

/** A group hands its systems to everyone in it. */
export function Groups() {
  const [groups, setGroups] = useState<Group[] | null>(null);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);
  const [open, setOpen] = useState<string | null>(null);
  const notify = useNotify();

  const load = useCallback(() => {
    api.groups()
      .then((r) => { setGroups(r.groups ?? []); setError(""); })
      .catch(() => setError("Couldn't load groups."));
  }, []);
  usePoll(load, 30_000);

  return (
    <>
      <PageHeader
        title="Groups"
        lede="Everyone in a group can reach the systems it lists."
        actions={groups && <Button onClick={() => setAdding(true)}>Add group</Button>}
      />

      {error && <Notice tone="problem">{error}</Notice>}

      {adding && (
        <AddGroup
          onClose={() => setAdding(false)}
          onAdded={(name) => { setAdding(false); load(); notify("good", `Added ${name}.`); }}
        />
      )}

      {!groups ? <Loading rows={3} /> : groups.length === 0 ? (
        <Notice tone="neutral">
          No groups yet. Add one, list the systems it should reach, then put
          people and keys in it.
        </Notice>
      ) : (
        <Card className="mt-4 overflow-hidden p-0">
          <div className="scroll-x">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Can reach</TableHead>
                  <TableHead>In it</TableHead>
                  <TableHead className="w-px" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {groups.map((g) => (
                  <GroupRow
                    key={g.id} group={g} notify={notify} onChanged={load}
                    open={open === g.id}
                    onToggle={() => setOpen(open === g.id ? null : g.id)}
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

function GroupRow({ group, notify, onChanged, open, onToggle }: {
  group: Group;
  notify: Notify;
  onChanged: () => void;
  open: boolean;
  onToggle: () => void;
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
      setError(e instanceof ApiError ? e.detail : "That didn't work.");
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <TableRow>
        <TableCell>
          <span className="font-medium">{group.name}</span>
          {group.description && (
            <div className="text-xs text-muted-foreground">{group.description}</div>
          )}
          {error && <div className="mt-1 text-xs text-problem">{error}</div>}
        </TableCell>
        <TableCell className="text-muted-foreground">
          {group.plugins.length === 0
            ? <Chip>Nothing</Chip>
            : reachLabel(group.plugins)}
        </TableCell>
        <TableCell className="whitespace-nowrap text-muted-foreground">
          {group.members === 1 ? "1 member" : `${group.members} members`}
        </TableCell>
        <TableCell className="whitespace-nowrap">
          <Button variant="ghost" size="sm" onClick={onToggle}>
            {open ? "Done" : "Edit"}
          </Button>
          <Button
            variant="ghost" size="sm" disabled={busy}
            onClick={() => {
              const who = group.members === 1 ? "1 member" : `${group.members} members`;
              if (!confirm(
                `Delete ${group.name}? ${who} will stop reaching what it lists. ` +
                `Nothing else about them changes.`,
              )) return;
              run("Group deleted.", () => api.deleteGroup(group.id));
            }}
          >
            Delete
          </Button>
        </TableCell>
      </TableRow>
      {open && (
        <TableRow>
          <TableCell colSpan={4} className="bg-muted/30">
            <GroupDetail group={group} notify={notify} onChanged={onChanged} />
          </TableCell>
        </TableRow>
      )}
    </>
  );
}

function GroupDetail({ group, notify, onChanged }: {
  group: Group;
  notify: Notify;
  onChanged: () => void;
}) {
  const [members, setMembers] = useState<GroupMember[] | null>(null);
  const [reach, setReach] = useState<string[]>(group.plugins);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(() => {
    api.group(group.id)
      .then((r) => setMembers(r.members ?? []))
      .catch(() => setError("Couldn't load who is in this group."));
  }, [group.id]);
  usePoll(load, 30_000);

  async function saveReach() {
    setBusy(true);
    setError("");
    try {
      await api.updateGroup(group.id, { plugins: reach });
      onChanged();
      notify("good", "Saved.");
    } catch (e) {
      setError(e instanceof ApiError ? e.detail : "Couldn't save that.");
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
    } catch (e) {
      setError(e instanceof ApiError ? e.detail : "Couldn't do that.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="space-y-4 py-2">
      {error && <Notice tone="problem">{error}</Notice>}

      <div className="space-y-1.5">
        <ReachPicker
          id={`reach-${group.id}`} value={reach} onChange={setReach}
          subject="everyone in this group"
        />
        <div className="pt-1">
          <Button size="sm" disabled={busy} onClick={saveReach}>Save</Button>
        </div>
      </div>

      <div className="space-y-1.5">
        <div className="text-sm font-medium">In this group</div>
        {!members ? <Loading rows={2} /> : members.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            Nobody yet. Add people on Users and keys on Keys.
          </p>
        ) : (
          <ul className="space-y-1">
            {members.map((m) => (
              <li key={`${m.kind}:${m.id}`} className="flex items-center gap-2 text-sm">
                <Chip tone={m.kind === "key" ? "info" : "neutral"}>
                  {m.kind === "key" ? "key" : "person"}
                </Chip>
                <span className="min-w-0 flex-1 truncate">{m.label}</span>
                <Button variant="ghost" size="sm" disabled={busy} onClick={() => remove(m)}>
                  Remove
                </Button>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

function AddGroup({ onClose, onAdded }: {
  onClose: () => void;
  onAdded: (name: string) => void;
}) {
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [reach, setReach] = useState<string[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.createGroup({
        name: name.trim(),
        description: description.trim(),
        plugins: reach,
      });
      onAdded(name.trim());
    } catch (err) {
      setError(err instanceof ApiError ? err.detail : "Couldn't add that group.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Add a group</CardTitle>
      </CardHeader>
      <CardContent>
        {error && <Notice tone="problem">{error}</Notice>}
        <form onSubmit={submit} className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="group-name">Name</Label>
            <Input
              id="group-name" value={name} autoComplete="off"
              onChange={(e) => setName(e.target.value)} placeholder="Field engineers"
            />
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="group-description">What it is for</Label>
            <Input
              id="group-description" value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="Optional"
            />
          </div>

          <ReachPicker
            id="group-reach" value={reach} onChange={setReach}
            subject="everyone in this group"
          />

          <div className="flex items-center gap-2">
            <Button type="submit" disabled={busy || !name.trim()}>
              {busy ? "Adding…" : "Add group"}
            </Button>
            <Button variant="ghost" type="button" onClick={onClose}>Cancel</Button>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}
