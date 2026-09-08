import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { noteInteraction, resetActivity, watchInteraction } from "./activity";

describe("telling the host somebody is still here", () => {
  beforeEach(() => resetActivity());
  afterEach(() => resetActivity());

  /**
   * The console refreshes several pages on a timer. If traffic counted as
   * presence, a window left open on one would hold a session open for ever
   * and the idle timeout would never once fire -- so nothing is reported
   * until somebody actually does something.
   */
  it("reports nothing until somebody does something", () => {
    const report = vi.fn();
    const stop = watchInteraction(report);
    expect(report).not.toHaveBeenCalled();

    document.dispatchEvent(new KeyboardEvent("keydown", { key: "a" }));
    expect(report).toHaveBeenCalledTimes(1);
    stop();
  });

  /** A burst of clicking is one person, and should be one request. */
  it("reports at most once a minute however much happens", () => {
    const report = vi.fn();
    const stop = watchInteraction(report);

    const at = Date.now();
    for (let i = 0; i < 20; i++) noteInteraction(at + i * 100);
    expect(report).toHaveBeenCalledTimes(1);

    noteInteraction(at + 61_000);
    expect(report).toHaveBeenCalledTimes(2);
    stop();
  });

  /**
   * A sleeping laptop with a cat on the desk is not somebody working, and a
   * jittery pointer would hold a session open all night.
   */
  it("does not count the mouse merely moving", () => {
    const report = vi.fn();
    const stop = watchInteraction(report);

    document.dispatchEvent(new MouseEvent("mousemove"));
    expect(report).not.toHaveBeenCalled();
    stop();
  });

  it("stops reporting when told to", () => {
    const report = vi.fn();
    watchInteraction(report)();

    document.dispatchEvent(new KeyboardEvent("keydown", { key: "a" }));
    expect(report).not.toHaveBeenCalled();
  });

  /** A remount must not end up with two listeners reporting the same click. */
  it("replaces a previous listener rather than adding a second", () => {
    const first = vi.fn();
    const second = vi.fn();
    watchInteraction(first);
    const stop = watchInteraction(second);

    document.dispatchEvent(new KeyboardEvent("keydown", { key: "a" }));
    expect(first).not.toHaveBeenCalled();
    expect(second).toHaveBeenCalledTimes(1);
    stop();
  });

  /** Navigating is somebody doing something, and touches no pointer or key. */
  it("reports a navigation reported directly", () => {
    const report = vi.fn();
    const stop = watchInteraction(report);
    noteInteraction();
    expect(report).toHaveBeenCalledTimes(1);
    stop();
  });
});
