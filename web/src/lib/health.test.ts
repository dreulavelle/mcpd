import { describe, expect, it } from "vitest";
import { parseHealth, statusTone, statusWords } from "./health";

/**
 * A status only appears in a health message when the check went wrong, so the
 * words have to hold for every status a plugin can quote -- including 2xx.
 *
 * The bug: everything not named fell through to "Refused", so Observium
 * answering 200 with a body that would not parse was reported as the far end
 * having said no, beside a chip coloured as if all was well.
 */
describe("an HTTP status as words", () => {
  const cases: [number, string][] = [
    [200, "Bad answer"],
    [204, "Bad answer"],
    [302, "Bad answer"],
    [401, "Refused"],
    [403, "Refused"],
    [404, "Not found"],
    [408, "No answer"],
    [429, "Rate limited"],
    [504, "No answer"],
    [502, "Their side failed"],
    [500, "Their side failed"],
  ];

  for (const [status, expected] of cases) {
    it(`says "${expected}" for ${status}`, () => {
      expect(statusWords(status)).toBe(expected);
    });
  }

  // Three different things to do about them, so three different words. A
  // redirect nobody followed and a request asked to wait were both "Refused",
  // which is the far end saying no and is what neither of them did.
  it("never reports an answer or a wait as a refusal", () => {
    for (const status of [200, 302, 429]) {
      expect(statusWords(status)).not.toBe("Refused");
    }
  });

  // A status only lands here from a health message, which a plugin writes when
  // something went wrong. A green chip beside a plugin that is not serving was
  // the number being read on its own.
  it("never colours a status as good, and keeps a transient one off problem", () => {
    for (const status of [200, 302, 408, 429]) {
      expect(statusTone(status)).toBe("attention");
    }
    for (const status of [401, 404, 500, 502]) {
      expect(statusTone(status)).toBe("problem");
    }
  });
});

describe("a plugin's health message, taken apart", () => {
  it("finds the status, the reference, and the first sentence", () => {
    const h = parseHealth("/health timed out inside Textable (Request timeout (HTTP 408, Textable reference 2f63e0e4-f79a-4a8e-a7d6-546ad2e75d98)). This is not transient: the list is built in one response. Do not retry.");
    expect(h.status).toBe(408);
    expect(h.reference).toBe("2f63e0e4-f79a-4a8e-a7d6-546ad2e75d98");
    expect(h.title).toBe("/health timed out inside Textable (Request timeout (HTTP 408, Textable reference 2f63e0e4-f79a-4a8e-a7d6-546ad2e75d98)).");
    expect(h.body).toBe("This is not transient: the list is built in one response. Do not retry.");
  });

  it("copes with a message that has none of them", () => {
    const h = parseHealth("waiting on a setting");
    expect(h.status).toBeUndefined();
    expect(h.reference).toBeUndefined();
    expect(h.title).toBe("waiting on a setting");
    expect(h.body).toBe("");
  });
});
