import { useCallback, useState } from "react";
import { api, ApiError, type Meta, type User } from "@/lib/api";
import { capabilitiesOf, type Capability } from "@/lib/capabilities";
import { whenExact } from "@/lib/format";
import { useLoader } from "@/lib/hooks";
import { useCan, useSession } from "@/lib/session";
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
 * The account you are signed in as.
 *
 * It exists mostly to answer "what am I allowed to do here", which was
 * previously answerable only by finding a control that was missing. The
 * capability list is read from the same table the navigation is gated on, so
 * the two cannot disagree.
 */
export function Account() {
  const session = useSession();
  const load = useCallback(() => api.meta(), []);
  const { data: meta } = useLoader<Meta>(load, "Couldn't read the host's details.");

  if (!session) return null;
  const held = capabilitiesOf(session.role);

  return (
    <>
      <PageHeader
        title="Account"
        lede="Who you are signed in as, and what that lets you do."
      />

      <div className="space-y-6">
        <Card>
          <CardContent>
            <dl className="grid gap-4 sm:grid-cols-2">
              <Detail label="Email">{session.email}</Detail>
              <Detail label="Name">
                {session.display_name || (
                  <span className="text-muted-foreground">Not set</span>
                )}
              </Detail>
              {/* The one place a role is read rather than a capability, and it
                  is reading it in order to print it. Nothing branches here. */}
              <Detail label="Role">
                {session.role === "admin" ? "Administrator" : "User"}
              </Detail>
              <Detail label="Session expires">{whenExact(session.expires_at)}</Detail>
              <Detail label="Can reach" className="sm:col-span-2">
                {session.plugins.includes("*")
                  ? "Every system on this host"
                  : session.plugins.join(", ") || "Nothing"}
              </Detail>
              {meta && (
                <Detail label="Host" className="sm:col-span-2">
                  mcpd {meta.version}
                </Detail>
              )}
            </dl>
          </CardContent>
        </Card>

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

        <ChangePassword email={session.email} />
      </div>
    </>
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
function ChangePassword({ email }: { email: string }) {
  const mayEditAccounts = useCan("admin");
  const notify = useNotify();
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [problem, setProblem] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(
    () => (mayEditAccounts ? api.users() : Promise.resolve({ users: [], count: 0 })),
    [mayEditAccounts],
  );
  const { data } = useLoader(load, "Couldn't load accounts.");
  const self: User | undefined = (data?.users ?? []).find((u) => u.self);

  if (!mayEditAccounts) {
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
