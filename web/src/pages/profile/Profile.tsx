import { useCallback, useState } from "react";
import { api, ApiError, type Meta, type User } from "@/lib/api";
import { capabilitiesOf, type Capability } from "@/lib/capabilities";
import { whenExact } from "@/lib/format";
import { useLoader } from "@/lib/hooks";
import { signedInAs, useCan, useSession } from "@/lib/session";
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
 * about your own account are changed -- as far as the API allows, which today
 * is not far. `PATCH /api/users/{id}` takes the admin capability, so an
 * administrator can edit their own account here and everybody else is told to
 * ask one. That is honest; a form that always answered 403 would not be.
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
  const { data: users, error: usersError, reload } = useLoader(loadSelf, "Couldn't load your account.");
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

        <DisplayName
          current={session.display_name}
          self={self}
          mayEdit={mayEditAccounts}
          onSaved={reload}
        />

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
 * `display_name` already travels end to end -- the session carries it and
 * `signedInAs` already prefers it over the address. What is missing is a way
 * to set your own, so this writes through the endpoint that exists:
 * `PATCH /api/users/{id}`, which is administrator-only. Until a self-service
 * endpoint lands, somebody without admin is told so rather than shown a field
 * that saves nothing.
 */
function DisplayName({ current, self, mayEdit, onSaved }: {
  current: string;
  self: User | undefined;
  mayEdit: boolean;
  onSaved: () => void;
}) {
  const notify = useNotify();
  const [name, setName] = useState(current);
  const [problem, setProblem] = useState("");
  const [busy, setBusy] = useState(false);

  if (!mayEdit) {
    return (
      <Section title="Name">
        <p className="text-sm text-muted-foreground">
          {current
            ? <>The console calls you <strong>{current}</strong>.</>
            : "You have no display name, so the console uses your email address."}{" "}
          Changing it needs the account endpoint, which takes the admin
          capability today — ask an administrator until mcpd grows a
          self-service one.
        </p>
      </Section>
    );
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!self) return;
    setBusy(true);
    setProblem("");
    try {
      await api.updateUser(self.id, { display_name: name.trim() });
      // The session's copy of the name is stale until it is fetched again, and
      // the sidebar reads it from there. Saying so beats a page that looks
      // like it ignored the change.
      notify("good", "Saved. Reload to see it in the sidebar.");
      onSaved();
    } catch (err) {
      setProblem(err instanceof ApiError ? err.detail : "Couldn't change it.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Section
      title="Name"
      description="What the console calls you. Your email is what identifies the account."
    >
      <Card>
        <CardContent>
          {problem && <Notice tone="problem">{problem}</Notice>}
          <form className="max-w-sm space-y-4" onSubmit={submit}>
            <div className="space-y-1.5">
              <Label htmlFor="profile-name">Display name</Label>
              <Input
                id="profile-name" value={name} placeholder="Not set"
                autoComplete="name"
                onChange={(e) => setName(e.target.value)}
              />
            </div>
            <Button type="submit" disabled={busy || !self || name.trim() === current.trim()}>
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
