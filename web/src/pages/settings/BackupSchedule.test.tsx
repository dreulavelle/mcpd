import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
  api, type BackupSchedule as ScheduleStatus, type SettingsPayload,
} from "@/lib/api";
import { renderWith, sessionFor } from "@/test/render";
import { BackupSchedule } from "./BackupSchedule";

/**
 * The catalog as the host sends it, trimmed to the group this form renders.
 * The labels and help text below are read off it rather than written into the
 * component, so a fixture that lied about them would be a test that passes on
 * a page nobody could read.
 */
function settings(values: Record<string, unknown> = {}, secretsSet = false): SettingsPayload {
  return {
    groups: [{
      name: "backup",
      title: "Scheduled backups",
      section: "backup",
      enabled_by: "backup.schedule.enabled",
      help: "mcpd takes one encrypted archive and sends it to every destination that is switched on.",
      fields: [
        {
          key: "backup.schedule.enabled", label: "Back up on a schedule",
          kind: "bool", group: "backup", apply: "live", default: false,
          help: "Off until you turn it on.",
        },
        {
          key: "backup.schedule.cadence", label: "How often",
          kind: "enum", group: "backup", apply: "live", default: "weekly",
          options: ["daily", "weekly"],
          option_labels: { daily: "Every day", weekly: "Every week" },
        },
        {
          key: "backup.schedule.weekday", label: "Day",
          kind: "int", group: "backup", apply: "live", default: 0,
          min: 0, max: 6, help: "Sunday is 0.",
          show_when: { field: "backup.schedule.cadence", equals: ["weekly"] },
        },
        {
          key: "backup.schedule.time", label: "Time",
          kind: "string", group: "backup", apply: "live",
          default: "04:00", placeholder: "04:00",
          help: "As HH:MM, in the time zone below.",
        },
        {
          key: "backup.schedule.timezone", label: "Time zone",
          kind: "string", group: "backup", apply: "live",
          default: "UTC", placeholder: "America/Chicago",
          help: "A zone name, such as Europe/London or America/Chicago.",
        },
        {
          key: "backup.passphrase", label: "Passphrase",
          kind: "secret", group: "backup", apply: "live",
          help: "Write this down and keep it with the backup. It is stored here "
            + "so a scheduled backup can run when nobody is present, and it will "
            + "not be shown again. A host that has lost its database cannot tell "
            + "you what it was.",
        },
      ],
    }],
    values: {
      "backup.schedule.enabled": true,
      "backup.schedule.cadence": "weekly",
      "backup.schedule.weekday": 0,
      "backup.schedule.time": "04:00",
      "backup.schedule.timezone": "UTC",
      ...values,
    },
    secrets_set: { "backup.passphrase": secretsSet },
    encryption_available: true,
    bootstrap: [],
  };
}

function schedule(overrides: Partial<ScheduleStatus> = {}): ScheduleStatus {
  return {
    enabled: true,
    cadence: "weekly",
    weekday: 0,
    time: "04:00",
    timezone: "UTC",
    next_run_at: "2026-09-06T04:00:00Z",
    destinations: 1,
    enabled_destinations: 1,
    passphrase_set: true,
    running: false,
    ...overrides,
  };
}

function stub(payload: SettingsPayload) {
  vi.spyOn(api, "settings").mockResolvedValue(payload);
}

describe("the backup schedule", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    window.localStorage.clear();
    window.sessionStorage.clear();
  });

  it("says when the next backup is", async () => {
    stub(settings());
    renderWith(<BackupSchedule schedule={schedule()} onSaved={() => {}} />);
    expect(await screen.findByText(/The next backup is/)).toBeInTheDocument();
  });

  /**
   * A schedule switched on with no destination, or with no passphrase, is a
   * page that looks configured and takes no backups. The host answers both in
   * one field and the page shows it instead of a time.
   */
  it("says why there will not be one, rather than showing nothing", async () => {
    stub(settings());
    renderWith(
      <BackupSchedule
        schedule={schedule({
          next_run_at: null,
          reason: "No destination is switched on, so a scheduled backup has nowhere to go.",
        })}
        onSaved={() => {}}
      />,
    );
    expect(await screen.findByText(/nowhere to go/)).toBeInTheDocument();
    expect(screen.queryByText(/The next backup is/)).not.toBeInTheDocument();
  });

  /**
   * The passphrase is the one thing that cannot be recovered, so the sentence
   * warning about that is the catalog's own and is rendered as written. A form
   * that paraphrased it would be a second copy to keep in step.
   */
  it("carries the passphrase warning the host wrote", async () => {
    stub(settings());
    renderWith(<BackupSchedule schedule={schedule()} onSaved={() => {}} />);
    expect(await screen.findByText(
      /A host that has lost its database cannot tell you what it was\./,
    )).toBeInTheDocument();
  });

  it("says a stored passphrase is there without offering to show it", async () => {
    stub(settings({}, true));
    renderWith(<BackupSchedule schedule={schedule()} onSaved={() => {}} />);

    const box = await screen.findByLabelText("Passphrase");
    expect(box).toHaveAttribute("type", "password");
    expect(box).toHaveAttribute("placeholder", "Saved — type to replace");
    expect(box).toHaveValue("");
  });

  /**
   * Sunday is 0 is a fact about the column, not a question to put to somebody
   * choosing a day. The names are offered and the stored number is what gets
   * written.
   */
  it("offers weekdays by name and writes the number the host stores", async () => {
    stub(settings());
    const save = vi.spyOn(api, "saveSettings").mockResolvedValue({ applied: [] });
    renderWith(<BackupSchedule schedule={schedule()} onSaved={() => {}} />);
    const user = userEvent.setup();

    await user.selectOptions(await screen.findByLabelText("Day"), "3");
    expect(screen.getByRole("option", { name: "Wednesday" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Save changes" }));

    expect(save).toHaveBeenCalledWith({ "backup.schedule.weekday": "3" });
  });

  // Every one of these is an ordinary setting, so the form writes the catalog's
  // own keys and nothing else. Only what changed is sent.
  it("writes the settings keys the catalog names", async () => {
    stub(settings());
    const save = vi.spyOn(api, "saveSettings").mockResolvedValue({ applied: [] });
    renderWith(<BackupSchedule schedule={schedule()} onSaved={() => {}} />);
    const user = userEvent.setup();

    await user.selectOptions(await screen.findByLabelText("How often"), "daily");
    const time = screen.getByLabelText("Time");
    await user.clear(time);
    await user.type(time, "04:30");
    const zone = screen.getByLabelText("Time zone");
    await user.clear(zone);
    await user.type(zone, "Europe/London");
    await user.type(screen.getByLabelText("Passphrase"), "a-long-enough-passphrase");
    await user.click(screen.getByRole("button", { name: "Save changes" }));

    expect(save).toHaveBeenCalledWith({
      "backup.schedule.cadence": "daily",
      "backup.schedule.time": "04:30",
      "backup.schedule.timezone": "Europe/London",
      "backup.passphrase": "a-long-enough-passphrase",
    });
  });

  // A weekday means nothing on a daily schedule, and it is the catalog that
  // says so -- the field carries the condition, and the form reads it rather
  // than writing "weekly" down a second time.
  it("does not ask for a day when the cadence is daily", async () => {
    stub(settings({ "backup.schedule.cadence": "daily" }));
    renderWith(<BackupSchedule schedule={schedule()} onSaved={() => {}} />);

    expect(await screen.findByLabelText("How often")).toBeInTheDocument();
    expect(screen.queryByLabelText("Day")).not.toBeInTheDocument();
  });

  /**
   * The zone is offered, never applied. The host stores one so the time means
   * the same thing all year and from whichever machine this page is open on;
   * filling in the browser's silently would tie the schedule to wherever
   * somebody last saved it from.
   */
  it("offers this browser's zone without taking it", async () => {
    // Pinned rather than read from wherever the tests happen to run: the offer
    // exists only when the browser's zone differs from the stored one, so a
    // machine set to UTC would silently assert nothing.
    vi.spyOn(Intl.DateTimeFormat.prototype, "resolvedOptions").mockReturnValue(
      { ...new Intl.DateTimeFormat().resolvedOptions(), timeZone: "Europe/London" },
    );
    stub(settings());
    const save = vi.spyOn(api, "saveSettings").mockResolvedValue({ applied: [] });
    renderWith(<BackupSchedule schedule={schedule()} onSaved={() => {}} />);
    const user = userEvent.setup();

    const offer = await screen.findByRole(
      "button", { name: "Use this browser's zone, Europe/London" });
    // Offered beside the stored zone, not written into it.
    expect(screen.getByLabelText("Time zone")).toHaveValue("UTC");

    await user.click(offer);
    expect(screen.getByLabelText("Time zone")).toHaveValue("Europe/London");
    await user.click(screen.getByRole("button", { name: "Save changes" }));

    expect(save).toHaveBeenCalledWith({ "backup.schedule.timezone": "Europe/London" });
  });

  // Nothing is asked for while the schedule is off: it would be a form of
  // settings that change nothing.
  it("asks for nothing but the switch while the schedule is off", async () => {
    stub(settings({ "backup.schedule.enabled": false }));
    renderWith(<BackupSchedule schedule={schedule({ enabled: false })} onSaved={() => {}} />);

    expect(await screen.findByLabelText("Back up on a schedule")).toBeInTheDocument();
    expect(screen.queryByLabelText("How often")).not.toBeInTheDocument();
  });

  it("shows no way to save to somebody who cannot change this host", async () => {
    stub(settings());
    renderWith(
      <BackupSchedule schedule={schedule()} onSaved={() => {}} />,
      { session: sessionFor("user") },
    );

    expect(await screen.findByLabelText("Time")).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Save changes" })).not.toBeInTheDocument();
  });
});
