import { beforeEach, describe, expect, it } from "vitest";
import { noteInteraction, recentlyInteracted, resetInteraction, watchInteraction } from "./activity";

describe("telling a person from a poll", () => {
  beforeEach(() => resetInteraction());

  /**
   * The console refreshes several pages on a timer. If traffic counted as
   * presence, a window left open on one would hold a session open for ever
   * and the idle timeout would never once fire.
   */
  it("goes quiet once nobody has done anything for a while", () => {
    const at = Date.now();
    resetInteraction(at);
    expect(recentlyInteracted(at + 30_000)).toBe(true);
    expect(recentlyInteracted(at + 61_000)).toBe(false);
  });

  it("counts a pointer or a key as somebody being here", () => {
    const stop = watchInteraction();
    resetInteraction(Date.now() - 120_000);
    expect(recentlyInteracted()).toBe(false);

    document.dispatchEvent(new KeyboardEvent("keydown", { key: "a" }));
    expect(recentlyInteracted()).toBe(true);
    stop();
  });

  /**
   * Mouse movement deliberately does not count: a sleeping laptop with a cat
   * on the desk is not somebody working, and a jittery pointer would hold a
   * session open all night.
   */
  it("does not count the mouse merely moving", () => {
    const stop = watchInteraction();
    resetInteraction(Date.now() - 120_000);

    document.dispatchEvent(new MouseEvent("mousemove"));
    expect(recentlyInteracted()).toBe(false);
    stop();
  });

  it("stops listening when told to", () => {
    const stop = watchInteraction();
    stop();
    resetInteraction(Date.now() - 120_000);
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "a" }));
    expect(recentlyInteracted()).toBe(false);
  });

  it("notes an interaction reported directly, as navigation does", () => {
    resetInteraction(Date.now() - 120_000);
    expect(recentlyInteracted()).toBe(false);
    noteInteraction();
    expect(recentlyInteracted()).toBe(true);
  });
});
