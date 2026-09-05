import { useEffect, useState, type ReactNode } from "react";
import {
  api,
  ApiError,
  type AuthOptions,
  type PendingLink,
  type ProviderName,
  type Session,
  problemText,
} from "@/lib/api";
import { consumeSSOOutcome } from "@/lib/sso";
import { Notice } from "@/components/chrome";
import { Brand } from "@/components/shell";
import { Mark } from "@/components/mark";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Separator } from "@/components/ui/separator";
import { ProviderMark } from "@/components/provider-mark";
import { NetworkField } from "./NetworkField";

/**
 * What this host is, for somebody who has arrived at it and is deciding
 * whether they are in the right place.
 *
 * Three statements rather than a description, and each one is a thing mcpd
 * actually does — the sign-in page is the first screen anybody sees, and it
 * should not be the one place that oversells. Short enough to sit on one
 * line each, so they read as a set.
 */
const FACTS = [
  "Assistants reach only the systems you allow.",
  "Reads go straight through. Changes wait for approval.",
  "Every call is recorded, and the record can't be changed.",
];

/**
 * The frame every signed-out screen sits in.
 *
 * Two columns on a wide window and one on a narrow. The left is decoration
 * with a job: a card alone in the middle of a 27-inch display reads as an
 * unfinished page, and the person arriving here often has not been told what
 * mcpd is. The panel is the brand's own night in both themes, with a field of
 * linked systems behind the words that lights up where the pointer goes.
 *
 * The words are DOM and the panel has its colour from CSS. The field is a
 * canvas underneath them that is allowed not to draw, so nothing on the
 * sign-in path waits on a script to finish before it can be read.
 */
function SignedOutCard({ error, title, children }: {
  error?: string;
  title?: string;
  children: ReactNode;
}) {
  return (
    <div className="min-h-screen lg:grid lg:grid-cols-[minmax(0,5fr)_minmax(0,4fr)]">
      <aside
        className="relative hidden overflow-hidden bg-panel text-panel-foreground lg:flex lg:flex-col lg:justify-between lg:border-r lg:p-12"
      >
        <NetworkField />

        <div className="relative flex items-center gap-2.5">
          <Mark className="size-7 text-panel-accent" />
          <span className="text-lg font-semibold tracking-tight">mcpd</span>
        </div>

        <div className="relative max-w-md space-y-3">
          <h2 className="text-3xl font-semibold leading-tight tracking-tight text-balance">
            Private infrastructure, connected to AI.
          </h2>
          <p className="text-[15px]/relaxed text-panel-muted">
            One gateway between your assistants and the systems inside your network.
          </p>
        </div>

        <ul className="relative max-w-md space-y-2.5 text-[15px]/relaxed text-panel-muted">
          {FACTS.map((fact) => (
            <li key={fact} className="flex items-baseline gap-3">
              <span aria-hidden="true" className="size-1.5 shrink-0 translate-y-[-1px] rounded-full bg-panel-accent" />
              {fact}
            </li>
          ))}
        </ul>
      </aside>

      <div className="grid place-items-center bg-background px-4 py-12">
        <div className="w-full max-w-sm space-y-6">
          {/* The mark belongs on every window narrow enough to have lost the
              panel that carries it. */}
          <div className="flex justify-center lg:hidden">
            <Brand compact />
          </div>

          <Card>
            <CardContent className="space-y-4">
              {title && <h1 className="text-lg font-semibold tracking-tight">{title}</h1>}
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

export function SignIn({ auth, notice, outcome, onDone }: {
  /** What this host offers. Null while it is still being asked. */
  auth: AuthOptions | null;
  /**
   * A message from a provider round trip that has just come back refused.
   * Omitted in the app, which lets this screen take the one the URL carried.
   */
  notice?: string;
  /**
   * The code that round trip came back with. One of them is not a refusal:
   * `link_password` means an account here already uses the address and the
   * offer to connect this provider to it is waiting.
   */
  outcome?: string;
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
  // Spent by the screen finishing rather than by being read, so a re-render
  // does not drop somebody out of the middle of connecting a provider.
  const [connecting, setConnecting] = useState(outcome === "link_password");

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

  if (connecting) {
    return (
      <ConnectProvider
        onDone={onDone}
        onCancel={(why) => { setConnecting(false); setError(why ?? ""); }}
      />
    );
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
 * Connecting a provider to an account that already uses the address.
 *
 * The refusal this replaces was correct and was a dead end: mcpd will not hand
 * an account over because an address matched, and the person on the other end
 * of that sentence was usually its owner. The password is the one proof the
 * provider cannot give, and this is where it is given — once.
 *
 * It asks the server before it draws anything. The code in the address bar is
 * a parameter somebody can type, and a password field on the strength of one
 * would be asking for a password against nothing.
 */
function ConnectProvider({ onDone, onCancel }: {
  onDone: (s: Session) => void;
  /** Back to the ordinary form, optionally saying why. */
  onCancel: (why?: string) => void;
}) {
  const [link, setLink] = useState<PendingLink | null>(null);
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let live = true;
    api.pendingLink()
      .then((l) => { if (live) setLink(l); })
      // Nothing is waiting: the offer expired, it belongs to another browser,
      // or somebody typed the parameter. The sign-in form is the honest
      // answer, and it is where they were going anyway.
      .catch(() => { if (live) onCancel(); });
    return () => { live = false; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      onDone(await api.connectPendingLink(password));
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        setError("That password did not match.");
      } else if (err instanceof ApiError && err.status === 404) {
        // Several things retire an offer -- three wrong passwords, the ten
        // minutes running out, "Not now" in another tab, a second sign-in
        // replacing it -- and the server does not say which, on purpose.
        // Naming one of them would be wrong most of the time, and what to do
        // is the same either way.
        onCancel("That offer is no longer open. Start the sign-in again.");
      } else {
        setError(problemText(err, "Couldn't connect that provider. Try again in a moment."));
      }
    } finally {
      setBusy(false);
      setPassword("");
    }
  }

  // Nothing is drawn until the server has confirmed the offer. A card with a
  // heading and no fields would read as a screen that failed to load.
  if (!link) return null;

  function notNow() {
    api.discardPendingLink().catch(() => undefined).finally(() => onCancel());
  }

  return (
    <SignedOutCard error={error} title={`Connect ${link.label} to your account`}>
      <p className="text-sm text-muted-foreground">
        An account here already uses <span className="font-medium">{link.email}</span>.
        Enter its password once to connect {link.label}.
      </p>

      <form className="space-y-4" onSubmit={submit}>
        <div className="space-y-1.5">
          <Label htmlFor="connect-password">Password</Label>
          <Input
            id="connect-password" type="password" autoComplete="current-password" autoFocus
            value={password} placeholder="Your password"
            onChange={(e) => setPassword(e.target.value)}
          />
        </div>

        <Button className="w-full" type="submit" disabled={busy || !password}>
          {busy ? "Connecting…" : "Connect and sign in"}
        </Button>
      </form>

      <p className="text-center text-sm text-muted-foreground">
        <button type="button" className="underline underline-offset-4" onClick={notNow}>
          Not now
        </button>
      </p>
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
      // problemText covers a 500 mcpd itself answered, so this cannot claim
      // mcpd is not running. The two sites that branch on ApiError themselves
      // still can, because they only reach it when nothing answered.
      setError(problemText(err, "Couldn't sign up. Try again in a moment."));
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
        err instanceof ApiError && err.status === 409
          ? "Someone has already set this host up. Reload the page and sign in."
          : problemText(err, "Couldn't set this host up. Try again in a moment."),
      );
    } finally {
      setBusy(false);
    }
  }

  return (
    <SignedOutCard error={error} title="Create the first account">
      <p className="text-sm text-muted-foreground">
        Nobody has set this host up yet. This first account is an
        administrator. You can add other people, and turn on sign-in with
        Google, GitHub or Microsoft, once you are in.
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
