import { useState, type ReactNode } from "react";
import { api, ApiError, type Meta, type Session } from "@/lib/api";
import { Notice } from "@/components/chrome";
import { Brand } from "@/components/shell";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

/**
 * The frame both signed-out screens sit in.
 *
 * Signing in and claiming a new host are different questions, but they are
 * asked in the same place and looked at the same way. Two copies of this
 * drifted apart in the ways chrome always does -- the version footer said a
 * different thing on one of them -- so there is one.
 */
function SignedOutCard({ meta, error, title, children }: {
  meta: Meta | null;
  error?: string;
  title?: string;
  children: ReactNode;
}) {
  return (
    <div className="grid min-h-screen place-items-center px-4 py-12">
      <div className="w-full max-w-sm space-y-5">
        <div className="flex justify-center">
          <Brand compact />
        </div>

        <Card>
          <CardContent className="space-y-4">
            {title && <h1 className="text-base font-semibold">{title}</h1>}
            {error && <Notice tone="problem">{error}</Notice>}
            {children}
          </CardContent>
        </Card>

        {meta && (
          <p className="text-center text-xs text-muted-foreground">mcpd {meta.version}</p>
        )}
      </div>
    </div>
  );
}

export function SignIn({ meta, onDone }: {
  meta: Meta | null;
  onDone: (s: Session) => void;
}) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      onDone(await api.signIn(email.trim(), password));
    } catch (err) {
      setError(
        err instanceof ApiError && err.status === 401
          ? "That email and password did not match."
          : "Couldn't reach mcpd. Is it running?",
      );
    } finally {
      setBusy(false);
    }
  }

  return (
    <SignedOutCard meta={meta} error={error}>
      <form className="space-y-4" onSubmit={submit}>
        <div className="space-y-1.5">
          <Label htmlFor="email">Email</Label>
          <Input
            id="email" type="email" autoComplete="username" autoFocus
            value={email} placeholder="you@example.com"
            onChange={(e) => setEmail(e.target.value)}
          />
        </div>

        <div className="space-y-1.5">
          <Label htmlFor="password">Password</Label>
          <Input
            id="password" type="password" autoComplete="current-password"
            value={password} placeholder="Your password"
            onChange={(e) => setPassword(e.target.value)}
          />
        </div>

        <Button className="w-full" type="submit" disabled={busy || !email.trim() || !password}>
          {busy ? "Signing in…" : "Sign in"}
        </Button>
      </form>
    </SignedOutCard>
  );
}

/**
 * Claiming a new instance.
 *
 * The first account is an administrator because there is nobody to grant it
 * the role afterwards. Registration stops being offered the moment an account
 * exists, so this is a door that closes behind the first person through it.
 */
export function FirstRun({ meta, onDone }: {
  meta: Meta | null;
  onDone: (s: Session) => void;
}) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const mismatch = confirm !== "" && password !== confirm;
  const ready = email.trim() !== "" && password.length >= 12 && !mismatch && confirm !== "";

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      onDone(await api.registerFirst(email.trim(), password));
    } catch (err) {
      setError(
        err instanceof ApiError
          ? (err.status === 409
            ? "Someone already claimed this instance. Reload and sign in."
            : err.detail)
          : "Couldn't reach mcpd. Is it running?",
      );
    } finally {
      setBusy(false);
    }
  }

  return (
    <SignedOutCard meta={meta} error={error} title="Create the first account">
      <p className="text-sm text-muted-foreground">
        Nobody has claimed this host yet. This account will be an administrator;
        you can add others once you are in.
      </p>

      <form className="space-y-4" onSubmit={submit}>
        <div className="space-y-1.5">
          <Label htmlFor="su-email">Email</Label>
          <Input
            id="su-email" type="email" autoComplete="username" autoFocus
            value={email} placeholder="you@example.com"
            onChange={(e) => setEmail(e.target.value)}
          />
        </div>

        <div className="space-y-1.5">
          <Label htmlFor="su-password">Password</Label>
          <Input
            id="su-password" type="password" autoComplete="new-password"
            value={password} placeholder="At least 12 characters"
            onChange={(e) => setPassword(e.target.value)}
          />
        </div>

        <div className="space-y-1.5">
          <Label htmlFor="su-confirm">Confirm password</Label>
          <Input
            id="su-confirm" type="password" autoComplete="new-password"
            value={confirm} placeholder="Type it again"
            onChange={(e) => setConfirm(e.target.value)}
          />
          {mismatch && <p className="text-xs text-problem">These do not match.</p>}
        </div>

        <Button className="w-full" type="submit" disabled={busy || !ready}>
          {busy ? "Creating…" : "Create account"}
        </Button>
      </form>
    </SignedOutCard>
  );
}
