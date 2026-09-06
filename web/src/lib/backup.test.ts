import { describe, expect, it } from "vitest";
import type { BackupDestination } from "./api";
import { lastRunWords, retentionWords, runTone, sizeWords } from "./backup";

describe("what a retention policy says it keeps", () => {
  // Keeping the last N is the whole policy for most people, and the sentence
  // for that case has nothing else in it.
  it("names only the count when no period rule is on", () => {
    expect(retentionWords({
      keep_last: 6, keep_daily: 0, keep_weekly: 0, keep_monthly: 0,
    })).toBe("Keeps the last 6.");
  });

  // A rule set to zero does nothing. Rendering it would describe a rule that
  // is off as though it were keeping something.
  it("leaves out a period rule that is switched off", () => {
    expect(retentionWords({
      keep_last: 6, keep_daily: 7, keep_weekly: 0, keep_monthly: 0,
    })).toBe("Keeps the last 6, and the newest in each of the last 7 days.");
  });

  it("joins the rules that are on", () => {
    expect(retentionWords({
      keep_last: 3, keep_daily: 7, keep_weekly: 4, keep_monthly: 1,
    })).toBe(
      "Keeps the last 3, and the newest in each of the last 7 days, 4 weeks and 1 month.",
    );
  });
});

describe("a size a person reads", () => {
  // A run still going has no size. Zero rendered as "0 bytes" reads as a
  // backup of nothing rather than as a number that does not exist yet.
  it("is empty for nothing", () => {
    expect(sizeWords(0)).toBe("");
    expect(sizeWords(-1)).toBe("");
  });

  it("uses the coarsest unit that still says something", () => {
    expect(sizeWords(512)).toBe("512 bytes");
    expect(sizeWords(2048)).toBe("2.0 KB");
    expect(sizeWords(4 * 1024 * 1024)).toBe("4.0 MB");
    expect(sizeWords(3 * 1024 * 1024 * 1024)).toBe("3.0 GB");
  });

  // Past ten the fraction is noise, and a column of "412.3 MB" beside
  // "4.0 MB" is harder to compare than one of whole numbers.
  it("drops the fraction once the number is big enough not to need it", () => {
    expect(sizeWords(412 * 1024 * 1024)).toBe("412 MB");
  });
});

describe("what happened at a destination last time", () => {
  const base: BackupDestination = {
    id: "dst_1", name: "NAS", kind: "sftp", where: "nas.example.com",
    settings: {}, enabled: true,
    policy: { keep_last: 6, keep_daily: 0, keep_weekly: 0, keep_monthly: 0 },
    has_secret: true, created_at: "2026-09-01T00:00:00Z",
  };

  /**
   * Three states, not two. A destination added a minute ago has never run,
   * which is not the same fact as one that ran and failed -- and colouring the
   * first as a problem sends somebody looking for one that is not there.
   */
  it("does not colour a destination that has never run", () => {
    expect(lastRunWords(base).tone).toBe("neutral");
    expect(lastRunWords(base).words).toBe("No backup has been sent here yet.");
  });

  it("uses the host's own sentence for a failure", () => {
    const failed = { ...base, last_ok: false, last_error: "The bucket refused the upload." };
    expect(lastRunWords(failed)).toEqual({
      tone: "problem", words: "The bucket refused the upload.",
    });
  });

  it("still says something when a failure carried no sentence", () => {
    const failed = { ...base, last_ok: false };
    expect(lastRunWords(failed).words).toBe(
      "The last backup did not reach this destination.");
  });
});

describe("how a run's outcome is coloured", () => {
  /**
   * `interrupted` is attention and never problem, for the reason the host
   * refuses to call it a failure: mcpd stopped while the run was going, so a
   * write may have landed. Painting it as failed invites a second run
   * presented as a first.
   */
  it("does not paint an interrupted run as a failure", () => {
    expect(runTone("interrupted")).toBe("attention");
    expect(runTone("failed")).toBe("problem");
  });

  // Some destinations took it and some did not. There is a backup; it is not
  // everywhere it should be, and neither green nor red says that.
  it("paints a partial run as neither worked nor failed", () => {
    expect(runTone("partial")).toBe("attention");
    expect(runTone("ok")).toBe("good");
  });

  // A status this build does not know renders uncoloured, which is the signal
  // the page and the host have drifted apart.
  it("does not guess at a status it does not know", () => {
    expect(runTone("something-new")).toBe("neutral");
  });
});
