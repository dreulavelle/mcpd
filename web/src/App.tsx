import { useCallback, useEffect, useState } from "react";
import {
  api, setCSRFToken, setUnauthorizedHandler, type AuthOptions, type Meta, type Session,
} from "@/lib/api";
import { usePoll } from "@/lib/hooks";
import { RouterProvider, useRouter } from "@/lib/router";
import { SessionProvider, useCan } from "@/lib/session";
import { takeSSOOutcome } from "@/lib/sso";
import { ErrorBoundary } from "@/components/chrome";
import { GettingStarted } from "@/components/getting-started";
import { Shell } from "@/components/shell";
import { ToastProvider } from "@/components/toast";
import { TooltipProvider } from "@/components/ui/tooltip";
import { AwaitingApproval, FirstRun, SignIn } from "@/pages/signed-out/SignedOut";
import { Routes } from "@/Routes";

// Read once, before React renders anything, because the parameter is stripped
// from the address bar as it is taken -- a refresh should not bring back a
// message about a round trip that finished a while ago. Which screen shows it
// depends on whether the person ended up signed in, which is not known yet: a
// refused sign-in belongs on the sign-in form, and a refused link belongs on
// the profile page beside the button that started it.
const SSO_NOTICE = takeSSOOutcome();

export default function App() {
  const [session, setSession] = useState<Session | null>(null);
  const [meta, setMeta] = useState<Meta | null>(null);
  const [auth, setAuth] = useState<AuthOptions | null>(null);
  // Undecided until the cookie is checked, or the form flashes at everyone.
  const [checked, setChecked] = useState(false);

  const adopt = useCallback((s: Session) => {
    setCSRFToken(s.csrf_token);
    setSession(s);
  }, []);

  useEffect(() => {
    api.meta().then(setMeta).catch(() => setMeta(null));
    // What the signed-out screen may offer. A failure leaves it null, which
    // draws the password form and nothing else -- the honest fallback when
    // this host cannot say what it supports.
    api.authOptions().then(setAuth).catch(() => setAuth(null));
    // The cookie survives a reload; the CSRF token does not.
    api.session().then(adopt).catch(() => undefined).finally(() => setChecked(true));
  }, [adopt]);

  const signOut = useCallback(() => {
    api.signOut().catch(() => undefined).finally(() => {
      setCSRFToken(null);
      setSession(null);
    });
  }, []);

  // A session that expires under the console -- the cookie's lifetime ran
  // out, or an administrator disabled the account -- is met with the sign-in
  // form, not a page of refusals.
  useEffect(() => {
    setUnauthorizedHandler(() => {
      setCSRFToken(null);
      setSession(null);
    });
    return () => setUnauthorizedHandler(null);
  }, []);

  if (!checked || !meta) return null;
  // Nothing to sign in with yet on an unclaimed instance.
  if (!session && meta.needs_setup) return <FirstRun onDone={adopt} />;
  if (!session) {
    return <SignIn auth={auth} notice={SSO_NOTICE} onDone={adopt} />;
  }
  // Signed in, and holding nothing until somebody says so. The server refuses
  // every call this account makes; this is only so the refusals are not what
  // the person meets.
  if (session.status === "pending") {
    return <AwaitingApproval email={session.email} onSignOut={signOut} />;
  }

  return (
    <SessionProvider session={session} onSession={setSession}>
      <TooltipProvider delayDuration={200}>
        <ToastProvider>
          <RouterProvider>
            <Console onSignOut={signOut} version={meta.version} />
          </RouterProvider>
        </ToastProvider>
      </TooltipProvider>
    </SessionProvider>
  );
}

function Console({ onSignOut, version }: { onSignOut: () => void; version: string }) {
  const { path } = useRouter();
  const badges = usePendingCount();

  return (
    <>
      <Shell badges={badges} onSignOut={onSignOut} version={version}>
        {/* Keyed on the path, so a page that failed is rebuilt from scratch when
            it is opened again rather than staying broken for the session. The
            boundary is inside the chrome: whatever happens to a page, the
            navigation out of it survives. */}
        <ErrorBoundary key={path}>
          <Routes />
        </ErrorBoundary>
      </Shell>
      {/* Outside the shell's children and outside the keyed boundary: it is
          pinned to the viewport, it has to survive navigation rather than be
          rebuilt by it, and a boundary of its own means a checklist that throws
          costs the checklist rather than the console. */}
      <ErrorBoundary quiet>
        <GettingStarted />
      </ErrorBoundary>
    </>
  );
}

/** The count beside Approvals, polled wherever the operator is standing. */
function usePendingCount(): Record<string, number> {
  const mayRead = useCan("read");
  const [pending, setPending] = useState(0);

  const load = useCallback(() => {
    if (!mayRead) return;
    api.operations("pending_approval", 200)
      .then((r) => setPending(r.count ?? (r.operations ?? []).length))
      .catch(() => undefined);
  }, [mayRead]);
  usePoll(load, 20_000);

  return { "/approvals": pending };
}
