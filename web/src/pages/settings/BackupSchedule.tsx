import { useCallback, useState } from "react";
import {
  api, ApiError, problemText,
  type BackupSchedule as ScheduleStatus,
  type SettingField, type SettingsPayload,
} from "@/lib/api";
import { browserZone, WEEKDAYS } from "@/lib/backup";
import { relative, whenExact } from "@/lib/format";
import { useLoader } from "@/lib/hooks";
import { useCan } from "@/lib/session";
import { Loading, Notice } from "@/components/chrome";
import { useNotify } from "@/components/toast";
import { Button } from "@/components/ui/button";
import {
  Card, CardContent, CardHeader, CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { Switch } from "@/components/ui/switch";

/** The keys this form writes. The catalog is what they mean. */
const ENABLED = "backup.schedule.enabled";
const CADENCE = "backup.schedule.cadence";
const WEEKDAY = "backup.schedule.weekday";
const TIME = "backup.schedule.time";
const TIMEZONE = "backup.schedule.timezone";
const PASSPHRASE = "backup.passphrase";

/**
 * When mcpd backs itself up without being asked.
 *
 * Every one of these is an ordinary setting, written through the settings API
 * like all the others, so the catalog stays the one authority for what they
 * mean and what they may hold. The labels and the help text below come from
 * that catalog rather than being written down a second time here.
 *
 * It is a form of its own rather than the shared SettingsForm for two reasons,
 * both about the two fields a generic renderer cannot help with: a weekday is
 * stored as 0 to 6 and nobody should be asked to do that arithmetic, and a
 * time zone is a name this browser already knows and can offer.
 */
export function BackupSchedule({ schedule, onSaved }: {
  /** What the host says will happen. Absent on a host with no runner wired. */
  schedule?: ScheduleStatus;
  onSaved: () => void;
}) {
  const admin = useCan("system:write");
  const notify = useNotify();
  const load = useCallback(() => api.settings(), []);
  const { data: settings, error, reload } = useLoader(
    load, "Couldn't read the schedule.");

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Schedule</CardTitle>
        <p className="text-sm text-muted-foreground">
          A backup you have to remember to take is one that stops happening.
        </p>
      </CardHeader>
      <CardContent className="space-y-5">
        <NextRun schedule={schedule} />
        {error && <Notice tone="problem">{error}</Notice>}
        {!settings ? <Loading rows={3} /> : (
          <ScheduleForm
            settings={settings} readOnly={!admin}
            onSaved={() => { reload(); onSaved(); notify("good", "Saved."); }}
          />
        )}
      </CardContent>
    </Card>
  );
}

/**
 * When the next one is, or why there will not be one.
 *
 * A schedule switched on with no destination, or with no passphrase, is a page
 * that looks configured and does nothing. The host answers both in one field,
 * so this renders whichever it sent.
 */
function NextRun({ schedule }: { schedule?: ScheduleStatus }) {
  if (!schedule) {
    return (
      <Notice tone="neutral">
        This host is not set up to send backups on its own.
      </Notice>
    );
  }
  if (!schedule.next_run_at) {
    return (
      <Notice tone={schedule.enabled ? "attention" : "neutral"}>
        {schedule.reason || "No backup is scheduled."}
      </Notice>
    );
  }
  return (
    <Notice tone="good">
      The next backup is {whenExact(schedule.next_run_at)}
      {" "}({relative(schedule.next_run_at)}).
      {schedule.reason && <> {schedule.reason}</>}
    </Notice>
  );
}

function ScheduleForm({ settings, readOnly, onSaved }: {
  settings: SettingsPayload;
  readOnly: boolean;
  onSaved: () => void;
}) {
  const notify = useNotify();
  const [draft, setDraft] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [problems, setProblems] = useState<string[]>([]);

  const fields = new Map<string, SettingField>();
  for (const group of settings.groups) {
    for (const f of group.fields) fields.set(f.key, f);
  }
  const field = (key: string): SettingField | undefined => fields.get(key);

  const value = (key: string): string => {
    if (key in draft) return draft[key]!;
    const stored = settings.values[key];
    return stored === undefined || stored === null ? "" : String(stored);
  };
  const set = (key: string, next: string) =>
    setDraft((d) => ({ ...d, [key]: next }));

  const on = value(ENABLED) === "true";
  const weekly = value(CADENCE) === "weekly";
  const zone = browserZone();
  const passphraseSet = settings.secrets_set[PASSPHRASE] ?? false;
  const dirty = Object.keys(draft).length > 0;

  async function save() {
    setBusy(true);
    setProblems([]);
    try {
      await api.saveSettings(draft);
      setDraft({});
      onSaved();
    } catch (e) {
      // A rejected value comes back as `problems` with no detail at all, so
      // falling through would show a code and name no field.
      if (e instanceof ApiError && e.problems?.length) setProblems(e.problems);
      else setProblems([problemText(e, "Couldn't save the schedule.")]);
      notify("problem", "Nothing was saved.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="space-y-5">
      {problems.map((p, i) => (
        <Notice tone="problem" key={i}>{p.replace(/^settings:\s*/, "")}</Notice>
      ))}

      <div className="flex items-start gap-3">
        <Switch
          id="sched-enabled" checked={on} disabled={readOnly}
          onCheckedChange={(v) => set(ENABLED, String(v))}
        />
        <div className="space-y-0.5">
          <Label htmlFor="sched-enabled">
            {field(ENABLED)?.label ?? "Back up on a schedule"}
          </Label>
          {field(ENABLED)?.help && (
            <p className="text-xs text-muted-foreground">{field(ENABLED)!.help}</p>
          )}
        </div>
      </div>

      {on && (
        <>
          <div className="grid gap-4 sm:grid-cols-3">
            <div className="space-y-1.5">
              <Label htmlFor="sched-cadence">
                {field(CADENCE)?.label ?? "How often"}
              </Label>
              <NativeSelect
                id="sched-cadence" value={value(CADENCE)} disabled={readOnly}
                onChange={(e) => set(CADENCE, e.target.value)}
              >
                {(field(CADENCE)?.options ?? ["daily", "weekly"]).map((o) => (
                  <option key={o} value={o}>
                    {field(CADENCE)?.option_labels?.[o] ?? o}
                  </option>
                ))}
              </NativeSelect>
            </div>

            {weekly && (
              <div className="space-y-1.5">
                <Label htmlFor="sched-weekday">
                  {field(WEEKDAY)?.label ?? "Day"}
                </Label>
                {/* Stored as 0 to 6, offered by name. "Sunday is 0" is a fact
                    about the column, not a question to put to an operator. */}
                <NativeSelect
                  id="sched-weekday" value={value(WEEKDAY) || "0"} disabled={readOnly}
                  onChange={(e) => set(WEEKDAY, e.target.value)}
                >
                  {WEEKDAYS.map((day, n) => (
                    <option key={day} value={String(n)}>{day}</option>
                  ))}
                </NativeSelect>
              </div>
            )}

            <div className="space-y-1.5">
              <Label htmlFor="sched-time">{field(TIME)?.label ?? "Time"}</Label>
              <Input
                id="sched-time" value={value(TIME)} disabled={readOnly}
                placeholder={field(TIME)?.placeholder ?? "04:00"}
                onChange={(e) => set(TIME, e.target.value)}
              />
            </div>
          </div>

          {field(TIME)?.help && (
            <p className="text-xs text-muted-foreground">{field(TIME)!.help}</p>
          )}

          <div className="space-y-1.5">
            <Label htmlFor="sched-zone">{field(TIMEZONE)?.label ?? "Time zone"}</Label>
            <Input
              id="sched-zone" value={value(TIMEZONE)} disabled={readOnly}
              placeholder={field(TIMEZONE)?.placeholder ?? "America/Chicago"}
              onChange={(e) => set(TIMEZONE, e.target.value)}
            />
            {field(TIMEZONE)?.help && (
              <p className="text-xs text-muted-foreground">{field(TIMEZONE)!.help}</p>
            )}
            {/* Offered rather than filled in. The stored zone is what makes the
                time mean the same thing all year and from whichever machine
                this page is open on; taking the browser's silently would tie
                the schedule to wherever it was last saved from. */}
            {zone && zone !== value(TIMEZONE) && !readOnly && (
              <Button
                type="button" variant="outline" size="sm"
                onClick={() => set(TIMEZONE, zone)}
              >
                Use this browser's zone, {zone}
              </Button>
            )}
          </div>
        </>
      )}

      <div className="space-y-1.5">
        <Label htmlFor="sched-passphrase">
          {field(PASSPHRASE)?.label ?? "Passphrase"}
        </Label>
        <Input
          id="sched-passphrase" type="password" autoComplete="new-password"
          disabled={readOnly} value={value(PASSPHRASE)}
          placeholder={passphraseSet ? "Saved — type to replace" : ""}
          onChange={(e) => set(PASSPHRASE, e.target.value)}
        />
        {field(PASSPHRASE)?.help && (
          <p className="text-xs text-muted-foreground">{field(PASSPHRASE)!.help}</p>
        )}
      </div>

      {!readOnly && (
        <div className="flex items-center gap-3 border-t pt-4">
          <Button disabled={!dirty || busy} onClick={() => void save()}>
            {busy ? "Saving…" : "Save changes"}
          </Button>
          {dirty ? (
            <Button variant="ghost" disabled={busy} onClick={() => setDraft({})}>
              Discard
            </Button>
          ) : (
            <span className="text-xs text-muted-foreground">Nothing to save.</span>
          )}
        </div>
      )}
    </div>
  );
}
