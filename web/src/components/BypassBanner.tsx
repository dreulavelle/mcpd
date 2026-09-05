import { useCallback, useState } from "react";
import { api, type BypassStatus } from "@/lib/api";
import { usePoll } from "@/lib/hooks";
import { useCan } from "@/lib/session";
import { Button } from "@/components/ui/button";

/**
 * A standing warning that this host is approving changes without asking.
 *
 * On every page, not on the Policies page where a bypass is opened. The whole
 * risk of the feature is that somebody opens a window, gets pulled away, and
 * nobody remembers it is open — so it has to be visible from wherever they
 * happen to be, and it has to look like a problem rather than a status.
 *
 * Read is enough to see it; only an administrator is offered the button. An
 * operator who cannot close it can still tell that it is open, which is the
 * half that matters for noticing.
 */
export function BypassBanner() {
  const [status, setStatus] = useState<BypassStatus | null>(null);
  const [busy, setBusy] = useState(false);
  const admin = useCan("policies:write");

  const load = useCallback(() => {
    api.bypassStatus()
      .then(setStatus)
      // Silent. This is a banner on every page, and a host without the
      // feature must not put an error above every one of them.
      .catch(() => setStatus(null));
  }, []);
  usePoll(load, 15_000);

  if (!status?.active || !status.current) return null;
  const b = status.current;

  async function close() {
    setBusy(true);
    try {
      await api.revokeBypasses();
      load();
    } catch {
      // The banner is the error display: a window that is still open is
      // still shown, and the button below is usable again.
    } finally {
      // The banner outlives the window it closed, so the button has to come
      // back for the next one. Left busy, it was greyed out until a reload.
      setBusy(false);
    }
  }

  return (
    <div
      role="status"
      className="mb-4 rounded-md border border-attention/40 bg-attention/10 px-4 py-3"
    >
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="text-sm">
          <strong className="font-medium">
            Changes are being approved without asking anyone.
          </strong>{" "}
          <span className="text-muted-foreground">
            {b.plugin ? `${b.plugin}, ` : "Every plugin, "}
            up to {b.ceiling} risk, for another {remaining(b.seconds_left)}.
            Opened by {b.created_by}
            {b.reason ? ` — ${b.reason}` : ""}.
            {b.approved > 0 && ` ${b.approved} so far.`}
            {(status.open ?? 1) > 1 &&
              ` ${status.open} windows are open, and this is the widest.`}
          </span>
        </div>
        {admin && (
          <Button size="sm" variant="outline" disabled={busy} onClick={close}>
            Close it now
          </Button>
        )}
      </div>
    </div>
  );
}

/** A countdown in the largest unit that is still honest. */
function remaining(seconds: number): string {
  if (seconds <= 60) return "less than a minute";
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes} minutes`;
  const hours = Math.floor(minutes / 60);
  const rest = minutes % 60;
  return rest === 0 ? `${hours}h` : `${hours}h ${rest}m`;
}
