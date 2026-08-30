import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, ApiError, type Caller, type ToolCall } from "@/lib/api";
import { renderWith } from "@/test/render";
import { Activity } from "./Activity";

function call(overrides: Partial<ToolCall> = {}): ToolCall {
  return {
    id: 1,
    at: "2026-08-29T12:00:00Z",
    principal: "key:abc",
    role: "user",
    plugin: "graylog",
    tool: "search_messages",
    outcome: "ok",
    duration_us: 42_000,
    ...overrides,
  };
}

function caller(overrides: Partial<Caller> = {}): Caller {
  return {
    principal: "key:abc",
    role: "user",
    calls: 12,
    errors: 0,
    denied: 0,
    first_seen: "2026-08-29T09:00:00Z",
    last_seen: "2026-08-29T12:00:00Z",
    plugins: ["graylog"],
    ...overrides,
  };
}

function stub(calls: ToolCall[], callers: Caller[], next = "") {
  vi.spyOn(api, "calls").mockResolvedValue({ calls, count: calls.length, next });
  vi.spyOn(api, "callers").mockResolvedValue({ callers, count: callers.length, days: 1 });
}

describe("the activity page", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    window.localStorage.clear();
  });

  it("names who made a call, not just that one happened", async () => {
    stub([call()], [caller()]);
    renderWith(<Activity />);

    expect(await screen.findAllByText("key:abc")).not.toHaveLength(0);
    expect(screen.getByText("graylog_search_messages")).toBeInTheDocument();
  });

  // What a credential reached is not what it is permitted to reach, and the
  // gap between those is the interesting part of a grant review.
  it("shows what each caller actually reached", async () => {
    stub([call()], [caller({ plugins: ["graylog", "observium"] })]);
    renderWith(<Activity />);

    const summary = await screen.findByRole("table", { name: "Callers" });
    expect(within(summary).getByText("graylog")).toBeInTheDocument();
    expect(within(summary).getByText("observium")).toBeInTheDocument();
  });

  // A refused call is a fact about who reached for what. Hiding refusals would
  // drop exactly the rows worth reading.
  it("shows refusals as well as successes", async () => {
    stub(
      [call({ id: 2, outcome: "denied", duration_us: undefined })],
      [caller({ denied: 3 })],
    );
    renderWith(<Activity />);

    expect(await screen.findByText("denied")).toBeInTheDocument();
  });

  // Zero is a call refused before it ran, not a call that took no time.
  // Rendering "0 ms" would read as a measurement that never happened.
  it("does not report a duration for a call that never ran", async () => {
    stub([call({ outcome: "denied", duration_us: undefined })], []);
    renderWith(<Activity />);

    expect(await screen.findByText("—")).toBeInTheDocument();
    expect(screen.queryByText("0 ms")).not.toBeInTheDocument();
  });

  // A call that ran faster than the resolution still ran. "0 ms" would read as
  // the same nothing the em dash means for a call that never happened.
  it("distinguishes a fast call from one that never ran", async () => {
    stub([call({ id: 3, outcome: "ok", duration_us: 63 })], []);
    renderWith(<Activity />);

    expect(await screen.findByText("<1 ms")).toBeInTheDocument();
    expect(screen.queryByText("—")).not.toBeInTheDocument();
  });

  it("narrows to one caller when their name is clicked", async () => {
    stub([call()], [caller()]);
    renderWith(<Activity />);

    const user = userEvent.setup();
    const summary = await screen.findByRole("table", { name: "Callers" });
    await user.click(within(summary).getByRole("button", { name: "key:abc" }));

    expect(await screen.findByRole("button", { name: /Clear filter/ })).toBeInTheDocument();
    expect(api.calls).toHaveBeenCalledWith(
      expect.objectContaining({ principal: "key:abc" }),
    );
  });

  // An empty ledger is the right state for a host nobody has used yet, and
  // should not read as something being broken.
  it("says an empty period is an ordinary state", async () => {
    stub([], []);
    renderWith(<Activity />);

    expect(await screen.findByText(/Nothing has called a tool/)).toBeInTheDocument();
  });

  it("reports a host that is not keeping a record", async () => {
    vi.spyOn(api, "calls").mockRejectedValue(
      new ApiError(501, "not_configured", "this host is not keeping a record of tool calls"),
    );
    vi.spyOn(api, "callers").mockRejectedValue(
      new ApiError(501, "not_configured", "this host is not keeping a record of tool calls"),
    );
    renderWith(<Activity />);

    expect(await screen.findByText(/not keeping a record/)).toBeInTheDocument();
  });
});
