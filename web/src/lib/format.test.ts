import { describe, expect, it } from "vitest";
import { describeEvent, pretty, relative, unprefixed, who } from "./format";

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
    expect(unprefixed("cnmaestro_devices", "cnmaestro")).toBe("devices");
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
