import { useCallback, useState } from "react";
import {
  api,
  type Meta,
  type ProviderName,
  type Session,
  type User,
  problemText,
} from "@/lib/api";
import { collect, describe } from "@/lib/permissions";
import { PermissionMatrix } from "@/components/PermissionMatrix";
import { whenExact } from "@/lib/format";
import { useLoader } from "@/lib/hooks";
import { signedInAs, useAdoptSession, useCan, useSession } from "@/lib/session";
import { consumeSSOOutcome } from "@/lib/sso";
import { Detail, Notice, PageHeader, Section } from "@/components/chrome";
import { Chip } from "@/components/status";
import { ThemePicker } from "@/components/ThemeToggle";
import { useNotify } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { useConfirm } from "@/components/confirm";
import { Label } from "@/components/ui/label";


/**
 * Your own account: what you may do here, and the things about it you can
 * change. Settings is how the *host* is configured; this is how *you* are.
 */
export function Profile() {
  const session = useSession();
  // Two questions. Learning your own id is a read of the accounts list;
  // changing your password goes through the same write as editing anybody's.
  const mayReadAccounts = useCan("access:read");
  const mayEditAccounts = useCan("access:write");

  const loadMeta = useCallback(() => api.meta(), []);
  const { data: meta } = useLoader<Meta>(loadMeta, "Couldn't read the host's details.");

  // The only way to learn your own id, and it takes admin -- so it is not
  // asked for without one.
  const loadSelf = useCallback(
    () => (mayReadAccounts ? api.users() : Promise.resolve({ users: [], count: 0 })),
    [mayReadAccounts],
  );
  const { data: users, error: usersError } = useLoader(loadSelf, "Couldn't load your account.");
  const self: User | undefined = (users?.users ?? []).find((u) => u.self);

  if (!session) return null;
  const held = collect(session.permissions);

  return (
    <>
      <PageHeader
        title={signedInAs(session) || "Your profile"}
        lede="Your account, what it lets you do, and the parts you can change."
      />

      <div className="space-y-6">
        <Card>
          <CardContent>
            <dl className="grid gap-4 sm:grid-cols-2">
              <Detail label="Email">{session.email}</Detail>
              {/* The one place a role is read rather than a capability, and it
                  is reading it in order to print it. Nothing branches here. */}
              <Detail label="Role">{session.role_name || session.role}</Detail>
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

        {usersError && mayReadAccounts && (
          <Notice tone="problem">{usersError}</Notice>
        )}

        <DisplayName session={session} />

        <Section
          title="Appearance"
          description="Saved in this browser."
        >
          <Card>
            <CardContent>
              <ThemePicker />
            </CardContent>
          </Card>
        </Section>

        <LinkedProviders />

        <ChangePassword email={session.email} self={self} mayEdit={mayEditAccounts} />

        <Section
          title="What you may do"
          description={`What you can do here: ${describe(held).toLowerCase()}. This is your role plus the role of every group you are in.`}
        >
          <Card>
            <CardContent>
              <PermissionMatrix id="mine" value={held} readOnly />
            </CardContent>
          </Card>
        </Section>
      </div>
    </>
  );
}

/**
 * The providers this account can sign in with, and the ones it could add.
 *
 * Linking is done from here, by the account itself, while signed in — and that
 * is the only way an account that already exists gains a provider. mcpd will
 * not adopt an account because a Google sign-in carries a matching email
 * address: controlling that address at Google says nothing about who owns the
 * account here, and treating it as proof is how somebody who registers a
 * lapsed domain walks in. Signing in here first and then completing the flow
 * says exactly the thing that needs saying.
 */
function LinkedProviders() {
  const confirm = useConfirm();
  const notify = useNotify();
  const load = useCallback(() => api.identities(), []);
  const { data, error, reload } = useLoader(load, "Couldn't read your linked providers.");
  const [busy, setBusy] = useState("");
  // Taken once, on mount: a link that came back refused landed on this page,
  // and the message should not follow the person around the console.
  const [problem, setProblem] = useState(consumeSSOOutcome);

  // Nothing configured and nothing linked: no card, rather than one explaining
  // a feature this host does not offer.
  if (!data || (data.identities.length === 0 && data.available.length === 0)) {
    return error ? <Notice tone="problem">{error}</Notice> : null;
  }

  const linked = new Set(data.identities.map((i) => i.provider));
  const addable = data.available.filter((p) => !linked.has(p.provider));

  async function link(provider: ProviderName) {
    setBusy(provider);
    setProblem("");
    try {
      const { authorization_url } = await api.linkIdentity(provider);
      window.location.assign(authorization_url);
    } catch (err) {
      setBusy("");
      setProblem(problemText(err, "Couldn't start that."));
    }
  }

  async function unlink(provider: ProviderName, label: string) {
    if (!(await confirm({
      title: `Stop signing in with ${label}?`,
      description: "You can link it again at any time.",
      action: "Unlink",
    }))) return;
    setBusy(provider);
    setProblem("");
    try {
      await api.unlinkIdentity(provider);
      notify("good", `${label} unlinked.`);
      reload();
    } catch (err) {
      setProblem(problemText(err, "Couldn't unlink that."));
    } finally {
      setBusy("");
    }
  }

  return (
    <Section
      title="Sign in with"
      description="Sign-in providers are linked here, by you."
    >
      <Card>
        <CardContent className="space-y-4">
          {problem && <Notice tone="problem">{problem}</Notice>}

          {data.identities.map((i) => (
            <div key={i.provider} className="flex flex-wrap items-center gap-3">
              <Chip tone="good" className="w-24 justify-center">{i.label}</Chip>
              <span className="text-sm text-muted-foreground">
                {i.email || "linked"} · {whenExact(i.linked_at)}
              </span>
              <Button
                variant="ghost" size="sm" disabled={busy !== ""}
                onClick={() => unlink(i.provider, i.label)}
              >
                Unlink
              </Button>
            </div>
          ))}

          {data.identities.length === 0 && (
            <p className="text-sm text-muted-foreground">
              You sign in with your password.
            </p>
          )}

          {addable.length > 0 && (
            <div className="flex flex-wrap gap-2">
              {addable.map((p) => (
                <Button
                  key={p.provider} variant="outline" size="sm"
                  disabled={busy !== ""} onClick={() => link(p.provider)}
                >
                  {busy === p.provider ? "Taking you there…" : `Link ${p.label}`}
                </Button>
              ))}
            </div>
          )}
        </CardContent>
      </Card>
    </Section>
  );
}

/**
 * The name the console calls you by. Self-service: `PATCH /api/account` carries
 * no identifier, so it can only edit the calling account.
 *
 * The field holds `display_name` and the heading renders `name`. Seeding the
 * box with `name` would offer to save its fallback -- the address -- as a name.
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
      // The server's answer, not what was typed, so a cleared name shows the
      // address the way the next reader will see it.
      adopt({ ...session, name: saved.name, display_name: saved.display_name });
      setName(saved.display_name);
      notify("good", next ? "Saved." : "Cleared. The dashboard will use your address.");
    } catch (err) {
      // The server's sentence: it is the only thing that knows which rule
      // the value broke.
      setProblem(problemText(err, "Couldn't change it."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Section
      title="Name"
      description="What the dashboard calls you. Your email is what identifies the account, and what the history records."
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
                  : "Not set, so the dashboard calls you by your address."}
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
 * Changing your own password. Only `PATCH /api/users/{id}` can, and it takes
 * admin, so anyone else is told to ask rather than shown a form that fails.
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
          Ask an administrator to change the password for {email}. You cannot
          change it yourself here.
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
      setProblem(problemText(err, "Couldn't change it."));
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
