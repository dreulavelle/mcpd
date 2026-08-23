import { useCallback, useState } from "react";
import { api, ApiError, type Meta, type Session, type User } from "@/lib/api";
import { capabilitiesOf, type Capability } from "@/lib/capabilities";
import { whenExact } from "@/lib/format";
import { useLoader } from "@/lib/hooks";
import { signedInAs, useAdoptSession, useCan, useSession } from "@/lib/session";
import { Detail, Notice, PageHeader, Section } from "@/components/chrome";
import { Chip } from "@/components/status";
import { useNotify } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

const CAPABILITY_MEANING: Record<Capability, string> = {
  read: "See plugins, settings, proposed changes and the audit trail.",
  propose: "Suggest a change, and withdraw one you suggested.",
  approve: "Decide on a proposed change — approve it or turn it down.",
  admin: "Change settings, manage accounts and tunnels, and clear the history.",
};

/**
 * You.
 *
 * Reached by clicking your own name in the sidebar rather than from a nav
 * entry, and living at /profile rather than under /settings, because Settings
 * is how the *host* is configured and this is how *you* are. The two were one
 * section and the line between them kept getting crossed.
 *
 * It answers "what am I allowed to do here", which was previously answerable
 * only by hunting for a control that was missing, and it is where the things
 * about your own account are changed. Naming yourself is one of them and needs
 * no capability beyond being signed in: `PATCH /api/account` carries no
 * identifier, so it can only ever edit the account the request authenticated
 * as. Changing a password still has no self-service endpoint, so that form is
 * offered to an administrator and explained to everybody else.
 */
export function Profile() {
  const session = useSession();
  const mayEditAccounts = useCan("admin");

  const loadMeta = useCallback(() => api.meta(), []);
  const { data: meta } = useLoader<Meta>(loadMeta, "Couldn't read the host's details.");

  // The account list is the only way to learn your own id, and it takes admin.
  // Asking for it without the capability would be one guaranteed 403 per page
  // load and an error notice about something nobody tried to do.
  const loadSelf = useCallback(
    () => (mayEditAccounts ? api.users() : Promise.resolve({ users: [], count: 0 })),
    [mayEditAccounts],
  );
  const { data: users, error: usersError } = useLoader(loadSelf, "Couldn't load your account.");
  const self: User | undefined = (users?.users ?? []).find((u) => u.self);

  if (!session) return null;
  const held = capabilitiesOf(session.role);

  return (
    <>
      <PageHeader
        title={signedInAs(session) || "Your profile"}
        lede="The account you are signed in as, what it lets you do, and the parts of it you can change."
      />

      <div className="space-y-6">
        <Card>
          <CardContent>
            <dl className="grid gap-4 sm:grid-cols-2">
              <Detail label="Email">{session.email}</Detail>
              {/* The one place a role is read rather than a capability, and it
                  is reading it in order to print it. Nothing branches here. */}
              <Detail label="Role">
                {session.role === "admin" ? "Administrator" : "User"}
              </Detail>
              <Detail label="This session expires">
                {whenExact(session.expires_at)}
              </Detail>
              {meta && <Detail label="Host">mcpd {meta.version}</Detail>}
              <Detail label="Can reach" className="sm:col-span-2">
                {session.plugins.includes("*")
                  ? "Every system on this host"
                  : session.plugins.join(", ") || "Nothing"}
              </Detail>
            </dl>
          </CardContent>
        </Card>

        {usersError && mayEditAccounts && (
          <Notice tone="problem">{usersError}</Notice>
        )}

        <DisplayName session={session} />

        <ChangePassword email={session.email} self={self} mayEdit={mayEditAccounts} />

        <Section
          title="What you may do"
          description="mcpd checks capabilities rather than roles. These are the ones your role carries."
        >
          <Card>
            <CardContent>
              <dl className="space-y-3">
                {(["read", "propose", "approve", "admin"] as Capability[]).map((c) => {
                  const has = held.includes(c);
                  return (
                    <div key={c} className="flex items-start gap-3">
                      <Chip tone={has ? "good" : "neutral"} className="mt-0.5 w-20 justify-center">
                        {c}
                      </Chip>
                      <p className={has ? "text-sm" : "text-sm text-muted-foreground line-through"}>
                        {CAPABILITY_MEANING[c]}
                      </p>
                    </div>
                  );
                })}
              </dl>
            </CardContent>
          </Card>
        </Section>
      </div>
    </>
  );
}

/**
 * The name the console calls you by.
 *
 * Self-service, and no capability beyond being signed in. `PATCH /api/account`
 * carries no identifier, so it can only ever edit the account the request
 * authenticated as -- which is what lets it take `read` where naming somebody
 * else still takes `admin`. Before it existed this section could only tell a
 * non-administrator to go and ask one to type their name for them.
 *
 * The field holds `display_name`, the stored value, and the heading renders
 * `name`, the resolved one. That is not pedantry: `name` falls back to the
 * address, so seeding the box with it would offer to save the fallback as a
 * name, and the next reader could not tell a person called by their address
 * from one who typed their address in.
 *
 * Emptying it is a real edit and is allowed. It clears the name, and the
 * console goes back to the address.
 */
function DisplayName({ session }: { session: Session }) {
  const notify = useNotify();
  const adopt = useAdoptSession();
  const [name, setName] = useState(session.display_name);
  const [problem, setProblem] = useState("");
  const [busy, setBusy] = useState(false);

  const next = name.trim();
  const unchanged = next === session.display_name.trim();

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setProblem("");
    try {
      const saved = await api.updateAccount(next);
      // The sidebar names you from the session, so the session is the thing
      // that has to change. Pushing the server's own answer back rather than
      // what was typed keeps the console showing what is stored -- including
      // the address, when the name was cleared.
      adopt({ ...session, name: saved.name, display_name: saved.display_name });
      setName(saved.display_name);
      notify("good", next ? "Saved." : "Cleared. The console will use your address.");
    } catch (err) {
      // The server's sentence, not a paraphrase. It is the only thing that
      // knows whether this was too long, carried a character that breaks a log
      // line in two, or collided with somebody else's address.
      setProblem(err instanceof ApiError ? err.detail : "Couldn't change it.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Section
      title="Name"
      description="What the console calls you. Your email is what identifies the account, and that is what every audit record is keyed on."
    >
      <Card>
        <CardContent>
          {problem && <Notice tone="problem">{problem}</Notice>}
          <form className="max-w-sm space-y-4" onSubmit={submit}>
            <div className="space-y-1.5">
              <Label htmlFor="profile-name">Display name</Label>
              <Input
                id="profile-name" value={name}
                placeholder={session.email}
                autoComplete="name"
                aria-describedby="profile-name-help"
                aria-invalid={problem ? true : undefined}
                onChange={(e) => {
                  setName(e.target.value);
                  setProblem("");
                }}
              />
              <p id="profile-name-help" className="text-xs text-muted-foreground">
                {session.display_name.trim()
                  ? "Leave it empty to go back to being called by your address."
                  : "Not set, so the console calls you by your address."}
              </p>
            </div>
            <Button type="submit" disabled={busy || unchanged}>
              {busy ? "Saving…" : "Save"}
            </Button>
          </form>
        </CardContent>
      </Card>
    </Section>
  );
}

/**
 * Changing your own password.
 *
 * The only endpoint that can do it is `PATCH /api/users/{id}`, which takes the
 * admin capability -- so this is offered to an administrator and not to anyone
 * else. A user who needs their password changed asks one; there is no
 * self-service endpoint to build a form against, and inventing one here would
 * mean a form that always fails.
 */
function ChangePassword({ email, self, mayEdit }: {
  email: string;
  self: User | undefined;
  mayEdit: boolean;
}) {
  const notify = useNotify();
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [problem, setProblem] = useState("");
  const [busy, setBusy] = useState(false);

  if (!mayEdit) {
    return (
      <Section title="Password">
        <p className="text-sm text-muted-foreground">
          Ask an administrator to change the password for {email}. mcpd has no
          self-service password endpoint.
        </p>
      </Section>
    );
  }

  const mismatch = confirm !== "" && password !== confirm;
  const ready = password.length >= 12 && !mismatch && confirm !== "";

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!self) return;
    setBusy(true);
    setProblem("");
    try {
      await api.updateUser(self.id, { password });
      notify("good", "Password changed.");
      setPassword("");
      setConfirm("");
    } catch (err) {
      setProblem(err instanceof ApiError ? err.detail : "Couldn't change it.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Section title="Password">
      <Card>
        <CardContent>
          {problem && <Notice tone="problem">{problem}</Notice>}
          <form className="max-w-sm space-y-4" onSubmit={submit}>
            <div className="space-y-1.5">
              <Label htmlFor="acc-password">New password</Label>
              <Input
                id="acc-password" type="password" autoComplete="new-password"
                value={password} placeholder="At least 12 characters"
                onChange={(e) => setPassword(e.target.value)}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="acc-confirm">Confirm</Label>
              <Input
                id="acc-confirm" type="password" autoComplete="new-password"
                value={confirm} placeholder="Type it again"
                onChange={(e) => setConfirm(e.target.value)}
              />
              {mismatch && (
                <p className="text-xs text-problem">These do not match.</p>
              )}
            </div>
            <Button type="submit" disabled={busy || !ready || !self}>
              {busy ? "Changing…" : "Change password"}
            </Button>
          </form>
        </CardContent>
      </Card>
    </Section>
  );
}
