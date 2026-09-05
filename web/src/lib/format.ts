import type { AuditRecord, OperationState, RiskLevel } from "./api";

/** A timestamp, in the reader's locale, short enough to sit in a table cell. */
export function when(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString(undefined, {
    month: "short", day: "numeric", hour: "2-digit", minute: "2-digit",
  });
}

/** The same, with the year and seconds, for a detail page. */
export function whenExact(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString(undefined, {
    year: "numeric", month: "short", day: "numeric",
    hour: "2-digit", minute: "2-digit", second: "2-digit",
  });
}

/** How long until a moment, or how long since. */
export function relative(iso: string, now: number = Date.now()): string {
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return iso;
  const seconds = Math.round((t - now) / 1000);
  const abs = Math.abs(seconds);

  // Coarse to fine, so the first match is the largest unit that fits.
  const scales: [Intl.RelativeTimeFormatUnit, number][] = [
    ["year", 31_536_000],
    ["month", 2_592_000],
    ["week", 604_800],
    ["day", 86_400],
    ["hour", 3_600],
    ["minute", 60],
  ];
  const format = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });

  for (const [unit, size] of scales) {
    if (abs >= size) return format.format(Math.round(seconds / size), unit);
  }
  return format.format(seconds, "second");
}

/** Strips the machine prefix off an identity. */
export function who(actor: string): string {
  if (actor.startsWith("system:")) return "mcpd";
  return actor.replace(/^(user|svc):/, "");
}

/** Turns an audit event name into something readable. */
export function describeEvent(r: AuditRecord): string {
  const what = r.action ? r.action.replace(/[._]/g, " ") : "";
  switch (r.kind) {
    case "operation.proposed": return what ? `Suggested: ${what}` : "Suggested a change";
    case "operation.approved": return what ? `Approved: ${what}` : "Approved a change";
    case "operation.rejected": return "Turned down a change";
    case "operation.cancelled": return "Withdrew a change";
    case "operation.expired": return "A change ran out of time";
    case "operation.executing": return what ? `Applying: ${what}` : "Applying a change";
    case "operation.succeeded": return what ? `Applied: ${what}` : "Applied a change";
    case "operation.failed": return "A change didn't run";
    case "operation.indeterminate": return "A change ended up in an unknown state";
    default: return r.kind.replace(/[._]/g, " ");
  }
}

/**
 * What each state is called and what it means. `indeterminate` is not
 * `failed`: reading it as settled invites a retry that applies the change twice.
 */
export const OPERATION_STATES: Record<OperationState, { label: string; meaning: string }> = {
  draft: { label: "Draft", meaning: "Being put together. Not asking for anything yet." },
  pending_approval: { label: "Waiting", meaning: "Proposed, and waiting for somebody to decide." },
  approved: { label: "Approved", meaning: "Cleared to run. It has not run yet." },
  executing: { label: "Running", meaning: "Being applied now." },
  succeeded: { label: "Applied", meaning: "It ran, and the change is in place." },
  failed: { label: "Didn't run", meaning: "It did not run. Nothing changed." },
  indeterminate: {
    label: "Unknown",
    meaning:
      "It started, and nobody recorded what happened. The change may be in " +
      "place. Check the system before proposing it again, so it is not " +
      "applied twice.",
  },
  rejected: { label: "Turned down", meaning: "Somebody decided against it." },
  expired: { label: "Expired", meaning: "Nobody decided in time." },
  cancelled: { label: "Withdrawn", meaning: "Whoever proposed it took it back." },
};

export function stateLabel(state: OperationState | string): string {
  return OPERATION_STATES[state as OperationState]?.label ?? String(state);
}

export const RISK_LABELS: Record<RiskLevel, string> = {
  low: "Low", medium: "Medium", high: "High", critical: "Critical",
};

/** A risk level as a word. A level this build does not know renders as itself. */
export function riskLabel(risk: string): string {
  return RISK_LABELS[risk as RiskLevel] ?? risk;
}

/** Tool names carry their plugin prefix; a page about one plugin already says which. */
export function unprefixed(tool: string, plugin: string): string {
  return tool.startsWith(plugin + "_") ? tool.slice(plugin.length + 1) : tool;
}

/** Pretty-prints a decoded JSON value for display. */
export function pretty(value: unknown): string {
  if (value === undefined || value === null) return "";
  if (typeof value === "string") return value;
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

// Imported here rather than added to the file's first import, so this block
// stays one contiguous piece.
import type { Change } from "./api";

/* -- a change, as a sentence ------------------------------------------------
 *
 * Everything below turns one operation into words an ops manager reads
 * without decoding anything. `label.set` is a machine's name for a change;
 * nobody outside this repository knows what it means, and the page that shows
 * it is the page somebody approves from.
 *
 * The raw action still exists -- under the detail page's technical block,
 * beside the id and the payload, which is where evidence belongs.
 */

/** The parts of an operation these functions read. */
export interface ChangeLike {
  plugin: string;
  /** `resource.verb`, as `MutationSpec.Action` writes it. */
  action: string;
  impact?: string;
  changes?: Change[];
  state?: OperationState | string;
  verified?: boolean | null;
  terminal_at?: string;
  approved_at?: string;
  requested_at?: string;
}

/**
 * The verbs a mutation's action ends in, and the word a person would use.
 * Deliberately small and closed: an action ending in a verb nobody listed
 * reads as itself rather than as something invented for it.
 */
const CHANGE_VERBS: Record<string, string> = {
  set: "Set",
  update: "Change",
  change: "Change",
  create: "Create",
  add: "Add",
  delete: "Remove",
  remove: "Remove",
  enable: "Turn on",
  disable: "Turn off",
  reset: "Reset",
  rotate: "Rotate",
  revoke: "Revoke",
  assign: "Assign",
  rename: "Rename",
  move: "Move",
  restart: "Restart",
  reboot: "Restart",
  pause: "Pause",
  resume: "Resume",
  silence: "Silence",
  acknowledge: "Acknowledge",
  clear: "Clear",
};

/** Verbs that bring a thing into being take "a", not "the". */
const INDEFINITE = new Set(["create", "add"]);

/**
 * Resources whose segment name is not what the change acts on.
 *
 * `recycle_bin.destroy` destroys one item in the bin, not the bin; it is also
 * the one action bookstack declares irreversible and the one nobody should
 * ever be able to read as routine housekeeping.
 */
const RESOURCE_WORDS: Record<string, string> = {
  recycle_bin: "item in the recycle bin",
};

/** Machine casing to words: `radio_channel` and `radioChannel` both read out. */
function words(segment: string): string {
  return segment
    .replace(/([a-z0-9])([A-Z])/g, "$1 $2")
    .replace(/[_-]+/g, " ")
    .trim()
    .toLowerCase();
}

function capitalise(text: string): string {
  return text.charAt(0).toUpperCase() + text.slice(1);
}

/**
 * One field value, short enough to sit inside a sentence.
 *
 * A value that is not a scalar has no honest short form -- pasting `{…}` into
 * prose is the evidence-in-a-sentence bug -- so it returns null and the whole
 * "from x to y" clause is dropped rather than half-rendered. The field table
 * on the detail page still carries every value in full.
 */
export function fieldValue(value: unknown): string | null {
  if (value === null || value === undefined) return null;
  if (typeof value === "boolean") return value ? "on" : "off";
  if (typeof value === "number") return String(value);
  if (typeof value !== "string") return null;

  const text = value.trim();
  if (text === "") return null;
  const short = text.length > 60 ? `${text.slice(0, 59)}…` : text;
  // A value with a space in it runs into the sentence around it. Quoted, the
  // reader can see where the value starts and where it stops.
  return /\s/.test(short) ? `“${short}”` : short;
}

/** What `describeChange` works out, in the pieces a layout places separately. */
export interface ChangeWords {
  /** The action as a clause: "Set the label on echo". Never the raw action. */
  headline: string;
  /**
   * What differs: "from multi-window to narrower-window", or "to 44" where
   * nothing recorded how it was before. Falls back to the plugin's own impact
   * when no fields were recorded, and is "" when there is neither.
   */
  detail: string;
  /**
   * Both as one sentence, for a row, a title, or a label read aloud. Only the
   * field-derived detail joins it: the impact is a sentence the plugin wrote,
   * and running two together makes one long one that is neither.
   */
  sentence: string;
}

/**
 * A proposed change in one sentence.
 *
 * The verb is the action's last segment and the resource is everything before
 * it, which is the shape `MutationSpec.Action` guarantees -- `resource.verb`,
 * kept that way because the approval policy matches on it. So the words are
 * derivable, and the raw form never has to be put in front of a person.
 */
export function describeChange(op: ChangeLike): ChangeWords {
  const headline = changeHeadline(op);
  const delta = changeDelta(op);
  const text = delta ? deltaWords(delta) : "";
  const impact = op.impact?.trim() ?? "";

  return {
    headline,
    detail: text || impact,
    // An aside takes a comma; a continuation finishes the same clause and
    // does not.
    sentence: delta
      ? `${headline}${delta.join === "aside" ? "," : ""} ${text}.`
      : `${headline}.`,
  };
}

/** The verb an action ends in, and the resource it acts on, as words. */
function parseAction(action: string): { verb: string; resource: string } | null {
  const segments = action.split(".").map((s) => s.trim()).filter(Boolean);
  if (segments.length === 0) return null;

  const last = segments[segments.length - 1]!.toLowerCase();
  const before = segments.slice(0, -1);

  // A last segment that is several words carries its own resource:
  // `device.set_radio_channel` is "set the radio channel", not "set radio
  // channel the device". The earlier segments named the thing that owns it,
  // and the segment itself is more specific than they are.
  const parts = words(last).split(" ").filter(Boolean);
  if (parts.length > 1 && CHANGE_VERBS[parts[0]!]) {
    return { verb: parts[0]!, resource: parts.slice(1).join(" ") };
  }

  const resource = before
    .map((s) => RESOURCE_WORDS[s.toLowerCase()] ?? words(s))
    .filter(Boolean)
    .join(" ");
  return { verb: last, resource };
}

function changeHeadline(op: ChangeLike): string {
  const on = op.plugin ? ` on ${op.plugin}` : "";
  const parsed = parseAction(op.action);
  if (!parsed) return `A change${on}`;

  const verb = CHANGE_VERBS[parsed.verb] ?? capitalise(words(parsed.verb));

  // No resource before the verb: the plugin is the only noun there is, so the
  // verb takes it directly rather than an article with nothing after it.
  if (parsed.resource === "") return `${verb}${on}`;

  // "an user" is what a vowel test gets wrong, and "u" is the only letter it
  // gets wrong often enough to be worth naming.
  const article = INDEFINITE.has(parsed.verb)
    ? (/^[aeio]/.test(parsed.resource) ? "an" : "a")
    : "the";
  return `${verb} ${article} ${parsed.resource}${on}`;
}

/** Always quoted, for a value the sentence names rather than compares. */
function quoted(value: string): string {
  return /^“.*”$/.test(value) ? value : `“${value}”`;
}

/**
 * What differs, in parts, so a layout can weight the values and the sentence
 * can read them out. One function, because a card that says "to Getting
 * started" beside a heading that says "called “Getting started”" is two
 * renderings of one fact disagreeing in front of somebody.
 *
 * "aside" takes a comma before it; "continuation" finishes the same clause.
 */
export type ChangeDelta =
  | { kind: "between"; join: "aside"; from: string; to: string }
  | { kind: "to"; join: "continuation"; to: string }
  | { kind: "called"; join: "aside"; name: string };

export function changeDelta(op: ChangeLike): ChangeDelta | null {
  const first = op.changes?.[0];
  if (!first) return null;

  const to = fieldValue(first.to);
  if (to === null) return null;

  const parsed = parseAction(op.action);
  const from = fieldValue(first.from);

  // Turning a flag on and then saying "from off to on" is the verb again in
  // the machine's words. Nothing is added, and the line is longer for it.
  if (parsed && (parsed.verb === "enable" || parsed.verb === "disable") &&
      typeof first.to === "boolean") {
    return null;
  }

  // Something that did not exist has nothing to have changed from, so the
  // value is its name rather than the far end of a comparison.
  if (from === null && parsed && INDEFINITE.has(parsed.verb)) {
    return { kind: "called", join: "aside", name: quoted(to) };
  }

  return from === null
    ? { kind: "to", join: "continuation", to }
    : { kind: "between", join: "aside", from, to };
}

/** The same delta as running text, for a sentence or a one-line row. */
export function deltaWords(delta: ChangeDelta): string {
  switch (delta.kind) {
    case "between": return `from ${delta.from} to ${delta.to}`;
    case "to": return `to ${delta.to}`;
    case "called": return `called ${delta.name}`;
  }
}

/**
 * A principal as a name, with nothing machine-shaped left in it.
 *
 * `system:policy` is not an account and must never read as one: a rule
 * approving a change is not somebody having said yes. A key reads as "a key"
 * here, because its name is a lookup this function does not make -- the
 * `Principal` component makes it where the session is allowed to.
 */
export function principalWords(actor: string): string {
  if (actor === "system:policy") return "a standing rule";
  if (actor.startsWith("system:")) return "mcpd";

  if (actor.startsWith("svc:")) {
    const [service, ...rest] = actor.slice(4).split(":");
    const name = service === "chatgpt" ? "ChatGPT" : capitalise(words(service ?? ""));
    const workspace = rest.filter(Boolean).join(" ");
    return workspace ? `${name} (${workspace})` : name;
  }
  if (actor.startsWith("key:")) return "a key";
  if (actor.startsWith("user:")) {
    const email = actor.slice(5);
    return email.split("@")[0] || email;
  }
  return actor;
}

/**
 * What became of a change, as a fragment for a list row: "applied 5 minutes
 * ago". `indeterminate` never reads as a failure, here as everywhere.
 */
export function describeOutcome(op: ChangeLike, now: number = Date.now()): string {
  const at = op.terminal_at ?? op.approved_at ?? op.requested_at;
  const ago = at ? ` ${relative(at, now)}` : "";
  switch (op.state) {
    case "succeeded": return `applied${ago}`;
    case "failed": return `didn't run${ago}`;
    // Never a time beside it: "an hour ago" reads as a settlement, and this is
    // the one outcome that is still open.
    case "indeterminate": return "ended in an unknown state; it may have landed";
    case "rejected": return `turned down${ago}`;
    case "expired": return `ran out of time${ago}`;
    case "cancelled": return `withdrawn${ago}`;
    case "approved": return "approved, not run yet";
    case "executing": return "running now";
    case "draft": return "not proposed yet";
    case "pending_approval": return `proposed${ago}`;
    default: return String(op.state ?? "");
  }
}

/**
 * The three values of `verified` as three different words. Absent is "not
 * checked", and must never render as the word for a confirmed outcome.
 */
export function confirmationWord(verified?: boolean | null): string {
  if (verified === true) return "confirmed";
  if (verified === false) return "did not match";
  return "not checked";
}
