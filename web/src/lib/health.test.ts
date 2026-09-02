import { describe, expect, it } from "vitest";
import { parseHealth } from "./health";

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
