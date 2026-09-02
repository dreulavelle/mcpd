import { useEffect, useState } from "react";
import { Clock } from "lucide-react";
import { useSession } from "@/lib/session";

/** How long before the end the warning appears. */
const WARN_MS = 10 * 60_000;

/**
 * A session ends by the clock, not by activity, and the console used to find
 * out when its next request was refused -- which is exactly when somebody is
 * halfway through a reason they are about to lose. This says so ten minutes
 * before, above the page, and counts down. It cannot extend the session:
 * there is no endpoint that would, and a warning that offered a button it
 * could not honour would be worse than none.
 */
export function SessionExpiry() {
  const session = useSession();
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 30_000);
    return () => clearInterval(t);
  }, []);

  if (!session) return null;
  const ends = Date.parse(session.expires_at);
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
