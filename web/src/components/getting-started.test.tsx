import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, type MCPServer, type PluginInstance, type TunnelState } from "@/lib/api";
import { renderWith, sessionFor } from "@/test/render";
import { GettingStarted } from "./getting-started";

/**
 * The panel exists because a checklist that does not know what you have done
 * is a brochure. These tests defend the three things that follow from that: it
 * answers each step from real state, it takes itself away when there is
 * nothing left, and it costs a host that has just started one request rather
 * than five.
 */

const SERVER: MCPServer = {
  name: "weather", title: "Weather", description: "Forecasts.",
  version: "1.0.0", schema_version: "2025-07-09",
  transport: "streamable-http", url: "https://weather.example/mcp",
  enabled: true, mounted: false, readable: true,
  created_at: "2026-08-01T10:00:00Z", updated_at: "2026-08-20T10:00:00Z",
  pending: 0, enabled_tools: 0, disabled: 0,
};

const INSTANCE: PluginInstance = {
  name: "weather", type: "mcp", runtime: "mcp",
  from_file: false, enabled: true, mounted: true,
};

function stub({
  instances = [] as PluginInstance[],
  servers = [] as MCPServer[],
  tunnels = [] as TunnelState[],
  rules = 0,
  users = 1,
}= {}) {
  vi.spyOn(api, "instances").mockResolvedValue({
    instances, count: instances.length,
  });
  vi.spyOn(api, "mcpServers").mockResolvedValue({ servers });
  vi.spyOn(api, "tunnel").mockResolvedValue({
    tunnels: tunnels.map((state) => ({ state })),
    can_manage: true, plugins: [], workspaces: [], assignments: {},
  });
  vi.spyOn(api, "approvalPolicy").mockResolvedValue({
    rules: Array.from({ length: rules }, (_, i) => ({
      id: `r${i}`, plugin: "*", action: "*", principal: "*", max_risk: "low",
    })),
    wildcard: "*", ceilings: ["low"], default: "Ask about everything.", unmatched: "none",
  });
  vi.spyOn(api, "users").mockResolvedValue({ users: [], count: users });
}

/**
 * The panel waits for the browser to be idle before it asks anything, so the
 * idle callback is stubbed rather than left to jsdom, which has none. Holding
 * the callbacks and running them by hand is what makes the deferral something
 * a test can assert about rather than a race against a timer.
 */
const idle: IdleRequestCallback[] = [];

beforeEach(() => {
  idle.length = 0;
  vi.stubGlobal("requestIdleCallback", (fn: IdleRequestCallback) =>
    idle.push(fn));
  vi.stubGlobal("cancelIdleCallback", () => {});
});

afterEach(() => vi.unstubAllGlobals());

/** Lets the deferred work run, and its answers land. */
async function settle() {
  await act(async () => {
    idle.splice(0).forEach((fn) => fn({ didTimeout: false, timeRemaining: () => 0 }));
    await Promise.resolve();
  });
}

/** A host with every step answered. */
function configured() {
  stub({
    instances: [INSTANCE],
    servers: [{ ...SERVER, enabled_tools: 3 }],
    tunnels: ["connected"],
    rules: 1,
    users: 2,
  });
}

beforeEach(() => {
  localStorage.clear();
});

describe("the getting-started panel", () => {
  it("is a button and nothing else until somebody opens it", async () => {
    stub();
    renderWith(<GettingStarted />);
    await settle();

    expect(screen.getByRole("button", { name: /get started/i })).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).toBeNull();
    // Never modal, and never in the way: opening is the operator's move.
    expect(document.body).toHaveFocus();
  });

  /**
   * The reason it is allowed on every page. Collapsed, it asks the one question
   * that can already answer "is this host finished" with a no; the other four
   * wait until somebody is reading the list.
   */
  it("asks one endpoint while it is collapsed, and the rest on opening", async () => {
    stub();
    renderWith(<GettingStarted />);
    await settle();

    expect(api.instances).toHaveBeenCalledTimes(1);
    expect(api.mcpServers).not.toHaveBeenCalled();
    expect(api.tunnel).not.toHaveBeenCalled();
    expect(api.approvalPolicy).not.toHaveBeenCalled();
    expect(api.users).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole("button", { name: /get started/i }));

    expect(await screen.findByText("Connect ChatGPT")).toBeInTheDocument();
    expect(api.tunnel).toHaveBeenCalledTimes(1);
  });

  it("sends each step to the page that completes it", async () => {
    stub();
    renderWith(<GettingStarted />);
    await settle();
    await userEvent.click(screen.getByRole("button", { name: /get started/i }));

    expect(await screen.findByRole("link", { name: /Connect ChatGPT/ }))
      .toHaveAttribute("href", "/tunnels");
    expect(screen.getByRole("link", { name: /Add an MCP server/ }))
      .toHaveAttribute("href", "/marketplace");
  });

  // A tool waiting to be classified is on one server's page, not on the list of
  // them, and the row knows which server it is talking about.
  it("points the tools step at the server whose tools are waiting", async () => {
    stub({ instances: [INSTANCE], servers: [{ ...SERVER, pending: 4 }] });
    renderWith(<GettingStarted />);
    await settle();
    await userEvent.click(screen.getByRole("button", { name: /get started/i }));

    expect(await screen.findByRole("link", { name: /Approve its tools/ }))
      .toHaveAttribute("href", "/plugins/weather");
  });

  /**
   * The whole point. Telling a host with a plugin and a live tunnel to add its
   * first plugin is what gets onboarding dismissed on the first day.
   */
  it("says which steps are already done, and counts them", async () => {
    stub({
      instances: [INSTANCE],
      servers: [{ ...SERVER, enabled_tools: 2 }],
      tunnels: ["connected"],
    });
    renderWith(<GettingStarted />);
    await settle();
    await userEvent.click(screen.getByRole("button", { name: /get started/i }));

    // The state is on the row as words rather than as a colour alone, so it is
    // there for a reader who cannot see the tick.
    expect(await screen.findByRole("link", { name: /Add an MCP server/ }))
      .toHaveTextContent("— done");
    expect(screen.getByRole("link", { name: /Connect ChatGPT/ }))
      .toHaveTextContent("— done");
    expect(screen.getByRole("link", { name: /Invite someone/ }))
      .toHaveTextContent("— still to do");
    expect(screen.getByText("3 of 5 done")).toBeInTheDocument();
  });

  /**
   * A host serving only built-in plugins has no third party's catalogue to
   * classify. Listing it anyway would leave one step permanently unfinished,
   * which is the same as never taking the panel away.
   */
  it("leaves out a step this host will never need", async () => {
    stub({ instances: [INSTANCE] });
    renderWith(<GettingStarted />);
    await settle();
    await userEvent.click(screen.getByRole("button", { name: /get started/i }));

    expect(await screen.findByText("Connect ChatGPT")).toBeInTheDocument();
    expect(screen.queryByText("Approve its tools")).toBeNull();
    expect(screen.getByText("1 of 4 done")).toBeInTheDocument();
  });

  it("stops rendering once there is nothing left to do", async () => {
    configured();
    renderWith(<GettingStarted />);
    await settle();

    expect(screen.queryByRole("button", { name: /get started/i })).toBeNull();
    // Remembered, so a finished host stops paying for the answer as well.
    expect(localStorage.getItem("mcpd.getting-started")).toBe("complete");
  });

  it("asks nothing at all on a browser that has already seen it finished", async () => {
    configured();
    localStorage.setItem("mcpd.getting-started", "complete");
    renderWith(<GettingStarted />);
    await settle();

    expect(api.instances).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: /get started/i })).toBeNull();
  });

  /**
   * A failed call is not evidence of anything. The row goes -- an error in the
   * corner of every page is worse than a shorter list -- but the panel must not
   * read the silence as a finished host and remember it for good.
   */
  it("hides a step it could not read, and does not call the host finished", async () => {
    configured();
    vi.spyOn(api, "users").mockRejectedValue(new Error("no"));
    renderWith(<GettingStarted />);
    await settle();
    await userEvent.click(screen.getByRole("button", { name: /get started/i }));

    expect(await screen.findByText("Connect ChatGPT")).toBeInTheDocument();
    expect(screen.queryByText("Invite someone")).toBeNull();
    expect(screen.queryByText(/could not/i)).toBeNull();
    expect(localStorage.getItem("mcpd.getting-started")).toBeNull();
  });

  /**
   * A host that has just refused one call is not asked four more times: the
   * panel is rendering either way, and the refusals cost the same as the
   * answers would have.
   */
  it("stops asking when a call is refused, rather than working through the rest", async () => {
    stub();
    vi.spyOn(api, "instances").mockRejectedValue(new Error("no"));
    renderWith(<GettingStarted />);
    await settle();

    expect(screen.getByRole("button", { name: /get started/i })).toBeInTheDocument();
    expect(api.tunnel).not.toHaveBeenCalled();
  });

  /**
   * Finishing the last step while the list is open must not make the list
   * disappear out from under whoever is reading it. It goes when they close it.
   */
  it("waits for the reader to close it before taking itself away", async () => {
    stub();
    renderWith(<GettingStarted />);
    await settle();

    configured();
    await userEvent.click(screen.getByRole("button", { name: /get started/i }));
    expect(await screen.findByText("5 of 5 done")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(screen.queryByRole("button", { name: /get started/i })).toBeNull();
    expect(localStorage.getItem("mcpd.getting-started")).toBe("complete");
  });

  it("does not offer steps to somebody who may not do them", async () => {
    stub();
    renderWith(<GettingStarted />, { session: sessionFor("user") });
    await settle();

    expect(screen.queryByRole("button", { name: /get started/i })).toBeNull();
    expect(api.instances).not.toHaveBeenCalled();
  });
});

describe("closing it", () => {
  beforeEach(() => stub());

  it("closes on Escape and gives the keyboard back where it came from", async () => {
    renderWith(<GettingStarted />);
    await settle();

    const trigger = screen.getByRole("button", { name: /get started/i });
    await userEvent.click(trigger);
    expect(await screen.findByRole("dialog")).toHaveFocus();

    await userEvent.keyboard("{Escape}");

    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.getByRole("button", { name: /get started/i })).toHaveFocus();
  });

  // The X collapses it. Losing the checklist for good on a stray click would
  // be a worse answer than the one thing it is there to prevent.
  it("collapses to the button again on the close control", async () => {
    renderWith(<GettingStarted />);
    await settle();
    await userEvent.click(screen.getByRole("button", { name: /get started/i }));
    await userEvent.click(await screen.findByRole("button", { name: "Close" }));

    expect(screen.getByRole("button", { name: /get started/i })).toBeInTheDocument();
    expect(localStorage.getItem("mcpd.getting-started")).toBeNull();
  });

  it("remembers a dismissal, so a reload does not bring it back", async () => {
    const first = renderWith(<GettingStarted />);
    await settle();
    await userEvent.click(screen.getByRole("button", { name: /get started/i }));
    await userEvent.click(await screen.findByRole("button", { name: /don't show this again/i }));

    expect(screen.queryByRole("button", { name: /get started/i })).toBeNull();
    expect(localStorage.getItem("mcpd.getting-started")).toBe("dismissed");

    first.unmount();
    renderWith(<GettingStarted />);
    await settle();
    expect(screen.queryByRole("button", { name: /get started/i })).toBeNull();
  });
});
