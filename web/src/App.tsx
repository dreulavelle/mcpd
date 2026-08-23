import { useCallback, useEffect, useState } from "react";
import { api, setCSRFToken, type Meta, type Session } from "@/lib/api";
import { usePoll } from "@/lib/hooks";
import { RouterProvider, useRouter } from "@/lib/router";
import { SessionProvider, useCan } from "@/lib/session";
import { ErrorBoundary } from "@/components/chrome";
import { GettingStarted } from "@/components/getting-started";
import { Shell } from "@/components/shell";
import { ToastProvider } from "@/components/toast";
import { TooltipProvider } from "@/components/ui/tooltip";
import { FirstRun, SignIn } from "@/pages/signed-out/SignedOut";
import { Routes } from "@/Routes";

export default function App() {
  const [session, setSession] = useState<Session | null>(null);
  const [meta, setMeta] = useState<Meta | null>(null);
  // Undecided until the cookie is checked, or the form flashes at everyone.
  const [checked, setChecked] = useState(false);

  const adopt = useCallback((s: Session) => {
    setCSRFToken(s.csrf_token);
    setSession(s);
  }, []);

  useEffect(() => {
    api.meta().then(setMeta).catch(() => setMeta(null));
    // The cookie survives a reload; the CSRF token does not.
    api.session().then(adopt).catch(() => undefined).finally(() => setChecked(true));
  }, [adopt]);

  const signOut = useCallback(() => {
    api.signOut().catch(() => undefined).finally(() => {
      setCSRFToken(null);
      setSession(null);
    });
  }, []);

  if (!checked || !meta) return null;
  // Nothing to sign in with yet on an unclaimed instance.
  if (!session && meta.needs_setup) return <FirstRun meta={meta} onDone={adopt} />;
  if (!session) return <SignIn meta={meta} onDone={adopt} />;

  return (
    <SessionProvider session={session} onSession={setSession}>
      <TooltipProvider delayDuration={200}>
        <ToastProvider>
          <RouterProvider>
            <Console onSignOut={signOut} />
          </RouterProvider>
        </ToastProvider>
      </TooltipProvider>
    </SessionProvider>
  );
}

function Console({ onSignOut }: { onSignOut: () => void }) {
  const { path } = useRouter();
  const badges = usePendingCount();

  return (
    <>
      <Shell badges={badges} onSignOut={onSignOut}>
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
