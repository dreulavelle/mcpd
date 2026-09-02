import { useCallback, useState, type FormEvent } from "react";
import { api, ApiError, type ChatGPTAccount, type ChatGPTAccountBody } from "@/lib/api";
import { useLoader, usePoll } from "@/lib/hooks";
import { useCan } from "@/lib/session";
import { SettingsForm } from "@/components/SettingsForm";
import { Loading, Notice, PageHeader } from "@/components/chrome";
import { SettingsTabs } from "./SettingsTabs";
import { SETTING_LINKS } from "./SettingsSection";
import { EVERYTHING, ReachPicker } from "@/components/ReachPicker";
import { Chip } from "@/components/status";
import { useNotify, type Notify } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { Switch } from "@/components/ui/switch";
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";
import { useConfirm } from "@/components/confirm";

/**
 * The ChatGPT accounts this host connects to.
 *
 * One account is one OpenAI credential, one identity in the history, and one
 * grant. Several exist because several workspaces can share a host, and when
 * they do the questions that matter are per workspace rather than per host:
 * whose key is this connector using, what may that workspace reach, and which
 * of them made the call somebody is now reading about.
 *
 * Tunnels are still made on the Tunnels page. This page decides who they can
 * be made as.
 */
export function ChatGPT() {
  const [rows, setRows] = useState<ChatGPTAccount[] | null>(null);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);
  const notify = useNotify();
  const admin = useCan("admin");

  // The switches that apply to every account: whether ChatGPT may connect at
  // all, and the two diagnostic ones. They were on the general settings page,
  // a tab away from the accounts they govern.
  const loadSettings = useCallback(() => api.settings(), []);
  const { data: settings, reload: reloadSettings } = useLoader(
    loadSettings, "Couldn't load the ChatGPT settings.");

  const load = useCallback(() => {
    api.chatgptAccounts()
      .then((r) => { setRows(r.accounts ?? []); setError(""); })
      .catch((e) => setError(
        e instanceof ApiError ? e.detail : "Couldn't load ChatGPT accounts.",
      ));
  }, []);
  usePoll(load, 60_000);

  return (
    <>
      <SettingsTabs />
      <PageHeader
        title="ChatGPT"
        lede="An account is one OpenAI organisation: its runtime key, the admin key that makes tunnels in it, and the identity its connectors act as in the history. A tunnel belongs to the organisation it was made in and runs under that account only. Add one per organisation that connects here."
        actions={rows && <Button onClick={() => setAdding(true)}>Add account</Button>}
      />

      {error && <Notice tone="problem">{error}</Notice>}

      {settings && (
        <div className="mb-4">
          <SettingsForm
            groups={settings.groups.filter((g) => g.section === "chatgpt")}
            settings={settings} links={SETTING_LINKS}
            onSaved={reloadSettings} readOnly={!admin}
          />
        </div>
      )}

      {adding && (
        <AccountDialog
          onClose={() => setAdding(false)}
          onSaved={() => { setAdding(false); load(); }}
        />
      )}

      {!rows ? <Loading rows={3} /> : rows.length === 0 ? (
        <Notice tone="neutral">
          No accounts yet, so no tunnel can connect. Add one with an OpenAI
          runtime key whose role has Tunnels: Read and Use — that is the key a
          tunnel carries traffic with. An admin key and organization ID are
          optional and separate: they let tunnels be created from the Tunnels
          page rather than pasted in by hand, and they need a role with
          Tunnels: Manage.
        </Notice>
      ) : (
        <Card className="mt-4 overflow-hidden p-0">
          <div className="scroll-x">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Account</TableHead>
                  <TableHead>Identity</TableHead>
                  <TableHead>Admin tools</TableHead>
                  <TableHead>Can reach</TableHead>
                  <TableHead>Rate limit</TableHead>
                  <TableHead className="w-px" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((a) => (
                  <AccountRow key={a.id} account={a} notify={notify} onChanged={load} />
                ))}
              </TableBody>
            </Table>
          </div>
        </Card>
      )}
    </>
  );
}

function AccountRow({ account, notify, onChanged }: {
  account: ChatGPTAccount;
  notify: Notify;
  onChanged: () => void;
}) {
  const confirm = useConfirm();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [editing, setEditing] = useState(false);

  async function remove() {
    if (!(await confirm(
      `Remove ${account.name}? Any tunnel using it stops connecting, and its ` +
      `assignment is cleared. The tunnels themselves stay in OpenAI.`,
    ))) return;
    setBusy(true);
    setError("");
    try {
      await api.removeChatGPTAccount(account.id);
      onChanged();
      notify("good", `${account.name} removed.`);
    } catch (e) {
      setError(e instanceof ApiError ? e.detail : "That didn't work.");
    } finally {
      setBusy(false);
    }
  }

  async function toggle(enabled: boolean) {
    setBusy(true);
    setError("");
    try {
      await api.updateChatGPTAccount(account.id, { enabled });
      onChanged();
      notify("good", enabled
        ? `${account.name} is connecting again.`
        : `${account.name} is switched off; its tunnels have stopped.`);
    } catch (e) {
      setError(e instanceof ApiError ? e.detail : "That didn't work.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <TableRow className={account.enabled ? undefined : "opacity-55"}>
        <TableCell>
          <div className="font-medium">{account.name}</div>
          <div className="text-xs text-muted-foreground">
            {account.has_admin_key
              ? account.organization_id || "admin key set"
              : "no admin key — tunnels are pasted in, not made here"}
          </div>
          {account.problem && (
            <div className="text-xs text-problem">{account.problem}</div>
          )}
          {error && <div className="text-xs text-problem">{error}</div>}
        </TableCell>
        <TableCell className="font-mono text-xs">{account.principal}</TableCell>
        <TableCell>
          {account.role === "admin"
            ? <Chip tone="attention">Allowed</Chip>
            : <span className="text-xs text-muted-foreground">—</span>}
        </TableCell>
        <TableCell className="text-xs">
          {account.plugins.includes("*")
            ? "everything"
            : account.plugins.join(", ") || "nothing"}
        </TableCell>
        <TableCell className="font-mono text-xs tabular-nums">
          {account.rate_per_sec > 0 ? `${account.rate_per_sec}/sec` : "—"}
        </TableCell>
        <TableCell>
          <div className="flex items-center justify-end gap-2">
            <Switch
              checked={account.enabled}
              disabled={busy}
              aria-label={`Let ${account.name} connect`}
              onCheckedChange={(v) => void toggle(v)}
            />
            <Button
              variant="ghost" size="sm" disabled={busy}
              onClick={() => setEditing(true)}
            >
              Edit
            </Button>
            <Button variant="ghost" size="sm" disabled={busy} onClick={() => void remove()}>
              Remove
            </Button>
          </div>
        </TableCell>
      </TableRow>

      {editing && (
        <AccountDialog
          account={account}
          onClose={() => setEditing(false)}
          onSaved={() => { setEditing(false); onChanged(); }}
        />
      )}
    </>
  );
}

/**
 * Add or edit one account.
 *
 * The two are one form because they ask the same questions. The only
 * difference is what an empty key box means: on a new account it is missing,
 * and on an existing one it is "leave the stored key alone" — which is why the
 * body below omits a key rather than sending an empty one.
 */
function AccountDialog({ account, onClose, onSaved }: {
  account?: ChatGPTAccount;
  onClose: () => void;
  onSaved: () => void;
}) {
  const editing = account !== undefined;
  const [name, setName] = useState(account?.name ?? "");
  const [apiKey, setApiKey] = useState("");
  const [adminKey, setAdminKey] = useState("");
  // Clearing the stored admin key is a real thing to want, but it has to be
  // asked for. It used to follow from leaving the box blank on any edit, so
  // changing a rate limit quietly took the Tunnels page's Add form away.
  const [dropAdminKey, setDropAdminKey] = useState(false);
  const [orgID, setOrgID] = useState(account?.organization_id ?? "");
  const [workspaces, setWorkspaces] = useState((account?.workspaces ?? []).join(", "));
  const [role, setRole] = useState<"user" | "admin">(account?.role ?? "user");
  // Held in the shape the API takes, so nothing has to be parsed back out of
  // a sentence on the way to the request.
  const [reach, setReach] = useState<string[]>(account?.plugins ?? [EVERYTHING]);
  const [rate, setRate] = useState(String(account?.rate_per_sec ?? 0));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const notify = useNotify();

  const ready = name.trim() !== "" && (editing || apiKey.trim() !== "");

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");

    const body: ChatGPTAccountBody = {
      name: name.trim(),
      organization_id: orgID.trim(),
      workspaces: workspaces.split(/[\s,]+/).map((w) => w.trim()).filter(Boolean),
      role,
      plugins: reach,
      rate_per_sec: Number(rate) || 0,
    };
    // Omitted rather than sent empty: on an edit, an absent key means "keep
    // the one you have", and sending "" would erase it.
    if (apiKey.trim() !== "") body.api_key = apiKey.trim();
    // The admin key follows the same rule, with one addition: an explicit
    // request to remove it sends an empty key, which is what the server reads
    // as "forget the stored one".
    if (adminKey.trim() !== "") body.admin_key = adminKey.trim();
    else if (editing && dropAdminKey) body.admin_key = "";

    try {
      const saved = editing
        ? await api.updateChatGPTAccount(account.id, body)
        : await api.addChatGPTAccount(body);
      notify("good", editing
        ? `${saved.name} saved. Its tunnels are reconnecting.`
        : `${saved.name} added, connecting as ${saved.principal}.`);
      onSaved();
    } catch (e) {
      setError(e instanceof ApiError ? e.detail : "That didn't work.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open onOpenChange={(o) => { if (!o) onClose(); }}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{editing ? `Edit ${account.name}` : "Add a ChatGPT account"}</DialogTitle>
          <DialogDescription>
            The key is stored encrypted and is never shown again. Everything a
            tunnel on this account does is recorded under its own identity.
            What this account can actually touch is <em>Systems it can
            reach</em>, below — reading from those systems and proposing
            changes to them is what every account may do, and a change is still
            shown in the conversation and needs your yes before it runs.
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={submit} className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="acct-name">Name</Label>
            <Input
              id="acct-name" autoFocus value={name} placeholder="Work"
              onChange={(e) => setName(e.target.value)}
            />
            <p className="text-xs text-muted-foreground">
              {editing
                ? "Renaming does not change the identity in the history."
                : "Letters, digits, spaces and dashes. It becomes the identity in the history."}
            </p>
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="acct-key">OpenAI key</Label>
            <Input
              id="acct-key" type="password" value={apiKey}
              placeholder={editing ? "leave blank to keep the stored key" : "sk-…"}
              onChange={(e) => setApiKey(e.target.value)}
            />
            <p className="text-xs text-muted-foreground">
              Needs Tunnels: Read and Use. Stored encrypted.
            </p>
          </div>

          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-1.5">
              <Label htmlFor="acct-admin">Admin key</Label>
              <Input
                id="acct-admin" type="password" value={adminKey}
                placeholder={
                  editing && account.has_admin_key
                    ? "leave blank to keep the stored key"
                    : "optional"
                }
                disabled={dropAdminKey}
                onChange={(e) => setAdminKey(e.target.value)}
              />
              {editing && account.has_admin_key && (
                <div className="flex items-center gap-2 pt-1">
                  <Switch
                    id="acct-admin-drop"
                    checked={dropAdminKey}
                    onCheckedChange={setDropAdminKey}
                  />
                  <Label htmlFor="acct-admin-drop" className="font-normal">
                    Remove the stored admin key
                  </Label>
                </div>
              )}
              <p className="text-xs text-muted-foreground">
                Optional. Lets tunnels be created from the Tunnels page instead
                of pasted in by hand. Made under Settings → Organization →
                Admin keys, and only works if whoever created it has a role
                including <span className="font-medium">Tunnels: Manage</span>.
              </p>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="acct-org">Organization ID</Label>
              <Input
                id="acct-org" value={orgID} placeholder="org_…"
                onChange={(e) => setOrgID(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">
                Required with an admin key — it cannot list anything alone.
                Settings → Organization → General, starting{" "}
                <span className="font-medium">org_</span>.
              </p>
            </div>
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="acct-ws">Workspaces</Label>
            <Input
              id="acct-ws" value={workspaces} placeholder="ws_…, ws_…"
              onChange={(e) => setWorkspaces(e.target.value)}
            />
            <p className="text-xs text-muted-foreground">
              The ChatGPT workspaces this account's connectors sit in, by id,
              separated by commas. A tunnel made under this account is listed
              in one of them; a tunnel listed only to the organisation is
              invisible in an Enterprise or Edu workspace. Ones already seen
              on the account's tunnels are added on their own.
            </p>
          </div>

          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-1.5">
              <Label htmlFor="acct-role">Administrative tools</Label>
              <NativeSelect
                id="acct-role" value={role}
                onChange={(e) => setRole(e.target.value as "user" | "admin")}
              >
                <option value="user">Not allowed</option>
                <option value="admin">Allowed</option>
              </NativeSelect>
              {/* Says what the setting does rather than what its value is
                  called. The old wording claimed admin let ChatGPT change this
                  host's settings, which it cannot: those live behind the
                  dashboard's HTTP API, and a tunnel only ever reaches an
                  in-process MCP server carrying plugin tools. */}
              <p className="text-xs text-muted-foreground">
                Whether this account may call a tool a plugin marks
                administrative. Nothing built in has one, so leave it off
                unless a plugin's own documentation says otherwise.
              </p>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="acct-rate">Rate limit</Label>
              <Input
                id="acct-rate" type="number" min="0" step="0.5" value={rate}
                onChange={(e) => setRate(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">
                Calls per second, shared by all this account's tunnels. 0 is no
                limit, which is the usual answer.
              </p>
            </div>
          </div>

          {/* The same picker groups, keys and accounts use. A comma-separated
              text box asks somebody to know a plugin's exact name and spell
              it, and gets its punishment wrong: a name matching nothing is not
              an error, it is a grant to a system that does not exist. */}
          <ReachPicker
            id="acct-reach" value={reach} onChange={setReach}
            subject="this account"
          />

          {error && <Notice tone="problem">{error}</Notice>}

          <DialogFooter className="sm:justify-start">
            <Button type="submit" disabled={busy || !ready}>
              {busy ? "Saving…" : editing ? "Save" : "Add"}
            </Button>
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
