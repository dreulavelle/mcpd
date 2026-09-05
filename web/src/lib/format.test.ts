import { describe, expect, it } from "vitest";
import type { AuditRecord } from "./api";
import {
  actionWords, auditCategory, auditWords, changeRows, describeActor,
  describeEvent, phraseText, pretty, relative, stepWords, unprefixed, who,
  type NameBook,
} from "./format";
// Kept as its own import so the approvals half stays one contiguous piece.
import {
  confirmationWord, describeChange, describeOutcome, principalWords,
  type ChangeLike,
} from "./format";

const NOW = Date.parse("2026-08-22T12:00:00Z");
const at = (offsetSeconds: number) =>
  new Date(NOW + offsetSeconds * 1000).toISOString();

/**
 * How long is left, which for an approval is the fact somebody is acting on.
 *
 * The unit has to be the largest the gap actually fills: "in 3600 seconds" is
 * arithmetic the reader should not be doing.
 */
describe("relative time", () => {
  const cases: [string, number, string][] = [
    ["seconds ahead", 30, "in 30 seconds"],
    ["seconds behind", -30, "30 seconds ago"],
    ["exactly a minute", 60, "in 1 minute"],
    ["several minutes", 300, "in 5 minutes"],
    ["just under an hour", 3_599, "in 60 minutes"],
    ["an hour", 3_600, "in 1 hour"],
    ["hours", 7_200, "in 2 hours"],
    ["a day", 86_400, "tomorrow"],
    ["days", 172_800, "in 2 days"],
    ["weeks", 1_814_400, "in 3 weeks"],
    ["weeks past", -1_814_400, "3 weeks ago"],
    // Larger units exist, so an old proposal is not "52 weeks ago".
    ["months", 7_776_000, "in 3 months"],
    ["a year past", -31_536_000, "last year"],
    ["years past", -94_608_000, "3 years ago"],
  ];

  for (const [name, offset, expected] of cases) {
    it(`renders ${name} as "${expected}"`, () => {
      expect(relative(at(offset), NOW)).toBe(expected);
    });
  }

  // A timestamp the server sent that this build cannot parse is shown as it
  // arrived. "Invalid Date" tells an operator nothing they can act on.
  it("passes an unparseable timestamp straight through", () => {
    expect(relative("not a date", NOW)).toBe("not a date");
  });
});

describe("identities", () => {
  it("names the host rather than showing its internal prefix", () => {
    expect(who("system:executor")).toBe("mcpd");
  });

  it("strips the kind prefix off a person and a machine", () => {
    expect(who("user:sam@example.com")).toBe("sam@example.com");
    expect(who("svc:assistant")).toBe("assistant");
  });
});

/**
 * Who did it, in words a reader recognises.
 *
 * The trail records identifiers because a name can change and an entry cannot.
 * That makes the identifier the honest thing to store and the wrong thing to
 * read: `key_993f…` at the head of a sentence is a string somebody has to
 * decode before they know what the line says.
 */
describe("who acted", () => {
  const book: NameBook = {
    users: { "sam@example.com": "Sam Vimes", u_7: "Sam Vimes" },
    keys: { key_993f: "ledger" },
  };

  const cases: [string, string, string, string][] = [
    ["a person, by their display name", "user:sam@example.com", "Sam Vimes", "person"],
    ["somebody who signed themselves up", "self:sam@example.com", "Sam Vimes", "person"],
    ["a key, by its name", "key:key_993f", "the key ledger", "key"],
    ["a ChatGPT workspace", "svc:chatgpt:work", "ChatGPT (work)", "service"],
    ["a connector with no workspace", "svc:chatgpt", "ChatGPT", "service"],
    // A rule an operator wrote approved this. Reading it as the host's own
    // doing hides the thing that needs revisiting.
    ["a standing rule", "system:policy", "a standing rule", "system"],
    ["mcpd applying a change", "system:executor", "mcpd", "system"],
    ["mcpd closing a stalled one", "system:reaper", "mcpd", "system"],
    ["mcpd tidying the record", "system:retention", "mcpd", "system"],
    ["a setting nobody invoked", "system:registration", "a sign-up default", "system"],
  ];

  it.each(cases)("names %s", (_name, actor, word, kind) => {
    const words = describeActor(actor, book);
    expect(words.word).toBe(word);
    expect(words.kind).toBe(kind);
  });

  // Listing accounts and keys takes a permission the reader may not hold. A
  // sentence still has to be a sentence without them.
  it("falls back to the local part, and to 'a key', without the names", () => {
    expect(describeActor("user:sam@example.com").word).toBe("sam");
    expect(describeActor("key:key_993f").word).toBe("a key");
  });

  /**
   * `principalWords` is the same question asked without a name book, so it is
   * the same answer. Two functions for "who is this" drift, and the page that
   * drifted would be the one nobody was looking at.
   */
  it("is the one authority principalWords reads", () => {
    for (const actor of [
      "system:policy", "system:executor", "system:registration",
      "svc:chatgpt:work", "svc:assistant", "user:sam@example.com", "key:k-17",
    ]) {
      expect(principalWords(actor)).toBe(describeActor(actor).word);
    }
  });

  // Housekeeping is in the record and not in the foreground.
  it("marks only the scheduled tidying as housekeeping", () => {
    expect(describeActor("system:retention").housekeeping).toBe(true);
    expect(describeActor("system:executor").housekeeping).toBe(false);
  });
});

/**
 * An action is `resource.verb` because the approval policy matches on it, so
 * the words are derivable rather than invented -- and every sentence puts the
 * result after "to" or after a modal, so no verb is ever conjugated.
 */
describe("an action as words", () => {
  it.each([
    ["label.set", "set the label"],
    ["device.reboot", "reboot the device"],
    ["stream.pause", "pause the stream"],
    ["firewall_rule.delete", "delete the firewall rule"],
    ["site.device.reboot", "reboot the site device"],
    ["reboot", "reboot"],
    // A last segment of several words carries its own resource. Treating it
    // as one verb produced "set radio channel the device".
    ["device.set_radio_channel", "set the radio channel"],
    ["device.setRadioChannel", "set the radio channel"],
    // An unknown first word is not a verb on a guess.
    ["foo.bar_baz", "bar baz the foo"],
    // Through the shared parser, so the audit trail gets the resource table
    // the Approvals page already had: the one irreversible bookstack action
    // destroys an item in the bin, not the bin.
    ["recycle_bin.destroy", "destroy the item in the recycle bin"],
  ])("reads %s as %s", (action, want) => {
    expect(actionWords(action)).toBe(want);
  });

  /**
   * Every action any plugin in this repository declares, so a new mutation
   * whose name does not read as English is caught here rather than on the page.
   *
   * Kept in step by hand with `grep -rhoE 'Action:\s*"[^"]+"' internal/plugins`.
   */
  const DECLARED = [
    "attachment.create", "attachment.delete", "attachment.update",
    "book.create", "book.delete", "book.update",
    "chapter.create", "chapter.delete", "chapter.update",
    "comment.create", "comment.delete", "comment.update",
    "content_permissions.update",
    "device.reboot", "device.set_radio_channel",
    "label.set",
    "page.create", "page.delete", "page.update",
    "recycle_bin.destroy", "recycle_bin.restore",
    "role.create", "role.delete", "role.update",
    "shelf.create", "shelf.delete", "shelf.update",
    "user.create", "user.delete", "user.update",
  ];

  it.each(DECLARED)("reads %s as a phrase that can follow \"asked to\"", (action) => {
    const words = actionWords(action)!;
    expect(words).toBeTruthy();
    // No machine casing left, and a verb at the front rather than a noun.
    expect(words).not.toMatch(/[_.]/);
    expect(words).toMatch(/^[a-z]+( the .+)?$/);
  });

  it("has nothing to say about an absent action", () => {
    expect(actionWords(undefined)).toBeNull();
    expect(actionWords("  ")).toBeNull();
  });
});

/**
 * Every kind of entry, as the sentence an ops manager reads.
 *
 * The table is the point: a kind added to a writer without a sentence here
 * falls through to the default, and the default is checked too. Two rules hold
 * across every row and are asserted for all of them at once below -- no
 * identifier and no JSON reaches prose.
 */
describe("an audit entry as a sentence", () => {
  const book: NameBook = {
    users: { "sam@example.com": "Sam Vimes", u_7: "Sam Vimes" },
    keys: { key_993f: "ledger" },
  };

  function entry(kind: string, over: Partial<AuditRecord> = {}): AuditRecord {
    return { seq: 1, at: at(0), kind, actor: "user:sam@example.com", ...over };
  }

  const cases: [string, AuditRecord, string][] = [
    ["operation.proposed", entry("operation.proposed", {
      operation_id: "op_1", plugin: "echo", action: "label.set",
      detail: { impact: "Changes the label reported upstream", reversible: true },
    }), "asked to set the label on echo"],

    ["operation.approved", entry("operation.approved", {
      actor: "system:policy", operation_id: "op_1", plugin: "echo", action: "label.set",
      detail: { channel: "policy", authority: "bypass:byp_2", reason: "inside an open window" },
    }), "approved a request to set the label on echo"],

    ["operation.rejected", entry("operation.rejected", {
      operation_id: "op_1", plugin: "echo", action: "label.set",
      detail: { reason: "wrong window" },
    }), "turned down a request to set the label on echo"],

    ["operation.cancelled", entry("operation.cancelled", {
      operation_id: "op_1", plugin: "echo", action: "label.set",
    }), "withdrew a request to set the label on echo"],

    ["operation.expired", entry("operation.expired", {
      actor: "system:reaper", operation_id: "op_1", plugin: "echo", action: "label.set",
    }), "recorded that a request to set the label on echo ran out of time"],

    ["operation.executing", entry("operation.executing", {
      actor: "system:executor", operation_id: "op_1", plugin: "echo", action: "label.set",
      detail: { drift: "none" },
    }), "started to set the label on echo"],

    ["operation.succeeded", entry("operation.succeeded", {
      actor: "system:executor", operation_id: "op_1", plugin: "echo", action: "label.set",
      detail: { verified: true },
    }), "applied the change to set the label on echo"],

    ["operation.failed", entry("operation.failed", {
      actor: "system:executor", operation_id: "op_1", plugin: "echo", action: "label.set",
      detail: { error_code: "upstream_failed", detail: "echo refused it" },
    }), "could not set the label on echo"],

    ["operation.indeterminate", entry("operation.indeterminate", {
      actor: "system:executor", operation_id: "op_1", plugin: "echo", action: "label.set",
      detail: { verified: null },
    }), "started to set the label on echo and could not tell whether it landed"],

    ["account.registered", entry("account.registered", {
      actor: "self:sam@example.com", plugin: "sam@example.com",
      detail: { status: "pending", role: "role_operator", provider: "password", groups: [] },
    }), "signed up"],

    ["account.approved", entry("account.approved", {
      plugin: "sam@example.com", detail: { account: "u_7", role: "role_operator", groups: ["g_1"] },
    }), "let Sam Vimes in"],

    ["account.rejected", entry("account.rejected", {
      plugin: "sam@example.com", detail: { account: "u_7" },
    }), "turned down Sam Vimes's sign-up"],

    ["account.identity_linked", entry("account.identity_linked", {
      plugin: "sam@example.com", detail: { provider: "google", account: "u_7" },
    }), "linked a google sign-in to Sam Vimes"],

    // The subject on this one is an account id, not an address, so it is
    // named from the book or not at all.
    ["account.identity_unlinked", entry("account.identity_unlinked", {
      plugin: "u_7", detail: { provider: "google" },
    }), "unlinked a google sign-in from Sam Vimes"],

    ["apikey.created", entry("apikey.created", {
      plugin: "key_993f",
      detail: {
        name: "ledger", role: "role_operator", groups: [],
        grants: [{ plugin: "cnmaestro", level: "write" }], expires_at: null,
      },
    }), "created the key ledger"],

    ["apikey.rescoped", entry("apikey.rescoped", {
      plugin: "key_993f",
      detail: {
        grants: [{ plugin: "cnmaestro", level: "write" }, { plugin: "netbox", level: "read" }],
        grants_before: [{ plugin: "cnmaestro", level: "write" }],
      },
    }), "changed what the key ledger may do"],

    // The detail is sparse. A name and nothing else changed is a rename, and
    // calling it a change to what the key may do records a privilege change
    // that never happened.
    ["apikey.rescoped, renamed only", entry("apikey.rescoped", {
      plugin: "key_993f", detail: { name: "ledger", name_before: "temp-key" },
    }), "renamed the key temp-key to ledger"],

    ["apikey.rotated", entry("apikey.rotated", {
      plugin: "key_993f", detail: { name: "ledger", grace_seconds: 3600 },
    }), "gave the key ledger a new secret"],

    ["apikey.revoked", entry("apikey.revoked", {
      plugin: "key_993f", detail: { name: "ledger" },
    }), "revoked the key ledger"],

    ["role.created", entry("role.created", {
      plugin: "Auditor", detail: { role: "role_x1", permissions: { history: "read" } },
    }), "created the role Auditor"],

    ["role.updated", entry("role.updated", {
      plugin: "Auditor",
      detail: { role: "role_x1", permissions: { history: "read" }, permissions_before: {} },
    }), "changed what the role Auditor may do"],

    ["role.updated, renamed only", entry("role.updated", {
      plugin: "Auditor", detail: { role: "role_x1", renamed_from: "Reader" },
    }), "renamed the role Reader to Auditor"],

    ["role.deleted", entry("role.deleted", {
      plugin: "Auditor", detail: { role: "role_x1" },
    }), "deleted the role Auditor"],

    ["group.created", entry("group.created", {
      plugin: "Field team",
      detail: { group: "g_1", role: "role_operator", grants: [{ plugin: "echo", level: "read" }] },
    }), "created the group Field team"],

    ["group.updated", entry("group.updated", {
      plugin: "Field team",
      detail: {
        group: "g_1",
        grants: [{ plugin: "echo", level: "write" }],
        grants_before: [{ plugin: "echo", level: "read" }],
      },
    }), "changed what the group Field team hands out"],

    // Named once, not twice: "to the group Field team" after "renamed the
    // group" says the same noun over again.
    ["group.updated, renamed only", entry("group.updated", {
      plugin: "Field team", detail: { group: "g_1", renamed_from: "Field crew" },
    }), "renamed the group Field crew to Field team"],

    ["group.deleted", entry("group.deleted", {
      plugin: "Field team", detail: { group: "g_1", role: "role_operator", grants: [], members: 3 },
    }), "deleted the group Field team"],

    ["group.member_added", entry("group.member_added", {
      actor: "system:registration", plugin: "Field team",
      detail: { group: "g_1", kind: "user", id: "u_7" },
    }), "added Sam Vimes to the group Field team"],

    ["group.member_removed", entry("group.member_removed", {
      plugin: "Field team", detail: { group: "g_1", kind: "key", id: "key_993f" },
    }), "removed the key ledger from the group Field team"],

    ["certificate.added", entry("certificate.added", {
      plugin: "corp root",
      detail: {
        certificate: "cert_1", subject: "CN=corp", fingerprint: "ab:cd",
        expires_at: "2027-01-09T00:00:00Z",
      },
    }), "trusted the certificate corp root"],

    ["certificate.removed", entry("certificate.removed", {
      plugin: "corp root", detail: { certificate: "cert_1", subject: "CN=corp", fingerprint: "ab:cd" },
    }), "stopped trusting the certificate corp root"],

    ["mcpserver.imported", entry("mcpserver.imported", {
      plugin: "graylog",
      detail: { transport: "http", endpoint: "https://example.test/mcp", schema_version: "1" },
    }), "added the remote server graylog"],

    ["mcpserver.removed", entry("mcpserver.removed", {
      plugin: "graylog", detail: { tools_stored: 9, tools_enabled: 2 },
    }), "removed the remote server graylog"],

    ["mcpserver.enabled", entry("mcpserver.enabled", {
      plugin: "graylog", detail: { enabled: true },
    }), "switched on the remote server graylog"],

    ["mcpserver.disabled", entry("mcpserver.disabled", {
      plugin: "graylog", detail: { enabled: false },
    }), "switched off the remote server graylog"],

    // The header's name is evidence, so the sentence does not carry it.
    ["mcpserver.header_added", entry("mcpserver.header_added", {
      plugin: "graylog", detail: { header: "X-Api-Key", secret: true },
    }), "added a header to graylog"],

    ["mcpserver.header_removed", entry("mcpserver.header_removed", {
      plugin: "graylog", detail: { header: "X-Api-Key" },
    }), "removed a header from graylog"],

    ["mcpserver.discovered", entry("mcpserver.discovered", {
      actor: "system:rediscovery", plugin: "graylog",
      detail: { offered: 9, sequence: 3, added: ["search_events"], changed: [], removed: [], unchanged: 8 },
    }), "read the tools graylog offers"],

    ["mcpserver.tool_classified", entry("mcpserver.tool_classified", {
      plugin: "graylog",
      detail: { tool: "search_events", from: "pending", to: "enabled", descriptor_hash: "ab" },
    }), "allowed search_events on graylog"],

    ["plugin.removed", entry("plugin.removed", {
      plugin: "echo", detail: { source: "configuration file", declared_type: "echo" },
    }), "removed the plugin echo"],

    ["plugin.restored", entry("plugin.restored", {
      plugin: "echo", detail: { source: "configuration file" },
    }), "restored the plugin echo"],

    ["plugin.enabled", entry("plugin.enabled", {
      plugin: "echo", detail: { source: "configuration file", enabled: true },
    }), "switched on the plugin echo"],

    ["plugin.disabled", entry("plugin.disabled", {
      plugin: "echo", detail: { source: "configuration file", enabled: false },
    }), "switched off the plugin echo"],

    ["chatgpt.account.added", entry("chatgpt.account.added", {
      detail: {
        account: "Field ops", account_id: "acct_3", principal: "svc:chatgpt",
        role: "role_operator", grants: [], rate_per_sec: 2, has_admin_key: false,
      },
    }), "added the ChatGPT account Field ops"],

    ["chatgpt.account.updated", entry("chatgpt.account.updated", {
      detail: { account: "Field ops", account_id: "acct_3", admin_key: "cleared" },
    }), "changed the ChatGPT account Field ops"],

    ["chatgpt.account.removed", entry("chatgpt.account.removed", {
      detail: { account: "Field ops", account_id: "acct_3", principal: "svc:chatgpt" },
    }), "removed the ChatGPT account Field ops"],

    ["approval.bypass.opened", entry("approval.bypass.opened", {
      plugin: "byp_2",
      detail: {
        minutes: 30, plugin: "echo", ceiling: "low", reason: "narrower, right plugin",
        expires_at: "2026-08-22T12:40:00Z",
      },
    }), "opened a 30-minute approval window"],

    ["approval.bypass.revoked", entry("approval.bypass.revoked", {
      plugin: "all", detail: { closed: 1 },
    }), "closed the open approval window"],

    // Midday, and the day itself spelled by the platform: the cutoff is drawn
    // in the reader's own locale and time zone, and pinning either here would
    // fail on a machine set to neither.
    ["audit.pruned", entry("audit.pruned", {
      actor: "system:retention",
      detail: { removed_entries: 16, older_than: "2026-07-29T12:00:00Z" },
    }), `removed 16 entries older than ${new Date("2026-07-29T12:00:00Z")
      .toLocaleDateString(undefined, { day: "numeric", month: "long" })}`],

    // A trail outliving the dashboard that renders it is the normal case
    // after an upgrade, so an unknown kind is still a sentence.
    ["a kind this build has never heard of", entry("tunnels.disconnected", {
      plugin: "field", actor: "system:executor",
    }), "recorded tunnels disconnected"],
  ];

  it.each(cases)("says, for %s, what happened", (_name, record, sentence) => {
    expect(phraseText(auditWords(record, book).phrase)).toBe(sentence);
  });

  /**
   * Two rules, over every row at once. An identifier in a sentence is a string
   * a reader has to decode, and a JSON fragment in prose is the record leaking
   * through the words that exist to explain it.
   */
  it.each(cases)("keeps identifiers and JSON out of the sentence for %s", (_name, record) => {
    const sentence = phraseText(auditWords(record, book).phrase);
    expect(sentence).not.toMatch(/\b(key_|byp_|op_|acct_|cert_|u_\d|g_\d|role_x)/);
    expect(sentence).not.toMatch(/[{}[\]]/);
  });

  it("carries the facts under the sentence rather than in it", () => {
    const [, opened] = cases.find(([name]) => name === "approval.bypass.opened")!;
    const { facts } = auditWords(opened, book);
    expect(facts).toContain("echo only");
    expect(facts).toContain("up to low risk");
    expect(facts).toContain("“narrower, right plugin”");
  });

  /**
   * A re-scope records what the grant was as well as what it became, because
   * an entry carrying only the new value leaves "what did this widen"
   * unanswerable.
   */
  it("shows a widened reach as what it was and what it became", () => {
    const [, rescoped] = cases.find(([name]) => name === "apikey.rescoped")!;
    expect(auditWords(rescoped, book).facts).toContain("reads nothing → netbox");
  });

  // Null is "nobody checked" and false is "checked, and it did not match".
  // Collapsing them turns an unverified change into a verified one.
  it.each([
    [true, "checked against echo: the change is in place"],
    [false, "checked against echo: the change could not be confirmed"],
    [null, "not checked"],
  ])("keeps all three values of verified apart (%s)", (verified, want) => {
    const record: AuditRecord = {
      seq: 1, at: at(0), kind: "operation.succeeded", actor: "system:executor",
      operation_id: "op_1", plugin: "echo", action: "label.set", detail: { verified },
    };
    expect(auditWords(record).facts).toContain(want);
  });

  // Two absent snapshots comparing equal is not a check that passed.
  it.each([
    ["none", "nothing had changed since it was proposed"],
    ["detected", "the system had changed since it was proposed"],
    ["not_checked", "whether the system had changed was not checked"],
  ])("says which of the three a drift check was (%s)", (drift, want) => {
    const record: AuditRecord = {
      seq: 1, at: at(0), kind: "operation.executing", actor: "system:executor",
      operation_id: "op_1", plugin: "echo", action: "label.set", detail: { drift },
    };
    expect(auditWords(record).facts).toContain(want);
  });

  it("sorts every kind it knows into a category somebody can filter on", () => {
    expect(auditCategory({ seq: 1, at: at(0), kind: "operation.proposed", actor: "x" }))
      .toBe("changes");
    expect(auditCategory({ seq: 1, at: at(0), kind: "apikey.created", actor: "x" }))
      .toBe("access");
    expect(auditCategory({ seq: 1, at: at(0), kind: "approval.bypass.opened", actor: "x" }))
      .toBe("windows");
    expect(auditCategory({ seq: 1, at: at(0), kind: "audit.pruned", actor: "x" }))
      .toBe("housekeeping");
    expect(auditCategory({ seq: 1, at: at(0), kind: "mcpserver.imported", actor: "x" }))
      .toBe("systems");
  });
});

/**
 * The later entries of a change, read as steps of the one thing that happened.
 *
 * "A standing rule approved this" is a different fact from "somebody approved
 * this", and an auto-approved change means a propose call can execute before
 * it returns with nobody asked at all.
 */
describe("a change's steps", () => {
  function step(kind: string, over: Partial<AuditRecord> = {}) {
    return stepWords({
      seq: 1, at: at(0), kind, actor: "system:executor",
      operation_id: "op_1", plugin: "echo", action: "label.set", ...over,
    });
  }

  it("names the rule that approved it rather than the host that ran it", () => {
    expect(step("operation.approved", {
      actor: "system:policy",
      detail: { channel: "policy", rule: "rule_4", rule_note: "labels on echo" },
    }).line).toBe("approved by the rule “labels on echo”");
  });

  it("says when an open window was what cleared it", () => {
    expect(step("operation.approved", {
      actor: "system:policy",
      detail: { channel: "policy", authority: "bypass:byp_2" },
    }).line).toBe("approved by an open approval window");
  });

  it("names the person when a person decided", () => {
    expect(step("operation.approved", {
      actor: "user:sam@example.com", detail: { channel: "dashboard", reason: "fine" },
    }).line).toBe("approved by sam");
  });

  it("does not read an unknown outcome as a failure", () => {
    const words = step("operation.indeterminate", { detail: { verified: null } });
    expect(words.tone).toBe("attention");
    expect(words.line).not.toMatch(/fail/i);
    expect(words.facts).toContain("not checked");
  });
});

describe("the exact fields a change carries", () => {
  it("reads them as was and now", () => {
    expect(changeRows({
      seq: 1, at: at(0), kind: "operation.proposed", actor: "user:sam",
      detail: { changes: [{ field: "label", from: "multi-window", to: "narrower-window" }] },
    })).toEqual([{ field: "label", from: "multi-window", to: "narrower-window" }]);
  });

  it("has nothing to draw for a call that carried an authorisation and nothing else", () => {
    expect(changeRows({
      seq: 1, at: at(0), kind: "operation.proposed", actor: "user:sam",
      detail: { assurance: "gated_call" },
    })).toEqual([]);
  });
});

describe("audit events, as one string", () => {
  // Overview's last few lines want a sentence with a capital on it.
  it("is a single sentence starting with a capital", () => {
    expect(describeEvent({
      seq: 1, at: at(0), kind: "operation.approved",
      actor: "user:sam", action: "device.reboot",
    })).toBe("Approved a request to reboot the device");
  });

  // Indeterminate reads as unknown rather than failed, here as everywhere.
  it("does not describe an indeterminate outcome as a failure", () => {
    const text = describeEvent({
      seq: 1, at: at(0), kind: "operation.indeterminate", actor: "system:executor",
      action: "label.set", plugin: "echo",
    });
    expect(text).toBe("Started to set the label on echo and could not tell whether it landed");
    expect(text).not.toMatch(/fail/i);
  });

  it("falls back to a readable form of an event it does not know", () => {
    expect(describeEvent({
      seq: 1, at: at(0), kind: "tunnels.disconnected", actor: "user:sam",
    })).toBe("Recorded tunnels disconnected");
  });
});

describe("presentation helpers", () => {
  it("drops a tool's plugin prefix, and only its own", () => {
    expect(unprefixed("cnmaestro_list_devices", "cnmaestro")).toBe("list_devices");
    expect(unprefixed("netbox_sites", "cnmaestro")).toBe("netbox_sites");
  });

  it("renders nothing for an absent payload rather than the word null", () => {
    expect(pretty(null)).toBe("");
    expect(pretty(undefined)).toBe("");
  });

  it("leaves a string alone rather than wrapping it in quotes", () => {
    expect(pretty("already text")).toBe("already text");
  });
});

/**
 * A change, as a sentence.
 *
 * The page somebody approves from used to head each row with
 * `op.action.replace(/[._]/g, " ")`, which turns `label.set` into "label set".
 * That is not English, it is the machine's name with the dots taken out, and
 * an ops manager reading it has to already know what the plugin calls things.
 *
 * `MutationSpec.Action` is `resource.verb` and stays that way because the
 * approval policy matches on it, so the words are derivable and the raw form
 * never has to be shown to a person.
 */
describe("describing a change", () => {
  const op = (over: Partial<ChangeLike> = {}): ChangeLike =>
    ({ plugin: "echo", action: "label.set", ...over });

  const cases: [string, ChangeLike, string][] = [
    [
      "a set with a before and an after",
      op({ changes: [{ field: "window", from: "multi-window", to: "narrower-window" }] }),
      "Set the label on echo, from multi-window to narrower-window.",
    ],
    [
      "a set with nothing recorded before it",
      op({ changes: [{ field: "window", from: null, to: "narrower-window" }] }),
      "Set the label on echo to narrower-window.",
    ],
    [
      "a multi-segment resource",
      op({
        plugin: "cnmaestro", action: "radio.channel.set",
        changes: [{ field: "channel", from: 36, to: 44 }],
      }),
      "Set the radio channel on cnmaestro, from 36 to 44.",
    ],
    [
      "a verb that is not one of the four common ones",
      op({ plugin: "cnmaestro", action: "device.reboot" }),
      "Restart the device on cnmaestro.",
    ],
    [
      "creating something, which takes an indefinite article",
      op({ plugin: "netbox", action: "site.create" }),
      "Create a site on netbox.",
    ],
    [
      "creating something that starts with a vowel",
      op({ plugin: "netbox", action: "address.create" }),
      "Create an address on netbox.",
    ],
    [
      "a delete, which reads as removing",
      op({ plugin: "graylog", action: "stream.delete" }),
      "Remove the stream on graylog.",
    ],
    [
      "a flag on an action whose verb does not already say which way",
      op({
        plugin: "graylog", action: "alert.set",
        changes: [{ field: "silenced", from: false, to: true }],
      }),
      "Set the alert on graylog, from off to on.",
    ],
    [
      "an underscored resource",
      op({ plugin: "threecx", action: "call_queue.update" }),
      "Change the call queue on threecx.",
    ],
    [
      "a verb this build has never heard of",
      op({ plugin: "observium", action: "poller.quiesce" }),
      "Quiesce the poller on observium.",
    ],
    // The SDK's own worked example. Splitting only on "." made the verb
    // `set_radio_channel`, which is not in the map, and the whole segment
    // became the verb: "Set radio channel the device on cnmaestro".
    [
      "a last segment that is a verb and its own resource",
      op({
        plugin: "cnmaestro", action: "device.set_radio_channel",
        changes: [{ field: "channel", from: 36, to: 44 }],
      }),
      "Set the radio channel on cnmaestro, from 36 to 44.",
    ],
    // "an user" is what a plain vowel test produces, and bookstack creates
    // users.
    [
      "a resource beginning with u, which takes a not an",
      op({ plugin: "bookstack", action: "user.create" }),
      "Create a user on bookstack.",
    ],
    // A page that did not exist has nothing to have changed from, so its
    // value is its name: "Create a page on bookstack to Getting started" read
    // as a move.
    [
      "a create whose only recorded field is the new thing's name",
      op({
        plugin: "bookstack", action: "page.create",
        changes: [{ field: "page", from: null, to: "Getting started" }],
      }),
      "Create a page on bookstack, called “Getting started”.",
    ],
    [
      "a create naming something with no spaces in its name",
      op({
        plugin: "bookstack", action: "book.create",
        changes: [{ field: "book", from: null, to: "runbooks" }],
      }),
      "Create a book on bookstack, called “runbooks”.",
    ],
    // The bin is not what is destroyed; one item in it is. This is also the
    // one irreversible action bookstack declares, so a sentence that reads as
    // tidying up is the wrong sentence.
    [
      "an action on the recycle bin, which acts on one item in it",
      op({ plugin: "bookstack", action: "recycle_bin.destroy" }),
      "Destroy the item in the recycle bin on bookstack.",
    ],
    [
      "restoring from the recycle bin",
      op({ plugin: "bookstack", action: "recycle_bin.restore" }),
      "Restore the item in the recycle bin on bookstack.",
    ],
    // "Turn off the alert, from on to off" says the verb twice, once in
    // English and once in the machine's words.
    [
      "a flag, whose before and after only restate the verb",
      op({
        plugin: "graylog", action: "alert.disable",
        changes: [{ field: "enabled", from: true, to: false }],
      }),
      "Turn off the alert on graylog.",
    ],
    [
      "a value with spaces in it, quoted so it does not run into the sentence",
      op({
        plugin: "netbox", action: "site.rename",
        changes: [{ field: "name", from: "Main office", to: "Branch office" }],
      }),
      "Rename the site on netbox, from “Main office” to “Branch office”.",
    ],
    [
      "an action with no resource in front of the verb",
      op({ plugin: "cnmaestro", action: "reboot" }),
      "Restart on cnmaestro.",
    ],
  ];

  for (const [name, operation, expected] of cases) {
    it(`renders ${name}`, () => {
      expect(describeChange(operation).sentence).toBe(expected);
    });
  }

  // The three things that must never reach a sentence: the machine's own name
  // for the action, a JSON object pasted into prose, and an id.
  it("never leaks the raw action, a payload or an id into the sentence", () => {
    for (const [, operation] of cases) {
      const { sentence } = describeChange(operation);
      expect(sentence).not.toContain(operation.action);
      expect(sentence).not.toContain("{");
      expect(sentence).not.toMatch(/op-[0-9a-f]/i);
    }
  });

  // A structured value has no honest short form. Half of one -- "[object
  // Object]", or the first line of a JSON dump -- is worse than none, and the
  // field table on the detail page still carries it in full.
  it("drops the clause rather than pasting an object into it", () => {
    const { sentence, detail } = describeChange(op({
      impact: "Replaces the whole tag set.",
      changes: [{ field: "tags", from: ["a"], to: ["a", "b"] }],
    }));
    expect(sentence).toBe("Set the label on echo.");
    expect(detail).toBe("Replaces the whole tag set.");
  });

  // With no fields recorded there is nothing to diff, so the plugin's own
  // sentence stands in -- but it stays a sentence of its own rather than being
  // run into the headline.
  it("falls back to the plugin's impact when no fields were recorded", () => {
    const { headline, detail, sentence } = describeChange(op({
      impact: "Moves one radio to another channel.",
    }));
    expect(headline).toBe("Set the label on echo");
    expect(detail).toBe("Moves one radio to another channel.");
    expect(sentence).toBe("Set the label on echo.");
  });

  it("still says something about a change with neither fields nor impact", () => {
    expect(describeChange({ plugin: "echo", action: "" }).sentence)
      .toBe("A change on echo.");
  });
});

/**
 * Every action this build's plugins actually declare, read out loud.
 *
 * A table of hand-picked shapes proves the rules; this proves the rules cover
 * what ships. Collected with
 * `grep -rhoE 'Action:\s*"[^"]+"' internal/plugins | sort -u`; the sentence
 * builder never sees these until a change reaches somebody's dashboard, so a
 * malformed one has no earlier place to fail.
 */
describe("every action the plugins declare", () => {
  const ACTIONS: [string, string, string][] = [
    ["bookstack", "attachment.create", "Create an attachment on bookstack."],
    ["bookstack", "attachment.delete", "Remove the attachment on bookstack."],
    ["bookstack", "attachment.update", "Change the attachment on bookstack."],
    ["bookstack", "book.create", "Create a book on bookstack."],
    ["bookstack", "book.delete", "Remove the book on bookstack."],
    ["bookstack", "book.update", "Change the book on bookstack."],
    ["bookstack", "chapter.create", "Create a chapter on bookstack."],
    ["bookstack", "chapter.delete", "Remove the chapter on bookstack."],
    ["bookstack", "chapter.update", "Change the chapter on bookstack."],
    ["bookstack", "comment.create", "Create a comment on bookstack."],
    ["bookstack", "comment.delete", "Remove the comment on bookstack."],
    ["bookstack", "comment.update", "Change the comment on bookstack."],
    ["bookstack", "content_permissions.update", "Change the content permissions on bookstack."],
    ["bookstack", "page.create", "Create a page on bookstack."],
    ["bookstack", "page.delete", "Remove the page on bookstack."],
    ["bookstack", "page.update", "Change the page on bookstack."],
    ["bookstack", "recycle_bin.destroy", "Destroy the item in the recycle bin on bookstack."],
    ["bookstack", "recycle_bin.restore", "Restore the item in the recycle bin on bookstack."],
    ["bookstack", "role.create", "Create a role on bookstack."],
    ["bookstack", "role.delete", "Remove the role on bookstack."],
    ["bookstack", "role.update", "Change the role on bookstack."],
    ["bookstack", "shelf.create", "Create a shelf on bookstack."],
    ["bookstack", "shelf.delete", "Remove the shelf on bookstack."],
    ["bookstack", "shelf.update", "Change the shelf on bookstack."],
    ["bookstack", "user.create", "Create a user on bookstack."],
    ["bookstack", "user.delete", "Remove the user on bookstack."],
    ["bookstack", "user.update", "Change the user on bookstack."],
    ["cnmaestro", "device.reboot", "Restart the device on cnmaestro."],
    ["cnmaestro", "device.set_radio_channel", "Set the radio channel on cnmaestro."],
    ["echo", "label.set", "Set the label on echo."],
    ["echo", "thing.set", "Set the thing on echo."],
    ["echo", "widget.set", "Set the widget on echo."],
  ];

  for (const [plugin, action, expected] of ACTIONS) {
    it(`reads ${action} as "${expected}"`, () => {
      expect(describeChange({ plugin, action }).sentence).toBe(expected);
    });
  }

  it("leaves no action reading as its own machine name", () => {
    for (const [plugin, action] of ACTIONS) {
      const { headline } = describeChange({ plugin, action });
      expect(headline).not.toContain(action);
      expect(headline).not.toContain("_");
      expect(headline).not.toContain(".");
      // Every one is a verb, a thing and the system it is on.
      expect(headline).toMatch(new RegExp(`^[A-Z].* on ${plugin}$`));
    }
  });
});

/**
 * A principal as a name. `system:policy` is the one that matters: it is not an
 * account, and a page that renders it says a person decided when nobody did.
 */
describe("naming a principal", () => {
  const cases: [string, string][] = [
    ["system:policy", "a standing rule"],
    ["system:executor", "mcpd"],
    ["svc:chatgpt:work", "ChatGPT (work)"],
    ["svc:chatgpt", "ChatGPT"],
    ["svc:assistant", "Assistant"],
    ["user:sam@example.com", "sam"],
    ["key:k-17", "a key"],
  ];

  for (const [actor, expected] of cases) {
    it(`renders ${actor} as "${expected}"`, () => {
      expect(principalWords(actor)).toBe(expected);
    });
  }

  it("leaves nothing machine-shaped in any of them", () => {
    for (const [actor] of cases) {
      expect(principalWords(actor)).not.toContain(":");
    }
  });
});

/** What became of a change, for a list row. */
describe("describing an outcome", () => {
  const settled = (over: Partial<ChangeLike>): ChangeLike => ({
    plugin: "echo", action: "label.set", terminal_at: at(-3600), ...over,
  });

  it("says a change was applied, and when", () => {
    expect(describeOutcome(settled({ state: "succeeded" }), NOW))
      .toBe("applied 1 hour ago");
  });

  // Indeterminate is not failed. It also carries no time, because "ended in an
  // unknown state an hour ago" invites reading the hour as a settlement.
  it("does not call an unknown outcome a failure", () => {
    const text = describeOutcome(settled({ state: "indeterminate" }), NOW);
    expect(text).toBe("ended in an unknown state; it may have landed");
    expect(text).not.toMatch(/fail|didn't run/i);
  });

  it("distinguishes turned down, ran out of time and withdrawn", () => {
    expect(describeOutcome(settled({ state: "rejected" }), NOW)).toBe("turned down 1 hour ago");
    expect(describeOutcome(settled({ state: "expired" }), NOW)).toBe("ran out of time 1 hour ago");
    expect(describeOutcome(settled({ state: "cancelled" }), NOW)).toBe("withdrawn 1 hour ago");
  });

  it("dates a change that has not settled from when it was proposed", () => {
    expect(describeOutcome(
      { plugin: "echo", action: "label.set", state: "pending_approval", requested_at: at(-600) },
      NOW,
    )).toBe("proposed 10 minutes ago");
  });
});

/**
 * Three values, three words. Absent is "not checked" -- nobody read the system
 * again -- and collapsing it into either of the other two is the bug this
 * console has a rule about.
 */
describe("the confirmation word", () => {
  it("keeps unchecked apart from confirmed and from mismatched", () => {
    expect(confirmationWord(true)).toBe("confirmed");
    expect(confirmationWord(false)).toBe("did not match");
    expect(confirmationWord(null)).toBe("not checked");
    expect(confirmationWord(undefined)).toBe("not checked");
  });
});
