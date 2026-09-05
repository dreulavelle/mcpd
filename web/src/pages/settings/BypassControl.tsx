import { useCallback, useState, type FormEvent } from "react";
import { api, type BypassStatus, problemText } from "@/lib/api";
import { when } from "@/lib/format";
import { usePoll } from "@/lib/hooks";
import { useCan } from "@/lib/session";
import { Section } from "@/components/chrome";
import { useNotify } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";

/**
 * Stop asking me, for a while.
 *
 * This exists because the alternative is worse. Somebody working through a
 * change at two in the morning has one lever otherwise — widen a standing rule
 * — and a widened rule has no expiry and is still there next month. A window
 * that cannot be made permanent is the safer shape of the same wish.
 *
 * It is deliberately weaker than a rule, not stronger. It cannot authorise an
 * irreversible change, cannot exceed its own ceiling, cannot reach critical,
 * and cannot override an exclusion — a rule that says "never" about one action
 * keeps saying it while this is open.
 */
export function BypassControl() {
  const [status, setStatus] = useState<BypassStatus | null>(null);
  const [minutes, setMinutes] = useState(60);
  const [ceiling, setCeiling] = useState("low");
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const mayWrite = useCan("policies:write");
  const notify = useNotify();

  const load = useCallback(() => {
    api.bypassStatus().then(setStatus).catch(() => setStatus(null));
  }, []);
  usePoll(load, 20_000);

  if (!status) return null;

  async function open(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    try {
      await api.openBypass({ minutes, ceiling, reason });
      setReason("");
      notify("good", `Not asking for the next ${minutes} minutes.`);
      load();
    } catch (e) {
      notify("problem", problemText(e, "Couldn't open it."));
    } finally {
      setBusy(false);
    }
  }

  async function close() {
    setBusy(true);
    try {
      const { closed } = await api.revokeBypasses();
      notify("good", closed > 0 ? "Asking again." : "Nothing was open.");
      load();
    } catch (e) {
      notify("problem", problemText(e, "Couldn't close it."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Section
      title="Stop asking, for a while"
      description="A window that closes on its own. It can never be made permanent — that is the difference between this and widening a rule."
    >
      <Card>
        <CardContent className="pt-6">
          {status.active && status.current ? (
            <div className="space-y-3">
              <p className="text-sm">
                Open until <strong>{when(status.current.expires_at)}</strong>,
                covering {status.current.plugin || "every plugin"} up to{" "}
                {status.current.ceiling} risk. Opened by{" "}
                {status.current.created_by}
                {status.current.reason ? ` — ${status.current.reason}` : ""}.
              </p>
              {(status.open ?? 1) > 1 && (
                <p className="text-sm">
                  {status.open} windows are open. Closing starts the asking
                  again for all of them.
                </p>
              )}
              <p className="text-sm text-muted-foreground">
                {status.current.approved === 0
                  ? "Nothing has used it yet."
                  : `${status.current.approved} change${status.current.approved === 1 ? "" : "s"} approved without anybody being asked.`}
              </p>
              {mayWrite && (
                <Button variant="outline" size="sm" disabled={busy} onClick={close}>
                  Start asking again
                </Button>
              )}
            </div>
          ) : mayWrite ? (
            <form onSubmit={open} aria-label="Stop asking" className="space-y-4">
              <div className="grid gap-4 sm:grid-cols-3">
                <div className="space-y-1.5">
                  <Label htmlFor="bypass-minutes">For</Label>
                  <NativeSelect
                    id="bypass-minutes"
                    value={String(minutes)}
                    onChange={(e) => setMinutes(Number(e.target.value))}
                  >
                    <option value="15">15 minutes</option>
                    <option value="60">1 hour</option>
                    <option value="240">4 hours</option>
                    <option value="480">8 hours</option>
                  </NativeSelect>
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="bypass-ceiling">Up to</Label>
                  <NativeSelect
                    id="bypass-ceiling"
                    value={ceiling}
                    onChange={(e) => setCeiling(e.target.value)}
                  >
                    <option value="low">Low risk</option>
                    <option value="medium">Medium risk</option>
                    <option value="high">High risk</option>
                  </NativeSelect>
                  <p className="text-xs text-muted-foreground">
                    Critical is not offered, here or in a rule.
                  </p>
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="bypass-reason">What for</Label>
                  <Input
                    id="bypass-reason"
                    value={reason}
                    placeholder="Migrating the edge switches"
                    onChange={(e) => setReason(e.target.value)}
                  />
                </div>
              </div>
              <Button type="submit" disabled={busy || !reason}>
                Stop asking
              </Button>
              <p className="text-xs text-muted-foreground">
                Every change it lets through records this window as the
                authority, so the trail says it ran because asking was switched
                off rather than because a rule allowed it.
              </p>
            </form>
          ) : (
            <p className="text-sm text-muted-foreground">
              Nothing is open. Every change is being put to a person, subject to
              the rules below.
            </p>
          )}
        </CardContent>
      </Card>
    </Section>
  );
}
