import type { AuditRecord, OperationState, RiskLevel } from "./api";
import { BUILTIN_ROLES } from "./permissions";

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

/**
 * An audit entry as one sentence.
 *
 * Kept for the places that want a single string -- the Overview's last few
 * lines, and a search over what is on screen. The Audit page builds the same
 * sentence out of `auditWords` instead, so that the systems and the keys named
 * in it can be links.
 */
export function describeEvent(r: AuditRecord): string {
  const text = phraseText(auditWords(r).phrase);
  return text.charAt(0).toUpperCase() + text.slice(1);
}

/* -- the audit trail, in words --------------------------------------------- */

/**
 * A run of an audit sentence. A phrase with a destination is the one thing in
 * the sentence a reader can act on -- the system, the key, the change itself.
 */
export type Phrase = string | { text: string; to: string };

/** The status tones, spelled here so this module stays free of components. */
export type EventTone = "good" | "attention" | "problem" | "info" | "neutral";

/** What an entry is about, which is what the page filters on. */
export type EventCategory =
  | "changes" | "access" | "systems" | "windows" | "housekeeping";

export const EVENT_CATEGORIES: Record<EventCategory, string> = {
  changes: "Changes",
  access: "Who may reach what",
  systems: "Systems",
  windows: "Approval windows",
  housekeeping: "Housekeeping",
};

/**
 * Names for the identifiers an entry carries.
 *
 * The trail stores identifiers because a name can be changed and an entry
 * cannot, so the identifier is the honest thing to record. It is not the
 * honest thing to *read*, and a reader who has to decode `key_993f…` before
 * they know what the line says is a reader who stops reading. Both maps are
 * optional: a principal who may not list accounts or keys still gets a
 * sentence, one that says "a key" rather than naming it.
 */
export interface NameBook {
  /** Keyed by account id and by email alike, because entries carry both. */
  users?: Record<string, string>;
  keys?: Record<string, string>;
  /** Custom roles by id; the built-in ones are known without asking. */
  roles?: Record<string, string>;
}

export interface ActorWords {
  /** What to call them at the head of a sentence. */
  word: string;
  kind: "person" | "key" | "service" | "system";
  /** What the mark beside the sentence is drawn from. */
  mark: string;
  /** mcpd acting on a schedule rather than on anybody's instruction. */
  housekeeping: boolean;
}

/**
 * Who acted, in words.
 *
 * `system:policy` is "a standing rule" rather than "mcpd" on purpose: an
 * auto-approved change was decided by something an operator wrote, and reading
 * it as the host's own doing hides the rule that needs revisiting.
 */
export function describeActor(actor: string, book: NameBook = {}): ActorWords {
  const [prefix, ...rest] = actor.split(":");
  const tail = rest.join(":");

  switch (prefix) {
    case "system":
      switch (tail) {
        case "policy":
          return { word: "a standing rule", kind: "system", mark: "policy", housekeeping: false };
        case "registration":
          return { word: "a sign-up default", kind: "system", mark: "signup", housekeeping: false };
        case "retention":
          return { word: "mcpd", kind: "system", mark: "mcpd", housekeeping: true };
        default:
          // The executor and the reaper are both mcpd applying or closing
          // something nobody is waiting on; neither is housekeeping, because
          // each is a step of a change somebody proposed.
          return { word: "mcpd", kind: "system", mark: "mcpd", housekeeping: false };
      }
    case "user":
    case "self": {
      const name = book.users?.[tail];
      return {
        word: name ?? localPart(tail),
        kind: "person",
        mark: name ?? tail,
        housekeeping: false,
      };
    }
    case "key": {
      const name = book.keys?.[tail];
      return {
        word: name ? `the key ${name}` : "a key",
        kind: "key",
        mark: name ?? tail,
        housekeeping: false,
      };
    }
    case "svc": {
      const [service, ...where] = tail.split(":");
      const workspace = where.join(":");
      const named = service === "chatgpt" ? "ChatGPT" : service ?? tail;
      return {
        word: workspace ? `${named} (${workspace})` : named,
        kind: "service",
        mark: tail,
        housekeeping: false,
      };
    }
    default:
      return { word: actor, kind: "system", mark: actor, housekeeping: false };
  }
}

/** The part of an address before the at sign, which is what people call it. */
function localPart(address: string): string {
  const at = address.indexOf("@");
  return at > 0 ? address.slice(0, at) : address;
}

export interface EventWords {
  /** What the actor did, in past simple. Never carries an identifier. */
  phrase: Phrase[];
  /** Short fragments under it: a reason, a reach, an expiry, a count. */
  facts: string[];
}

/**
 * One audit entry as words.
 *
 * Every sentence is `{who} {did what}` in past simple, and no sentence carries
 * an identifier, a JSON fragment or an error code -- those live in the raw
 * entry, which is where somebody goes when the sentence was not enough. A kind
 * this build has no words for still reads as a sentence rather than as a blank
 * row, because the trail outliving the dashboard that renders it is the normal
 * case after an upgrade.
 */
export function auditWords(r: AuditRecord, book: NameBook = {}): EventWords {
  const d = detailOf(r);
  const subject = r.plugin ?? "";
  const facts: string[] = [];

  /** The system an entry is about, linked to its own page. */
  const system = (): Phrase[] =>
    subject ? [" on ", { text: subject, to: `/plugins/${encodeURIComponent(subject)}` }] : [];

  /**
   * What was asked for, as an infinitive: `label.set` is "set the label".
   *
   * Every sentence below puts it after "to" or after a modal, so no verb is
   * ever conjugated. English inflection off an arbitrary plugin's verb is a
   * guess -- "set" doubles its consonant and "visit" does not -- and a guess
   * that is wrong reads as a typo on the line somebody approves a change to
   * live infrastructure from.
   */
  const act = actionWords(r.action);
  const change = (): Phrase[] => {
    const to = operationPath(r);
    const words = act ?? "make a change";
    return [to ? { text: words, to } : words, ...system()];
  };

  switch (r.kind) {
    /* -- changes ----------------------------------------------------------- */
    case "operation.proposed": {
      const impact = str(d, "impact");
      if (impact) facts.push(quoted(impact));
      if (bool(d, "reversible") === false) facts.push("cannot be undone");
      if (str(d, "assurance") === "gated_call") {
        // The record proves an authorisation and nothing else. Saying so is
        // the whole reason the two words are kept apart.
        facts.push("an authorisation and nothing more");
      }
      return { phrase: ["asked to ", ...change()], facts };
    }
    case "operation.approved": {
      pushReason(facts, d);
      const by = authorityWords(d, book);
      if (by) facts.push(by);
      return { phrase: ["approved a request to ", ...change()], facts };
    }
    case "operation.rejected":
      pushReason(facts, d);
      return { phrase: ["turned down a request to ", ...change()], facts };
    case "operation.cancelled":
      pushReason(facts, d);
      return { phrase: ["withdrew a request to ", ...change()], facts };
    case "operation.expired":
      return {
        phrase: ["let a request to ", ...change(), " run out of time"],
        facts,
      };
    case "operation.executing":
      pushDrift(facts, d);
      return { phrase: ["started to ", ...change()], facts };
    case "operation.succeeded":
      facts.push(verifiedWords(bool(d, "verified"), subject));
      return { phrase: ["applied the change to ", ...change()], facts };
    case "operation.failed":
      if (str(d, "detail")) facts.push(quoted(str(d, "detail")));
      return { phrase: ["could not ", ...change()], facts };
    case "operation.indeterminate":
      facts.push(verifiedWords(bool(d, "verified"), subject));
      return {
        phrase: ["started to ", ...change(), " and could not tell whether it landed"],
        facts,
      };

    /* -- who may reach what ------------------------------------------------ */
    case "account.registered": {
      const status = str(d, "status");
      if (status === "pending") facts.push("waiting for an administrator");
      const provider = str(d, "provider");
      if (provider && provider !== "password") facts.push(`through ${provider}`);
      return { phrase: ["signed up"], facts };
    }
    case "account.approved":
      pushGroups(facts, d, "joined");
      return { phrase: ["let ", person(subject, book), " in"], facts };
    case "account.rejected":
      return { phrase: ["turned down ", person(subject, book), "'s sign-up"], facts };
    case "account.identity_linked":
      return {
        phrase: [
          `linked a ${str(d, "provider") || "provider"} sign-in to `,
          person(subject, book),
        ],
        facts,
      };
    case "account.identity_unlinked":
      // The subject here is an account id rather than an address, so the
      // account is named from the book or not at all.
      return {
        phrase: [
          `unlinked a ${str(d, "provider") || "provider"} sign-in from `,
          book.users?.[subject] ?? "an account",
        ],
        facts,
      };

    case "apikey.created":
      pushRole(facts, d, book);
      pushReach(facts, d);
      pushExpiry(facts, d);
      return { phrase: ["created ", key(subject, d, book)], facts };
    case "apikey.rescoped":
      pushRole(facts, d, book);
      pushReach(facts, d);
      pushExpiry(facts, d);
      if (str(d, "name_before")) facts.push(`was called ${str(d, "name_before")}`);
      return { phrase: ["changed what ", key(subject, d, book), " may do"], facts };
    case "apikey.rotated": {
      const grace = num(d, "grace_seconds");
      if (grace !== null) {
        facts.push(grace > 0
          ? `the old secret works for another ${duration(grace)}`
          : "the old secret stopped working at once");
      }
      return { phrase: ["gave ", key(subject, d, book), " a new secret"], facts };
    }
    case "apikey.revoked":
      return { phrase: ["revoked ", key(subject, d, book)], facts };

    case "role.created":
      return { phrase: ["created ", role(subject)], facts };
    case "role.updated": {
      const renamed = str(d, "renamed_from");
      if (d["permissions"] !== undefined) {
        if (renamed) facts.push(`was called ${renamed}`);
        return { phrase: ["changed what ", role(subject), " may do"], facts };
      }
      if (renamed) return { phrase: [`renamed the role ${renamed} to `, role(subject)], facts };
      return { phrase: ["changed ", role(subject)], facts };
    }
    case "role.deleted":
      return { phrase: ["deleted ", role(subject)], facts };

    case "group.created":
      pushRole(facts, d, book);
      pushReach(facts, d);
      return { phrase: ["created ", group(subject)], facts };
    case "group.updated": {
      const renamed = str(d, "renamed_from");
      pushRole(facts, d, book);
      pushReach(facts, d);
      if (d["role"] !== undefined || d["grants"] !== undefined) {
        if (renamed) facts.push(`was called ${renamed}`);
        return { phrase: ["changed what ", group(subject), " hands out"], facts };
      }
      if (renamed) return { phrase: [`renamed the group ${renamed} to `, group(subject)], facts };
      return { phrase: ["changed ", group(subject)], facts };
    }
    case "group.deleted": {
      const members = num(d, "members");
      if (members) {
        facts.push(`${members} ${members === 1 ? "member" : "members"} lost what it handed out`);
      }
      return { phrase: ["deleted ", group(subject)], facts };
    }
    case "group.member_added":
      return { phrase: ["added ", member(d, book), " to ", group(subject)], facts };
    case "group.member_removed":
      return { phrase: ["removed ", member(d, book), " from ", group(subject)], facts };

    /* -- systems ----------------------------------------------------------- */
    case "certificate.added": {
      const expires = str(d, "expires_at");
      if (expires) facts.push(`expires ${day(expires)}`);
      return { phrase: ["trusted the certificate ", certificate(subject)], facts };
    }
    case "certificate.removed":
      return { phrase: ["stopped trusting the certificate ", certificate(subject)], facts };

    case "mcpserver.imported": {
      const transport = str(d, "transport");
      if (transport) facts.push(`over ${transport}`);
      return { phrase: ["added the remote server ", ...serverName(subject)], facts };
    }
    case "mcpserver.removed": {
      const allowed = num(d, "tools_enabled");
      if (allowed) facts.push(`${allowed} ${allowed === 1 ? "tool was" : "tools were"} allowed`);
      return { phrase: [`removed the remote server ${subject}`], facts };
    }
    case "mcpserver.enabled":
      return { phrase: ["switched on the remote server ", ...serverName(subject)], facts };
    case "mcpserver.disabled":
      return { phrase: ["switched off the remote server ", ...serverName(subject)], facts };
    case "mcpserver.header_added":
      // The header's name is evidence, so it stays in the raw entry.
      if (bool(d, "secret")) facts.push("a secret one");
      return { phrase: ["added a header to ", ...serverName(subject)], facts };
    case "mcpserver.header_removed":
      return { phrase: ["removed a header from ", ...serverName(subject)], facts };
    case "mcpserver.discovered": {
      const added = list(d, "added").length;
      const changed = list(d, "changed").length;
      const gone = list(d, "removed").length;
      if (added) facts.push(`${added} new`);
      if (changed) facts.push(`${changed} changed`);
      if (gone) facts.push(`${gone} gone`);
      if (!added && !changed && !gone) facts.push("nothing had changed");
      return { phrase: ["read the tools ", ...serverName(subject), " offers"], facts };
    }
    case "mcpserver.tool_classified": {
      const tool = str(d, "tool") || "a tool";
      const to = str(d, "to");
      const verb = to === "enabled" ? "allowed "
        : to === "disabled" ? "stopped "
          : to === "pending" ? "put back for review "
            : "decided on ";
      return { phrase: [verb, tool, " on ", ...serverName(subject)], facts };
    }

    case "plugin.removed":
      return { phrase: ["removed the plugin ", ...serverName(subject)], facts };
    case "plugin.restored":
      return { phrase: ["restored the plugin ", ...serverName(subject)], facts };
    case "plugin.enabled":
      return { phrase: ["switched on the plugin ", ...serverName(subject)], facts };
    case "plugin.disabled":
      return { phrase: ["switched off the plugin ", ...serverName(subject)], facts };

    case "chatgpt.account.added":
      pushRole(facts, d, book);
      pushReach(facts, d);
      return { phrase: ["added ", chatgpt(d)], facts };
    case "chatgpt.account.updated":
      if (str(d, "api_key")) facts.push("a new key");
      if (str(d, "admin_key") === "cleared") facts.push("admin key cleared");
      else if (str(d, "admin_key")) facts.push("a new admin key");
      pushRole(facts, d, book);
      pushReach(facts, d);
      if (bool(d, "enabled") === false) facts.push("switched off");
      else if (bool(d, "enabled") === true) facts.push("switched on");
      return { phrase: ["changed ", chatgpt(d)], facts };
    case "chatgpt.account.removed":
      return { phrase: ["removed ", chatgpt(d)], facts };

    /* -- approval windows -------------------------------------------------- */
    case "approval.bypass.opened": {
      const minutes = num(d, "minutes");
      const scope = str(d, "plugin");
      facts.push(scope ? `${scope} only` : "every system");
      const ceiling = str(d, "ceiling");
      if (ceiling) facts.push(`up to ${riskLabel(ceiling).toLowerCase()} risk`);
      if (str(d, "reason")) facts.push(quoted(str(d, "reason")));
      const closes = str(d, "expires_at");
      if (closes) facts.push(`closes ${moment(closes, r.at)}`);
      return {
        phrase: [
          minutes
            ? `opened a ${minutes}-minute approval window`
            : "opened an approval window",
        ],
        facts,
      };
    }
    case "approval.bypass.revoked": {
      const closed = num(d, "closed") ?? 0;
      return {
        phrase: [closed === 1
          ? "closed the open approval window"
          : `closed ${closed} open approval windows`],
        facts,
      };
    }

    /* -- housekeeping ------------------------------------------------------ */
    case "audit.pruned": {
      const removed = num(d, "removed_entries") ?? 0;
      const older = str(d, "older_than");
      return {
        phrase: [
          `removed ${removed} ${removed === 1 ? "entry" : "entries"}` +
          (older ? ` older than ${day(older)}` : " from this record"),
        ],
        facts,
      };
    }

    default:
      // A kind this build has never heard of. Saying so plainly beats
      // guessing, and beats a blank line in a record whose whole value is
      // that nothing is missing from it.
      return { phrase: [`recorded ${r.kind.replace(/[._]/g, " ")}`], facts };
  }
}

/** What an entry is about. Anything unrecognised is a system's doing. */
export function auditCategory(r: AuditRecord): EventCategory {
  if (r.kind.startsWith("operation.")) return "changes";
  if (r.kind.startsWith("approval.bypass.")) return "windows";
  if (r.kind.startsWith("audit.")) return "housekeeping";
  if (/^(account|apikey|role|group)\./.test(r.kind)) return "access";
  return "systems";
}

/**
 * How an entry should read at a glance.
 *
 * `indeterminate` is attention and never problem, here as everywhere: the
 * change may be in place, and painting it as a failure invites a retry that
 * applies it twice. An opened approval window is attention because it is the
 * state somebody has to remember to leave.
 */
export function auditTone(r: AuditRecord): EventTone {
  switch (r.kind) {
    case "operation.succeeded":
    case "operation.approved":
      return "good";
    case "operation.failed":
      return "problem";
    case "operation.indeterminate":
    case "approval.bypass.opened":
      return "attention";
    case "operation.executing":
    case "operation.proposed":
      return "info";
    default:
      return "neutral";
  }
}

/** An audit sentence as plain text, for a search and for `describeEvent`. */
export function phraseText(phrase: Phrase[]): string {
  return phrase.map((p) => (typeof p === "string" ? p : p.text)).join("");
}

/** The exact fields a change carries, ready to draw as `was → now`. */
export function changeRows(r: AuditRecord): { field: string; from: string; to: string }[] {
  const raw = detailOf(r)["changes"];
  if (!Array.isArray(raw)) return [];
  return raw.flatMap((c) => {
    if (!c || typeof c !== "object") return [];
    const row = c as Record<string, unknown>;
    if (typeof row["field"] !== "string") return [];
    return [{ field: row["field"], from: value(row["from"]), to: value(row["to"]) }];
  });
}

export interface StepWords {
  /** What this step of a change was, in two or three words. */
  label: string;
  /** The whole line, naming who or what did it. */
  line: string;
  tone: EventTone;
  facts: string[];
}

/**
 * One later entry of a change, as a step under the proposal.
 *
 * A change is five rows in the table and one thing that happened. The steps
 * say who decided and who applied, because "a standing rule approved this and
 * mcpd applied it" is a different fact from "somebody approved it" and the
 * page must not let the second wear the first's name.
 */
export function stepWords(r: AuditRecord, book: NameBook = {}): StepWords {
  const d = detailOf(r);
  const actor = describeActor(r.actor, book);
  const facts: string[] = [];
  const label = STEP_LABELS[r.kind] ?? auditWords(r, book).phrase.map(
    (p) => (typeof p === "string" ? p : p.text)).join("");

  switch (r.kind) {
    case "operation.approved": {
      pushReason(facts, d);
      const authority = authorityWords(d, book);
      return {
        label,
        line: `approved by ${authority ?? actor.word}`,
        tone: "good",
        facts,
      };
    }
    case "operation.rejected":
      pushReason(facts, d);
      return { label, line: `turned down by ${actor.word}`, tone: "neutral", facts };
    case "operation.cancelled":
      pushReason(facts, d);
      return { label, line: `withdrawn by ${actor.word}`, tone: "neutral", facts };
    case "operation.expired":
      return {
        label,
        line: "ran out of time before anybody decided",
        tone: "neutral",
        facts,
      };
    case "operation.executing":
      pushDrift(facts, d);
      return { label, line: `being applied by ${actor.word}`, tone: "info", facts };
    case "operation.succeeded":
      return {
        label,
        line: `applied by ${actor.word}`,
        tone: "good",
        facts: [verifiedWords(bool(d, "verified"), r.plugin ?? "")],
      };
    case "operation.failed":
      if (str(d, "detail")) facts.push(quoted(str(d, "detail")));
      return { label, line: `did not run`, tone: "problem", facts };
    case "operation.indeterminate":
      return {
        label,
        line: "started, and nobody recorded what happened",
        tone: "attention",
        facts: [verifiedWords(bool(d, "verified"), r.plugin ?? "")],
      };
    default:
      return { label, line: label, tone: auditTone(r), facts };
  }
}

const STEP_LABELS: Record<string, string> = {
  "operation.approved": "Approved",
  "operation.rejected": "Turned down",
  "operation.cancelled": "Withdrawn",
  "operation.expired": "Expired",
  "operation.executing": "Applying",
  "operation.succeeded": "Applied",
  "operation.failed": "Didn't run",
  "operation.indeterminate": "Unknown",
};

/* -- the pieces sentences are built from ----------------------------------- */

function detailOf(r: AuditRecord): Record<string, unknown> {
  return r.detail !== null && typeof r.detail === "object" && !Array.isArray(r.detail)
    ? r.detail as Record<string, unknown>
    : {};
}

function str(d: Record<string, unknown>, key: string): string {
  return typeof d[key] === "string" ? d[key] : "";
}

function num(d: Record<string, unknown>, key: string): number | null {
  return typeof d[key] === "number" ? d[key] : null;
}

/** Three-valued on purpose: absent and null both mean "nobody checked". */
function bool(d: Record<string, unknown>, key: string): boolean | null {
  return typeof d[key] === "boolean" ? d[key] : null;
}

function list(d: Record<string, unknown>, key: string): unknown[] {
  return Array.isArray(d[key]) ? d[key] : [];
}

function quoted(text: string): string {
  return `“${text}”`;
}

function person(subject: string, book: NameBook): Phrase {
  const name = book.users?.[subject] ?? (subject.includes("@") ? subject : "");
  return { text: name || "an account", to: "/settings/users" };
}

function key(subject: string, d: Record<string, unknown>, book: NameBook): Phrase {
  const name = str(d, "name") || book.keys?.[subject] || str(d, "name_before");
  return { text: name ? `the key ${name}` : "a key", to: "/settings/keys" };
}

function role(name: string): Phrase {
  return { text: name ? `the role ${name}` : "a role", to: "/settings/roles" };
}

function group(name: string): Phrase {
  return { text: name ? `the group ${name}` : "a group", to: "/settings/groups" };
}

function certificate(name: string): Phrase {
  return { text: name || "one this host trusted", to: "/settings/certificates" };
}

function chatgpt(d: Record<string, unknown>): Phrase {
  const name = str(d, "account");
  return {
    text: name ? `the ChatGPT account ${name}` : "a ChatGPT account",
    to: "/settings/chatgpt",
  };
}

/** A remote server or a plugin, linked to its own page. */
function serverName(subject: string): Phrase[] {
  if (!subject) return ["one this host serves"];
  return [{ text: subject, to: `/plugins/${encodeURIComponent(subject)}` }];
}

/** Whichever member a membership entry names, in words rather than as an id. */
function member(d: Record<string, unknown>, book: NameBook): Phrase {
  const id = str(d, "id");
  if (str(d, "kind") === "key") {
    const name = book.keys?.[id];
    return { text: name ? `the key ${name}` : "a key", to: "/settings/keys" };
  }
  const name = book.users?.[id];
  return { text: name ?? "an account", to: "/settings/users" };
}

function operationPath(r: AuditRecord): string | null {
  return r.operation_id ? `/approvals/${encodeURIComponent(r.operation_id)}` : null;
}

/**
 * A mutation's action as an infinitive phrase.
 *
 * `MutationSpec.Action` is `resource.verb` and stays that way, because the
 * approval policy matches on it and reordering the words would silently stop
 * a stored exclusion matching. The words are still derivable: the last segment
 * is the verb, whatever comes before it is the thing acted on.
 */
export function actionWords(action?: string): string | null {
  const trimmed = (action ?? "").trim();
  if (!trimmed) return null;
  const parts = trimmed.split(".").filter(Boolean);
  const verb = parts.pop()?.replace(/_/g, " ") ?? "";
  if (!verb) return null;
  const resource = parts.join(" ").replace(/_/g, " ");
  return resource ? `${verb} the ${resource}` : verb;
}

function pushReason(facts: string[], d: Record<string, unknown>): void {
  const reason = str(d, "reason");
  if (reason) facts.push(quoted(reason));
}

/**
 * The role a subject was given, by name.
 *
 * The trail stores the role's identifier, and `role_x1a2` in a sentence is the
 * record leaking through the words meant to explain it. A role this reader
 * cannot name is left out of the sentence rather than named badly; it is still
 * in the raw entry, which is where identifiers live.
 */
function pushRole(facts: string[], d: Record<string, unknown>, book: NameBook): void {
  const now = roleName(str(d, "role"), book);
  if (!now) return;
  const before = roleName(str(d, "role_before"), book);
  facts.push(before && before !== now ? `role ${before} → ${now}` : `role ${now}`);
}

function roleName(id: string, book: NameBook): string {
  if (!id) return "";
  return book.roles?.[id] ?? BUILTIN_ROLES[id]?.name ?? "";
}

function pushExpiry(facts: string[], d: Record<string, unknown>): void {
  if (d["expires_at"] === undefined) return;
  const now = str(d, "expires_at");
  const before = str(d, "expires_at_before");
  const word = (v: string) => (v ? day(v) : "never");
  if (d["expires_at_before"] !== undefined && before !== now) {
    facts.push(`expires ${word(before)} → ${word(now)}`);
    return;
  }
  facts.push(now ? `expires ${day(now)}` : "never expires");
}

function pushGroups(facts: string[], d: Record<string, unknown>, verb: string): void {
  const groups = list(d, "groups").length;
  if (groups) facts.push(`${verb} ${groups} ${groups === 1 ? "group" : "groups"}`);
}

/**
 * What a subject reaches, at the level it reaches it.
 *
 * Read and write are separate fragments because they are separate grants, and
 * a re-scope carries what it replaced, so widening is visible as `was → now`
 * rather than only as the state it left behind.
 */
function pushReach(facts: string[], d: Record<string, unknown>): void {
  if (d["grants"] === undefined) return;
  const now = reach(d["grants"]);
  const before = d["grants_before"] !== undefined ? reach(d["grants_before"]) : null;

  for (const level of ["write", "read"] as const) {
    const verb = level === "write" ? "changes" : "reads";
    const a = before?.[level] ?? "";
    const b = now[level];
    if (before && a !== b) {
      if (a || b) facts.push(`${verb} ${a || "nothing"} → ${b || "nothing"}`);
    } else if (b) {
      facts.push(`${verb} ${b}`);
    }
  }
}

function reach(grants: unknown): { read: string; write: string } {
  const out = { read: [] as string[], write: [] as string[] };
  if (Array.isArray(grants)) {
    for (const g of grants) {
      if (!g || typeof g !== "object") continue;
      const row = g as Record<string, unknown>;
      const plugin = typeof row["plugin"] === "string" ? row["plugin"] : "";
      const level = row["level"] === "write" ? "write" : "read";
      if (plugin) out[level].push(plugin === "*" ? "every system" : plugin);
    }
  }
  return { read: out.read.join(", "), write: out.write.join(", ") };
}

/**
 * Who or what cleared a change.
 *
 * A standing rule can approve without anybody being asked, so the trail's
 * authority is a separate fact from whoever proposed it. Naming the rule
 * matters: it is the thing an operator has to revisit.
 */
function authorityWords(d: Record<string, unknown>, book: NameBook): string | null {
  if (str(d, "channel") !== "policy") return null;
  if (str(d, "rule_note")) return `the rule “${str(d, "rule_note")}”`;
  const authority = str(d, "authority");
  if (authority.startsWith("bypass:")) return "an open approval window";
  if (str(d, "rule")) return "a standing rule";
  if (!authority) return "a standing rule";
  return describeActor(authority, book).word;
}

/**
 * Whether the target was re-read afterwards, and what it said.
 *
 * Null is "nobody checked" and false is "checked, and it did not match". They
 * are different facts and collapsing them turns an unverified change into a
 * verified one.
 */
function verifiedWords(verified: boolean | null, system: string): string {
  const against = system ? ` against ${system}` : "";
  if (verified === true) return `checked${against}: the change is in place`;
  if (verified === false) return `checked${against}: it did not match`;
  return "not checked";
}

/**
 * What a drift check found, keeping "nothing had changed" apart from "no
 * check ran". Two absent snapshots comparing equal is not a check that passed.
 */
function pushDrift(facts: string[], d: Record<string, unknown>): void {
  switch (str(d, "drift")) {
    case "detected":
      facts.push("the system had changed since it was proposed");
      break;
    case "none":
      facts.push("nothing had changed since it was proposed");
      break;
    case "not_checked":
      facts.push("no drift check ran");
      break;
  }
}

/** A JSON value inside a change, as the shortest true rendering of it. */
function value(v: unknown): string {
  if (v === undefined || v === null) return "";
  if (typeof v === "string") return v;
  if (typeof v === "number" || typeof v === "boolean") return String(v);
  try {
    return JSON.stringify(v);
  } catch {
    return String(v);
  }
}

/**
 * A moment beside another one: the time alone when they fall on the same day,
 * because a window opened at 06:10 and closing at 06:40 does not need the date
 * saying twice on one line.
 */
function moment(iso: string, near: string): string {
  const a = new Date(iso);
  const b = new Date(near);
  if (Number.isNaN(a.getTime()) || Number.isNaN(b.getTime())) return when(iso);
  const sameDay = a.getFullYear() === b.getFullYear()
    && a.getMonth() === b.getMonth()
    && a.getDate() === b.getDate();
  return sameDay
    ? a.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })
    : when(iso);
}

/** A date without a time, for an expiry or a cutoff. */
function day(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleDateString(undefined, { day: "numeric", month: "long" });
}

/** A span of seconds in the coarsest unit that says it exactly. */
function duration(seconds: number): string {
  const units: [string, number][] = [["day", 86_400], ["hour", 3_600], ["minute", 60]];
  for (const [unit, size] of units) {
    if (seconds >= size && seconds % size === 0) {
      const n = seconds / size;
      return `${n} ${unit}${n === 1 ? "" : "s"}`;
    }
  }
  return `${seconds} second${seconds === 1 ? "" : "s"}`;
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
