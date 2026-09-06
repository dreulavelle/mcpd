import type {
  BackupDestination, BackupKind, BackupPolicy, BackupRunStatus,
} from "./api";
import type { Tone } from "@/components/status";

/**
 * The words the Backup page uses, in one place.
 *
 * Kept out of the components because every one of them is a string somebody
 * reads and a test asserts, and because the same sentence is needed in a list
 * row, a form and a history table. docs/dashboard-copy.md is the rule they
 * follow: what happened, then what to do, and no evidence in a sentence.
 */

/** What each kind is called in the picker, in the words docs/backup.md uses. */
export const KIND_LABELS: Record<BackupKind, string> = {
  local: "A folder on this machine",
  sftp: "SFTP",
  s3: "S3",
  webdav: "WebDAV",
};

/** The same, short enough for a chip beside a name. */
export const KIND_SHORT: Record<BackupKind, string> = {
  local: "Folder",
  sftp: "SFTP",
  s3: "S3",
  webdav: "WebDAV",
};

/** A kind this build does not know renders as itself, which is the drift signal. */
export function kindLabel(kind: BackupKind | string): string {
  return KIND_LABELS[kind as BackupKind] ?? kind;
}

export function kindShort(kind: BackupKind | string): string {
  return KIND_SHORT[kind as BackupKind] ?? kind;
}

/** What a new destination starts with: the last six, and nothing clever. */
export const DEFAULT_POLICY: BackupPolicy = {
  keep_last: 6, keep_daily: 0, keep_weekly: 0, keep_monthly: 0,
};

/**
 * How much this destination keeps, as a sentence.
 *
 * The three period rules are named only when they are on. A summary reading
 * "and the newest in each of the last 0 days" is a rule that does nothing,
 * described as though it did something.
 */
export function retentionWords(policy: BackupPolicy): string {
  const last = `Keeps the last ${policy.keep_last}`;
  const periods: string[] = [];
  if (policy.keep_daily > 0) periods.push(plural(policy.keep_daily, "day"));
  if (policy.keep_weekly > 0) periods.push(plural(policy.keep_weekly, "week"));
  if (policy.keep_monthly > 0) periods.push(plural(policy.keep_monthly, "month"));
  if (periods.length === 0) return `${last}.`;
  return `${last}, and the newest in each of the last ${list(periods)}.`;
}

function plural(n: number, unit: string): string {
  return `${n} ${unit}${n === 1 ? "" : "s"}`;
}

function list(parts: string[]): string {
  if (parts.length === 1) return parts[0]!;
  return `${parts.slice(0, -1).join(", ")} and ${parts[parts.length - 1]}`;
}

/**
 * What a run's status says and how it is coloured.
 *
 * `interrupted` is "attention" and never "problem", for the reason the host
 * refuses to call it a failure: mcpd stopped while the run was going, so a
 * write may have landed, and painting it as a failure invites a second run
 * presented as a first.
 */
export const RUN_STATUSES: Record<BackupRunStatus, {
  label: string; tone: Tone; meaning: string;
}> = {
  running: {
    label: "Running", tone: "info",
    meaning: "Under way now.",
  },
  ok: {
    label: "Sent everywhere", tone: "good",
    meaning: "Every destination took this backup.",
  },
  partial: {
    label: "Sent to some", tone: "attention",
    meaning: "Some destinations took this backup and some did not. There is a "
      + "backup; it is not everywhere it should be.",
  },
  failed: {
    label: "Not sent", tone: "problem",
    meaning: "No destination took this backup.",
  },
  interrupted: {
    label: "Interrupted", tone: "attention",
    meaning: "mcpd stopped while this run was going. Some destinations may "
      + "hold this backup and some may not.",
  },
};

export function runLabel(status: BackupRunStatus | string): string {
  return RUN_STATUSES[status as BackupRunStatus]?.label ?? status;
}

export function runTone(status: BackupRunStatus | string): Tone {
  return RUN_STATUSES[status as BackupRunStatus]?.tone ?? "neutral";
}

export function runMeaning(status: BackupRunStatus | string): string {
  return RUN_STATUSES[status as BackupRunStatus]?.meaning ?? "";
}

/** What started a run, in words. */
export function triggerWords(trigger: string): string {
  return trigger === "manual" ? "Asked for" : "On the schedule";
}

/**
 * A size in the coarsest unit that still says something. Empty for nothing, so
 * a run still going does not read as a backup of zero bytes.
 */
export function sizeWords(bytes: number): string {
  if (!bytes || bytes <= 0) return "";
  const units = ["bytes", "KB", "MB", "GB", "TB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  if (unit === 0) return `${Math.round(value)} bytes`;
  return `${value < 10 ? value.toFixed(1) : Math.round(value)} ${units[unit]}`;
}

/** Sunday is 0, which is what the host stores. */
export const WEEKDAYS = [
  "Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday",
];

export function weekdayName(day: number | string): string {
  return WEEKDAYS[Number(day)] ?? String(day);
}

/**
 * The zone this browser is in, offered beside the stored one.
 *
 * Offered, never applied. The host stores a zone rather than reading the
 * machine's, so that the time means the same thing all year and on whichever
 * machine this page happens to be open; filling it in silently would make the
 * schedule depend on where somebody last saved it from.
 */
export function browserZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone ?? "";
  } catch {
    // A browser with no zone database. Nothing to offer, and nothing to say.
    return "";
  }
}

/**
 * The last attempt at one destination, as a sentence.
 *
 * Three states, not two. Never having run is not a failure, and collapsing the
 * two would have a destination added a minute ago read as broken.
 */
export function lastRunWords(d: BackupDestination): { tone: Tone; words: string } {
  if (d.last_ok === undefined || d.last_ok === null) {
    return { tone: "neutral", words: "No backup has been sent here yet." };
  }
  if (d.last_ok) return { tone: "good", words: "The last backup was sent here." };
  return {
    tone: "problem",
    words: d.last_error || "The last backup did not reach this destination.",
  };
}
