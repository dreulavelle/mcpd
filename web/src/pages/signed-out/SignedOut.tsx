import { useState, type ReactNode } from "react";
import {
  api, ApiError, type AuthOptions, type Meta, type ProviderName, type Session,
} from "@/lib/api";
import { consumeSSOOutcome } from "@/lib/sso";
import { Notice } from "@/components/chrome";
import { Brand } from "@/components/shell";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Separator } from "@/components/ui/separator";

/** The frame both signed-out screens sit in. */
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

/**
 * The buttons that hand the browser to a provider.
 *
 * A full page navigation rather than a fetch: the provider's sign-in page is
 * somewhere the person has to actually go. What comes back from the start
 * endpoint is the URL, and the cookie the state is bound to came back with it.
 */
function ProviderButtons({ providers, onProblem }: {
  providers: { provider: ProviderName; label: string }[];
  onProblem: (message: string) => void;
}) {
  const [busy, setBusy] = useState("");

  if (providers.length === 0) return null;

  async function go(provider: ProviderName) {
    setBusy(provider);
    try {
      const { authorization_url } = await api.ssoStart(provider);
      window.location.assign(authorization_url);
    } catch (e) {
      setBusy("");
      onProblem(e instanceof ApiError ? e.detail : "Couldn't start that sign-in.");
    }
  }

  return (
    <div className="space-y-2">
      {providers.map((p) => (
        <Button
          key={p.provider} variant="outline" className="w-full"
          disabled={busy !== ""} onClick={() => go(p.provider)}
        >
          {busy === p.provider ? "Taking you there…" : `Continue with ${p.label}`}
        </Button>
      ))}
    </div>
  );
}

function Or() {
  return (
    <div className="flex items-center gap-3">
      <Separator className="flex-1" />
      <span className="text-xs text-muted-foreground">or</span>
      <Separator className="flex-1" />
    </div>
  );
}

export function SignIn({ meta, auth, notice, onDone }: {
  meta: Meta | null;
  /** What this host offers. Null while it is still being asked. */
  auth: AuthOptions | null;
  /**
   * A message from a provider round trip that has just come back refused.
   * Omitted in the app, which lets this screen take the one the URL carried.
   */
  notice?: string;
  onDone: (s: Session) => void;
}) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  // Taken, not read. The `address_taken` message tells somebody to sign in and
  // link the provider from their profile -- so leaving it behind would have it
  // reappear on the profile page they were just sent to, as a failure, next to
  // the button that is the thing it asked them to press.
  const [error, setError] = useState(() => notice ?? consumeSSOOutcome());
  const [busy, setBusy] = useState(false);
  const [signingUp, setSigningUp] = useState(false);

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

  if (signingUp) {
    return <SignUp meta={meta} onDone={onDone} onCancel={() => setSigningUp(false)} />;
  }

  const providers = auth?.providers ?? [];

  return (
    <SignedOutCard meta={meta} error={error}>
      {providers.length > 0 && (
        <>
          <ProviderButtons providers={providers} onProblem={setError} />
          <Or />
        </>
      )}

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

      {auth?.registration && (
        <p className="text-center text-sm text-muted-foreground">
          No account?{" "}
          <button
            type="button"
            className="underline underline-offset-4"
            onClick={() => { setError(""); setSigningUp(true); }}
          >
            Ask for one
          </button>
        </p>
      )}
    </SignedOutCard>
  );
}

/**
 * Asking for an account on a host somebody already owns.
 *
 * It always waits for an administrator, whatever the host's approval setting
 * says, and the form says so up front rather than at the end. Nothing between
 * this form and the row has checked that the person can receive mail at the
 * address they typed — which is the difference between this and the provider
 * buttons above it, and the reason the setting cannot switch this off.
 */
function SignUp({ meta, onDone, onCancel }: {
  meta: Meta | null;
  onDone: (s: Session) => void;
  onCancel: () => void;
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
      onDone(await api.register(email.trim(), password));
    } catch (err) {
      setError(err instanceof ApiError ? err.detail : "Couldn't reach mcpd. Is it running?");
    } finally {
      setBusy(false);
    }
  }

  return (
    <SignedOutCard meta={meta} error={error} title="Ask for an account">
      <p className="text-sm text-muted-foreground">
        An administrator has to say yes before you can do anything here.
      </p>

      <form className="space-y-4" onSubmit={submit}>
        <div className="space-y-1.5">
          <Label htmlFor="reg-email">Email</Label>
          <Input
            id="reg-email" type="email" autoComplete="username" autoFocus
            value={email} placeholder="you@example.com"
            onChange={(e) => setEmail(e.target.value)}
          />
        </div>

        <div className="space-y-1.5">
          <Label htmlFor="reg-password">Password</Label>
          <Input
            id="reg-password" type="password" autoComplete="new-password"
            value={password} placeholder="At least 12 characters"
            onChange={(e) => setPassword(e.target.value)}
          />
        </div>

        <div className="space-y-1.5">
          <Label htmlFor="reg-confirm">Confirm password</Label>
          <Input
            id="reg-confirm" type="password" autoComplete="new-password"
            value={confirm} placeholder="Type it again"
            onChange={(e) => setConfirm(e.target.value)}
          />
          {mismatch && <p className="text-xs text-problem">These do not match.</p>}
        </div>

        <Button className="w-full" type="submit" disabled={busy || !ready}>
          {busy ? "Asking…" : "Ask for an account"}
        </Button>
      </form>

      <p className="text-center text-sm text-muted-foreground">
        <button type="button" className="underline underline-offset-4" onClick={onCancel}>
          Back to sign in
        </button>
      </p>
    </SignedOutCard>
  );
}

/**
 * Claiming a new instance. The first account is an administrator because there
 * is nobody to grant it the role afterwards, and the door closes behind it.
 *
 * Password only, deliberately. Claiming is what makes somebody the owner of
 * this host, and a provider round trip is not a claim mcpd can honour: whoever
 * held an account at Google would own any fresh host they could reach.
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
        you can add others, and turn on sign-in with Google, GitHub or Microsoft,
        once you are in.
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

/**
 * The whole of the console for an account nobody has approved yet.
 *
 * It is signed in — that is how it proved who it is — and it holds no
 * capability at all, so there is nothing else to show it. Nothing here is what
 * enforces that: the server refuses every call such an account makes, and this
 * screen only exists so the refusals are not what the person meets.
 */
export function AwaitingApproval({ meta, email, onSignOut }: {
  meta: Meta | null;
  email: string;
  onSignOut: () => void;
}) {
  return (
    <SignedOutCard meta={meta} title="Waiting for approval">
      <p className="text-sm text-muted-foreground">
        You are signed in as <span className="font-medium">{email}</span>. An
        administrator has to approve the account before you can use mcpd.
        Reload this page once they have.
      </p>
      <Button variant="outline" className="w-full" onClick={onSignOut}>
        Sign out
      </Button>
    </SignedOutCard>
  );
}
