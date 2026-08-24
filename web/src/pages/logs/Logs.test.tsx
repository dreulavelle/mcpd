import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { act } from "react";
import { Logs } from "./Logs";

/**
 * A stand-in for the browser's own EventSource.
 *
 * The page is a consumer of a stream it does not control, so the tests drive
 * the stream rather than the page: what arrives, in what order, and when.
 */
class FakeEventSource {
  static last: FakeEventSource | null = null;

  onopen: (() => void) | null = null;
  onmessage: ((e: { data: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  closed = false;
  private listeners: Record<string, ((e: { data: string }) => void)[]> = {};

  constructor(public url: string) {
    FakeEventSource.last = this;
  }

  addEventListener(type: string, fn: (e: { data: string }) => void) {
    (this.listeners[type] ??= []).push(fn);
  }

  close() { this.closed = true; }

  /** One record, as the host would render it. */
  send(record: Record<string, unknown>) {
    act(() => this.onmessage?.({ data: JSON.stringify(record) }));
  }

  emit(type: string, data: string) {
    act(() => this.listeners[type]?.forEach((fn) => fn({ data })));
  }

  open() { act(() => this.onopen?.()); }
}

function mount() {
  vi.stubGlobal("EventSource", FakeEventSource);
  render(<Logs />);
  const source = FakeEventSource.last;
  if (!source) throw new Error("the page never opened a stream");
  source.open();
  return source;
}

const AT = "2026-08-24T01:10:37.168Z";

afterEach(() => { vi.unstubAllGlobals(); FakeEventSource.last = null; });

describe("the logs page", () => {
  it("shows what the stream sends", () => {
    const source = mount();
    source.send({ time: AT, level: "INFO", msg: "tunnel connected" });

    expect(screen.getByText("tunnel connected")).toBeInTheDocument();
  });

  // A coloured word is easy to miss at this size while scrolling past
  // hundreds of lines. The row is what makes a problem findable.
  it("washes a problem across the whole row, not just the level", () => {
    const source = mount();
    source.send({ time: AT, level: "ERROR", msg: "could not reach that provider" });

    const row = screen.getByText("could not reach that provider").closest("div");
    expect(row?.className).toContain("bg-problem-soft");
  });

  // Most lines are INFO, so tinting those rows would be the same as tinting
  // none of them.
  it("leaves an ordinary line untinted", () => {
    const source = mount();
    source.send({ time: AT, level: "INFO", msg: "nothing remarkable" });

    const row = screen.getByText("nothing remarkable").closest("div");
    expect(row?.className).not.toContain("bg-problem-soft");
    expect(row?.className).not.toContain("bg-attention-soft");
  });

  // "Which part of the host is talking" is the question being asked while
  // scanning, and answering it from a pair buried among the others means
  // reading the whole line to find out whether it is worth reading.
  it("puts the source in front of the message, and does not say it twice", () => {
    const source = mount();
    source.send({
      time: AT, level: "INFO", msg: "poller started",
      component: "tunnel", tunnel: "echo",
    });

    const row = screen.getByText("poller started").closest("div")!;
    expect(within(row).getByText("tunnel")).toBeInTheDocument();
    // The chip's own key is gone from the attributes; the second source key is
    // not, because "the tunnel component, for the echo tunnel" is two facts.
    expect(row.textContent).not.toContain("component=");
    expect(row.textContent).toContain("tunnel=echo");
  });

  it("filters to a level and everything worse", async () => {
    const source = mount();
    source.send({ time: AT, level: "INFO", msg: "ordinary" });
    source.send({ time: AT, level: "ERROR", msg: "alarming" });

    await userEvent.selectOptions(screen.getByLabelText("Level"), "WARN");

    expect(screen.getByText("alarming")).toBeInTheDocument();
    expect(screen.queryByText("ordinary")).toBeNull();
  });

  it("filters on what a line contains, attributes included", async () => {
    const source = mount();
    source.send({ time: AT, level: "INFO", msg: "started", plugin: "cnmaestro" });
    source.send({ time: AT, level: "INFO", msg: "started", plugin: "echo" });

    await userEvent.type(screen.getByLabelText("Containing"), "cnmaestro");

    expect(screen.getByText("cnmaestro")).toBeInTheDocument();
    expect(screen.queryByText("echo")).toBeNull();
  });

  // Pausing is for reading something before it scrolls away, so what arrives
  // while paused must not push it off the screen.
  it("stops taking lines while paused, and takes them again after", async () => {
    const source = mount();
    source.send({ time: AT, level: "INFO", msg: "before pausing" });

    await userEvent.click(screen.getByRole("button", { name: "Pause" }));
    source.send({ time: AT, level: "INFO", msg: "while paused" });
    expect(screen.queryByText("while paused")).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: "Resume" }));
    source.send({ time: AT, level: "INFO", msg: "after resuming" });
    expect(screen.getByText("after resuming")).toBeInTheDocument();
    expect(screen.getByText("before pausing")).toBeInTheDocument();
  });

  // A gap in a log with nothing marking it reads as "nothing happened", which
  // is worse than no log at all.
  it("marks the lines it was too slow to receive", () => {
    const source = mount();
    source.emit("dropped", "12");

    expect(screen.getByText(/12 lines not shown/)).toBeInTheDocument();
  });

  // The host restarting is the ordinary reason the connection drops, and
  // EventSource reconnects on its own. A red notice on every deploy would
  // teach somebody to ignore the one that matters.
  it("says it is reconnecting rather than raising an error", () => {
    const source = mount();
    act(() => source.onerror?.());

    expect(screen.getByText(/Reconnecting/)).toBeInTheDocument();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("closes the stream when the page goes away", () => {
    vi.stubGlobal("EventSource", FakeEventSource);
    const { unmount } = render(<Logs />);
    const source = FakeEventSource.last!;
    unmount();
    expect(source.closed).toBe(true);
  });
});
