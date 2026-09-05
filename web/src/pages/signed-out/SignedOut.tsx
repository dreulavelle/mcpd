import { useState, type ReactNode } from "react";
import {
  api,
  ApiError,
  type AuthOptions,
  type ProviderName,
  type Session,
  problemText,
} from "@/lib/api";
import { consumeSSOOutcome } from "@/lib/sso";
import { Notice } from "@/components/chrome";
import { Brand } from "@/components/shell";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Separator } from "@/components/ui/separator";
import { ProviderMark } from "@/components/provider-mark";

/**
 * What this host is, for somebody who has arrived at it and is deciding
 * whether they are in the right place.
 *
 * Three statements rather than a description, and each one is a thing mcpd
 * actually does — the sign-in page is the first screen anybody sees, and it
 * should not be the one place that oversells.
 */
const FACTS = [
  "One address your assistants reach every system through.",
  "Reads run. Changes are held, or approved by a rule you wrote.",
  "Every call is on an append-only record that notices tampering.",
];

/**
 * The frame every signed-out screen sits in.
 *
 * Two columns on a wide window and one on a narrow. The left is decoration
 * with a job: a card alone in the middle of a 27-inch display reads as an
 * unfinished page, and the person arriving here often has not been told what
 * mcpd is. It is plain CSS — a gradient and a grid — because nothing on the
 * sign-in path should wait on a script to finish before it can be read.
 */
function SignedOutCard({ error, title, children }: {
  error?: string;
  title?: string;
  children: ReactNode;
}) {
  return (
    <div className="min-h-screen lg:grid lg:grid-cols-2">
      <aside
        className="relative hidden overflow-hidden bg-primary p-10 text-primary-foreground lg:flex lg:flex-col lg:justify-center"
      >
        {/* A grid that fades out, drawn in the foreground colour so it holds
            up in both themes without a second definition. */}
        <div
          aria-hidden="true"
          className="pointer-events-none absolute inset-0 opacity-[0.07]"
          style={{
            backgroundImage:
              "linear-gradient(currentColor 1px, transparent 1px), linear-gradient(90deg, currentColor 1px, transparent 1px)",
            backgroundSize: "44px 44px",
            maskImage: "radial-gradient(ellipse at 30% 20%, #000 0%, transparent 75%)",
            WebkitMaskImage: "radial-gradient(ellipse at 30% 20%, #000 0%, transparent 75%)",
          }}
        />

        <div className="absolute top-10 left-10 flex items-center gap-2">
          <span
            aria-hidden="true"
            className="grid size-7 place-items-center rounded-md bg-primary-foreground font-mono text-sm font-bold text-primary"
          >
            m
          </span>
          <span className="text-lg font-semibold tracking-tight">mcpd</span>
        </div>

        <div className="relative max-w-md space-y-6">
          <h2 className="text-2xl font-semibold leading-snug tracking-tight">
            A control plane for the systems your assistants can reach.
          </h2>
          <ul className="space-y-3 text-sm/relaxed opacity-90">
            {FACTS.map((fact) => (
              <li key={fact} className="flex gap-3">
                <span aria-hidden="true" className="mt-2 size-1.5 shrink-0 rounded-full bg-current" />
                {fact}
              </li>
            ))}
          </ul>
        </div>
      </aside>

      <div className="grid place-items-center px-4 py-12">
        <div className="w-full max-w-sm space-y-5">
          {/* The mark belongs on every window narrow enough to have lost the
              panel that carries it. */}
          <div className="flex justify-center lg:hidden">
            <Brand compact />
          </div>

          <Card>
            <CardContent className="space-y-4">
              {title && <h1 className="text-base font-semibold">{title}</h1>}
              {error && <Notice tone="problem">{error}</Notice>}
              {children}
            </CardContent>
          </Card>
        </div>
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
      onProblem(problemText(e, "Couldn't start that sign-in."));
    }
  }

  return (
    <div className="space-y-2">
      {providers.map((p) => (
        <Button
          key={p.provider} variant="outline"
          className="w-full justify-start gap-3"
          disabled={busy !== ""} onClick={() => go(p.provider)}
        >
          <ProviderMark provider={p.provider} />
          {/* The label is centred within what the mark leaves, rather than
              running from it, so a column of buttons has one text edge. */}
          <span className="flex-1 text-center">
            {busy === p.provider ? "Taking you there…" : `Continue with ${p.label}`}
          </span>
          {/* Balances the mark so the centring above is true. */}
          <span aria-hidden="true" className="size-4 shrink-0" />
        </Button>
      ))}
    </div>
  );
}

/** The line between the providers and the form, which says what is below it. */
function Or({ label }: { label: string }) {
  return (
    <div className="flex items-center gap-3">
      <Separator className="flex-1" />
      <span className="shrink-0 text-xs text-muted-foreground">{label}</span>
      <Separator className="flex-1" />
    </div>
  );
}

export function SignIn({ auth, notice, onDone }: {
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
    return <SignUp onDone={onDone} onCancel={() => setSigningUp(false)} />;
  }

  const providers = auth?.providers ?? [];

  return (
    <SignedOutCard error={error} title="Sign in">
      {providers.length > 0 && (
        <>
          <ProviderButtons providers={providers} onProblem={setError} />
          <Or label="or continue with email" />
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
function SignUp({ onDone, onCancel }: {
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
      setError(problemText(err, "Couldn't reach mcpd. Is it running?"));
    } finally {
      setBusy(false);
    }
  }

  return (
    <SignedOutCard error={error} title="Ask for an account">
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
export function FirstRun({ onDone }: {
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
    <SignedOutCard error={error} title="Create the first account">
      <p className="text-sm text-muted-foreground">
        Nobody has claimed this host yet. This account will be an administrator;
        you can add others, and turn on sign-in with Google, GitHub, Microsoft
        or your own provider, once you are in.
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
export function AwaitingApproval({ email, onSignOut }: {
  email: string;
  onSignOut: () => void;
}) {
  return (
    <SignedOutCard title="Waiting for approval">
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
