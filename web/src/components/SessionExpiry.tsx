import { useEffect, useState } from "react";
import { Clock } from "lucide-react";
import { useSession } from "@/lib/session";

/** How long before the end the warning appears. */
const WARN_MS = 10 * 60_000;

/**
 * A session ends when the first of two clocks runs out: the idle window, which
 * moves whenever somebody does something, and the ceiling, which never moves.
 * The console used to find out when its next request was refused -- exactly
 * when somebody is halfway through a reason they are about to lose.
 *
 * `ends_at` is the nearer of the two and is what this counts down. Reading the
 * ceiling instead would promise a week to somebody about to be signed out for
 * going quiet.
 *
 * There is still no button. Doing anything at all is what extends it now, so a
 * warning somebody is reading has already stopped being true the moment they
 * click to dismiss it -- and the countdown will simply go away.
 */
export function SessionExpiry() {
  const session = useSession();
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 30_000);
    return () => clearInterval(t);
  }, []);

  if (!session) return null;
  const ends = Date.parse(session.ends_at ?? session.expires_at);
  if (Number.isNaN(ends)) return null;
  const left = ends - now;
  if (left > WARN_MS) return null;

  const minutes = Math.max(0, Math.ceil(left / 60_000));
  return (
    <div
      role="status"
      className="mb-4 flex items-start gap-2 rounded-md border border-attention/40 bg-attention/10 px-4 py-3 text-sm"
    >
      <Clock className="mt-0.5 size-4 shrink-0 text-attention" aria-hidden="true" />
      <span>
        <strong className="font-medium">
          {minutes <= 1 ? "Your session ends in under a minute." : `Your session ends in ${minutes} minutes.`}
        </strong>{" "}
        <span className="text-muted-foreground">
          Finish what you are writing; you will be asked to sign in again, and
          a form left half-filled is lost.
        </span>
      </span>
    </div>
  );
}
