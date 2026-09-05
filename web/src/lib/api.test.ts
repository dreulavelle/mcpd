import { describe, expect, it } from "vitest";
import { ApiError, problemText } from "./api";

// One rule for what a failed request says to a person, decided here rather
// than at the sixty call sites that each used to read `e.detail`.
describe("problemText", () => {
  it("shows a refusal, because it is the answer", () => {
    expect(problemText(new ApiError(400, "bad_request", `there is no system called "graylog"`), "no"))
      .toBe(`there is no system called "graylog"`);
  });

  // 501, 502 and 503 are not "something went wrong inside": they say what
  // this host does not do, or what the far end said. Treating everything
  // over 500 as unshowable lost the only actionable sentence on three pages.
  it.each([
    [501, "this host is not keeping a record of tool calls"],
    [502, "the registry could not be read just now"],
    [503, "settings are unavailable"],
  ])("shows what a %d means", (status, detail) => {
    expect(problemText(new ApiError(status, "x", detail), "no")).toBe(detail);
  });

  // A 500 is the one body nobody can act on. The correlation id is the only
  // thing somebody on a machine you cannot reach can quote back, so it is
  // said out loud rather than left in a response body nothing renders.
  it("replaces a 500 and names the reference", () => {
    expect(problemText(new ApiError(500, "internal", "sqlite: begin: locked", "abc123"), "Couldn't save."))
      .toBe("Couldn't save. Reference abc123.");
  });

  it("says only the fallback for a 500 with no reference", () => {
    expect(problemText(new ApiError(500, "internal", "boom"), "Couldn't save."))
      .toBe("Couldn't save.");
  });

  it("says the fallback for something that never reached the server", () => {
    expect(problemText(new TypeError("Failed to fetch"), "Couldn't save.")).toBe("Couldn't save.");
  });
});
