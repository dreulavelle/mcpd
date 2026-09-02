import { describe, expect, it } from "vitest";
import { asTextForTest as asText } from "./Logs";

// What "Copy this line" and "Save" hand over: the record as the host would
// have written it, so a support caller can quote the correlation id rather
// than describe a screenshot.
describe("a log line as text", () => {
  it("is time, level, message and every attribute", () => {
    expect(asText({
      time: "2026-09-01T10:00:00Z", level: "WARN", msg: "upstream refused",
      rest: { plugin: "graylog", correlation_id: "abc123", status: 503 },
    })).toBe('2026-09-01T10:00:00Z WARN upstream refused plugin=graylog correlation_id=abc123 status=503');
  });

  it("quotes a value with spaces, so the line reads back as one record", () => {
    expect(asText({ time: "", level: "INFO", msg: "ready", rest: { reason: "no key set" } }))
      .toBe('- INFO ready reason="no key set"');
  });
});
