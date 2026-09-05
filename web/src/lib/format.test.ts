import { describe, expect, it } from "vitest";
import { describeEvent, pretty, relative, unprefixed, who } from "./format";
// Kept as its own import so this file's new half stays one contiguous piece.
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

describe("audit events", () => {
  it("names the action when there is one", () => {
    expect(describeEvent({
      seq: 1, at: at(0), kind: "operation.approved",
      actor: "user:sam", action: "device.reboot",
    })).toBe("Approved: device reboot");
  });

  // Indeterminate reads as unknown rather than failed, here as everywhere.
  it("does not describe an indeterminate outcome as a failure", () => {
    const text = describeEvent({
      seq: 1, at: at(0), kind: "operation.indeterminate", actor: "system:executor",
    });
    expect(text).toBe("A change ended up in an unknown state");
    expect(text).not.toMatch(/fail/i);
  });

  it("falls back to a readable form of an event it does not know", () => {
    expect(describeEvent({
      seq: 1, at: at(0), kind: "audit.pruned", actor: "user:sam",
    })).toBe("audit pruned");
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
