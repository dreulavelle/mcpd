import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { act } from "react";
import { Logs, parseLogfmtForTest } from "./Logs";

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

/**
 * Records this host actually relayed, copied verbatim off a running instance.
 *
 * A parser written against an imagined format is a parser tested against the
 * imagination. The long one is the worst real case: six hundred characters, a
 * quoted message, three URLs, and the same key twice.
 */
const REAL = {
  longest: "time=2026-08-24T02:10:31.594Z level=INFO msg=\"TunnelServiceClient created\" client_instance_id=be670359a1caa2f99e85204b70ec9aa2 tunnel_id=tunnel_6a88a00b4a588191be93a1b68521cbd2 component=controlplane tunnel_id=tunnel_6a88a00b4a588191be93a1b68521cbd2 poll_endpoint=https://api.openai.com/v1/tunnels/tunnel_6a88a00b4a588191be93a1b68521cbd2/poll response_endpoint=https://api.openai.com/v1/tunnels/tunnel_6a88a00b4a588191be93a1b68521cbd2/response metadata_endpoint=https://api.openai.com/v1/tunnels/tunnel_6a88a00b4a588191be93a1b68521cbd2 poll_timeout_ms=30000 poll_deadline_guardrail_ms=5000 poll_deadline_ms=35000",
  emptyValue: "time=2026-08-24T02:10:31.592Z level=INFO msg=\"mcp channel route resolved\" client_instance_id=be670359a1caa2f99e85204b70ec9aa2 tunnel_id=tunnel_6a88a00b4a588191be93a1b68521cbd2 component=mcpclient channel=main transport=in-memory mtls_enabled=false route_kind=mcp_channel route_name=main target_host=\"\" route_mode=direct proxy_source=none",
};

describe("reading a relayed record", () => {
  it("finds the message inside the longest one this host produces", () => {
    const got = parseLogfmtForTest(REAL.longest);
    expect(got.msg).toBe("TunnelServiceClient created");
    expect(got.level).toBe("INFO");
    expect(got.poll_timeout_ms).toBe("30000");
    // A URL is full of the characters a careless split would break on.
    expect(got.poll_endpoint).toContain("https://api.openai.com/v1/tunnels/");
  });

  // The client repeats tunnel_id in the same record. Last wins, which is the
  // ordinary reading and which matters only because they agree.
  it("survives the same key appearing twice", () => {
    const got = parseLogfmtForTest(REAL.longest);
    expect(got.tunnel_id).toBe("tunnel_6a88a00b4a588191be93a1b68521cbd2");
  });

  it("reads an empty quoted value as empty rather than as the next key", () => {
    const got = parseLogfmtForTest(REAL.emptyValue);
    expect(got.target_host).toBe("");
    expect(got.route_mode).toBe("direct");
  });

  it("keeps an escaped quote inside a message", () => {
    const got = parseLogfmtForTest(String.raw`level=WARN msg="he said \"no\"" k=v`);
    expect(got.msg).toBe('he said "no"');
    expect(got.k).toBe("v");
  });
});

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
    // Which tunnel, not "a tunnel": on a busy host most lines are tunnel
    // lines, so the generic word tells a reader nothing they cannot see.
    expect(within(row).getByText("echo")).toBeInTheDocument();
    expect(row.textContent).not.toContain("tunnel=echo");
    // Every other key stays, the chip's alone is dropped.
    expect(row.textContent).toContain("component=tunnel");
  });

  // The tunnel client logs a whole record of its own, which this host relays
  // as one attribute. Left as it arrived, every one of those is a row whose
  // message is the constant "tunnel-client" and whose content is six hundred
  // characters of grey.
  it("unwraps a record relayed inside an attribute", () => {
    const source = mount();
    source.send({
      time: AT, level: "INFO", msg: "tunnel-client",
      component: "tunnel", tunnel: "echo",
      line: 'time=2026-08-24T02:10:31.592Z level=INFO msg="mcp channel route resolved" '
        + "client_instance_id=be670359 tunnel_id=tunnel_6a88 component=mcpclient "
        + 'channel=main target_host=""',
    });

    expect(screen.getByText("mcp channel route resolved")).toBeInTheDocument();
    expect(screen.queryByText("tunnel-client")).toBeNull();

    const row = screen.getByText("mcp channel route resolved").closest("div")!;
    expect(row.textContent).toContain("channel=main");
    // The instance id is one constant repeated on every line of a run, and the
    // tunnel is already the chip.
    expect(row.textContent).not.toContain("client_instance_id");
    expect(row.textContent).not.toContain("tunnel_id");
  });

  // The wrapper is INFO whatever the record inside it says, so a tunnel error
  // arrived looking ordinary and never turned red.
  it("takes the relayed record's own level", () => {
    const source = mount();
    source.send({
      time: AT, level: "INFO", msg: "tunnel-client", component: "tunnel",
      line: 'time=2026-08-24T02:10:31.592Z level=ERROR msg="poll failed"',
    });

    const row = screen.getByText("poll failed").closest("div")!;
    expect(row.className).toContain("bg-problem-soft");
  });

  // A record with no message is not one this page can improve on, and
  // replacing the outer line with nothing would lose it entirely.
  it("leaves an unparseable relay alone", () => {
    const source = mount();
    source.send({
      time: AT, level: "WARN", msg: "tunnel-client",
      line: "not logfmt at all, just prose",
    });

    expect(screen.getByText("tunnel-client")).toBeInTheDocument();
  });

  // The summary is one line so the screen holds forty records rather than
  // eight; everything the record carried is a click away.
  it("opens a line onto everything it carried", async () => {
    const source = mount();
    source.send({
      time: AT, level: "INFO", msg: "plugin registered",
      plugin: "cnmaestro", endpoint: "/mcp/cnmaestro", tools: 17,
    });

    const row = screen.getByText("plugin registered").closest("div")!;
    const summary = within(row).getByRole("button");

    expect(summary).toHaveAttribute("aria-expanded", "false");
    expect(row.querySelector("dl")).toBeNull();

    await userEvent.click(summary);
    expect(summary).toHaveAttribute("aria-expanded", "true");

    const detail = row.querySelector("dl");
    expect(detail).not.toBeNull();
    // Every attribute, as a pair -- which is the only honest place to put the
    // ones a record carries but a reader does not usually want.
    const terms = [...detail!.querySelectorAll("dt")].map((d) => d.textContent);
    expect(terms).toEqual(["endpoint", "tools"]);
    expect(detail!.textContent).toContain("/mcp/cnmaestro");

    await userEvent.click(summary);
    expect(row.querySelector("dl")).toBeNull();
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
