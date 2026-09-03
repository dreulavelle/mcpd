import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, type ChatGPTAccount, type TunnelInfo, type TunnelStatus } from "@/lib/api";
import { renderWith, sessionFor } from "@/test/render";
import { reading, Tunnels } from "./Tunnels";

function account(overrides: Partial<ChatGPTAccount> = {}): ChatGPTAccount {
  return {
    id: "acct_1", name: "Work", principal: "svc:chatgpt:work", role: "role_operator",
    role_name: "Operator", grants: [{ plugin: "*", level: "write" }], plugins: ["*"],
    rate_per_sec: 0, enabled: true, organization_id: "org_1", has_admin_key: true,
    can_manage: true, created_at: "2026-08-27T09:00:00Z", workspaces: ["ws_1"], ...overrides,
  };
}

function status(overrides: Partial<TunnelStatus> = {}): TunnelStatus {
  return { state: "connected", tunnel_id: "tunnel_a", plugin: "graylog", requests: 5, last_request_at: new Date().toISOString(), degraded: false, ...overrides };
}

function info(overrides: Partial<TunnelInfo> = {}): TunnelInfo {
  return {
    tunnels: [status(), status({ tunnel_id: "tunnel_b", plugin: "echo", state: "failed", message: "bad key", requests: 0, last_request_at: undefined })],
    can_manage: true,
    available: [
      { id: "tunnel_a", name: "mcpd: graylog", account_id: "acct_1" },
      { id: "tunnel_b", name: "mcpd: echo", account_id: "acct_2" },
    ] as never,
    assignments: { tunnel_a: "graylog", tunnel_b: "echo" },
    account_assignments: { tunnel_a: "acct_1", tunnel_b: "acct_2" },
    accounts: [account(), account({ id: "acct_2", name: "Lab", principal: "svc:chatgpt:lab" })],
    plugins: ["graylog", "echo"],
    workspaces: ["ws_1"],
    ...overrides,
  };
}

describe("the tunnels page", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    window.history.replaceState(null, "", "/tunnels");
    window.localStorage.clear();
  });

  // A page opened because something is wrong puts the wrong thing at the
  // top.
  it("lists worst first", async () => {
    vi.spyOn(api, "tunnel").mockResolvedValue(info());
    renderWith(<Tunnels />);
    const table = await screen.findByRole("table", { name: "Tunnels" });
    const rows = within(table).getAllByRole("row").slice(1);
    expect(rows[0]).toHaveTextContent("mcpd: echo");
    expect(rows[0]).toHaveTextContent("Stopped");
    expect(rows[1]).toHaveTextContent("mcpd: graylog");
  });

  // The detail is a sheet over the list, opened from the row, and the choice
  // lives in the address so a link can open the page on one tunnel.
  it("opens a tunnel's detail from its row and keeps the choice in the address", async () => {
    vi.spyOn(api, "tunnel").mockResolvedValue(info());
    renderWith(<Tunnels />);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await userEvent.click(await screen.findByRole("button", { name: "Details for mcpd: echo" }));
    const sheet = await screen.findByRole("dialog");
    expect(within(sheet).getByText("mcpd: echo")).toBeInTheDocument();
    expect(within(sheet).getByText(/It will not restart on its own/)).toBeInTheDocument();
    expect(within(sheet).getByText(/connects as/)).toHaveTextContent("Lab");
    expect(window.location.search).toBe("?tunnel=tunnel_b");
  });

  it("opens on the tunnel the address names", async () => {
    vi.spyOn(api, "tunnel").mockResolvedValue(info());
    renderWith(<Tunnels />, { path: "/tunnels?tunnel=tunnel_a" });
    const sheet = await screen.findByRole("dialog");
    expect(within(sheet).getByText("mcpd: graylog")).toBeInTheDocument();
  });

  it("narrows to what needs somebody from the chips", async () => {
    vi.spyOn(api, "tunnel").mockResolvedValue(info());
    renderWith(<Tunnels />);
    await screen.findByRole("table", { name: "Tunnels" });
    await userEvent.click(screen.getByRole("button", { name: /Ready 1/ }));
    const rows = within(screen.getByRole("table", { name: "Tunnels" })).getAllByRole("row").slice(1);
    expect(rows).toHaveLength(1);
    expect(rows[0]).toHaveTextContent("mcpd: graylog");
  });

  it("moves a tunnel to the account that owns it", async () => {
    vi.spyOn(api, "tunnel").mockResolvedValue(info({
      tunnels: [status({ tunnel_id: "tunnel_b", plugin: "echo", state: "failed", message: "refused", requests: 0, last_request_at: undefined })],
      available: [{ id: "tunnel_b", name: "mcpd: echo", account_id: "acct_2" }] as never,
      assignments: { tunnel_b: "echo" },
      account_assignments: { tunnel_b: "acct_1" },
    }));
    const assign = vi.spyOn(api, "assignTunnel").mockResolvedValue({ status: "assigned" });
    renderWith(<Tunnels />);
    expect((await screen.findAllByText("Wrong account")).length).toBeGreaterThan(0);
    await userEvent.click(screen.getByRole("button", { name: "Details for mcpd: echo" }));
    await userEvent.click(await screen.findByRole("button", { name: "Move to Lab" }));
    await waitFor(() => expect(assign).toHaveBeenCalledWith("tunnel_b", "echo", "acct_2"));
  });

  it("restarts a tunnel from its row without opening it", async () => {
    vi.spyOn(api, "tunnel").mockResolvedValue(info());
    const restart = vi.spyOn(api, "restartTunnel").mockResolvedValue({ status: "restarted", tunnels: [] });
    renderWith(<Tunnels />);
    await userEvent.click(await screen.findByRole("button", { name: "Restart mcpd: echo" }));
    await waitFor(() => expect(restart).toHaveBeenCalledWith("tunnel_b"));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  // mcpd's half is done when the tunnel is made; the setup steps carry the
  // other half, and the first request through is what ticks it.
  it("walks through the ChatGPT step for a tunnel it made, and only that one", async () => {
    window.localStorage.setItem("mcpd.tunnels.awaiting", JSON.stringify(["tunnel_a"]));
    vi.spyOn(api, "tunnel").mockResolvedValue(info({
      tunnels: [status({ requests: 0, last_request_at: undefined })],
      available: [{ id: "tunnel_a", name: "mcpd: graylog", account_id: "acct_1" }] as never,
    }));
    renderWith(<Tunnels />);
    expect((await screen.findAllByText(/Waiting for ChatGPT/)).length).toBeGreaterThan(0);
    await userEvent.click(screen.getByRole("button", { name: "Details for mcpd: graylog" }));
    expect(await screen.findByText(/choose Create, pick/)).toBeInTheDocument();
  });

  // A restart of mcpd starts every count again. A connector ChatGPT already
  // has, idle since, is ready -- not waiting to be attached.
  it("does not mistake an idle connector after a restart for one never attached", async () => {
    vi.spyOn(api, "tunnel").mockResolvedValue(info({
      tunnels: [status({ requests: 0, last_request_at: undefined, connected_at: new Date().toISOString() })],
      available: [{ id: "tunnel_a", name: "mcpd: graylog", account_id: "acct_1" }] as never,
    }));
    renderWith(<Tunnels />);
    expect((await screen.findAllByText("Ready")).length).toBeGreaterThan(0);
    expect(screen.queryByText(/Waiting for ChatGPT/)).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Details for mcpd: graylog" }));
    expect(await screen.findByText(/nothing has come through since mcpd started/)).toBeInTheDocument();
  });

  // The bars are a way in: a chart worth looking at is worth looking at
  // larger, with the errors beside it and the calls that made it.
  it("opens the metrics view from the bars, with the recent calls", async () => {
    vi.spyOn(api, "tunnel").mockResolvedValue(info({
      tunnels: [status({ activity: [0, 0, 0, 0, 0, 0, 0, 0, 2, 5, 3, 1], errors: [0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0] })],
      available: [{ id: "tunnel_a", name: "mcpd: graylog", account_id: "acct_1" }] as never,
    }));
    const calls = vi.spyOn(api, "calls").mockResolvedValue({
      calls: [{ id: 1, at: new Date().toISOString(), principal: "svc:chatgpt:work", plugin: "graylog", tool: "search_messages", outcome: "ok", duration_us: 1200 }],
      count: 1, next: "",
    });
    renderWith(<Tunnels />);
    await userEvent.click(await screen.findByRole("button", { name: "Metrics for mcpd: graylog" }));
    const sheet = await screen.findByRole("dialog");
    expect(within(sheet).getByText("Last twelve hours")).toBeInTheDocument();
    expect(await within(sheet).findByText("search_messages")).toBeInTheDocument();
    expect(calls).toHaveBeenCalledWith(expect.objectContaining({ principal: "svc:chatgpt:work", plugin: "graylog" }));
    expect(window.location.search).toContain("view=metrics");
  });

  it("offers an operator the state and nothing to press", async () => {
    vi.spyOn(api, "tunnel").mockResolvedValue(info({ can_manage: false, available: undefined }));
    renderWith(<Tunnels />, { session: sessionFor("user") });
    await screen.findByRole("table", { name: "Tunnels" });
    expect(screen.queryByRole("button", { name: /Restart/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Remove" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Make a tunnel/ })).not.toBeInTheDocument();
  });
});

// One table decides what a tunnel is, so the row, the inspector and the
// chips cannot disagree.
describe("what a tunnel is doing", () => {
  const row = (s: Partial<TunnelStatus> | null = {}, extra: Record<string, unknown> = {}) =>
    ({ id: "t", name: "t", account: "acct_1", owners: [], assigned: "graylog", status: s === null ? undefined : status(s), ...extra }) as never;
  const one = [account()];
  it.each([
    ["not in this organisation", row({ upstream: "missing" }), "gone", 0],
    ["stopped for good", row({ state: "failed", message: "bad key" }), "stopped", 0],
    ["retrying", row({ state: "failed", attempts: 2, next_retry_at: new Date(Date.now() + 60_000).toISOString() }), "retrying", 1],
    ["degraded", row({ degraded: true }), "degraded", 2],
    ["idle since a restart", row({ requests: 0, last_request_at: undefined }), "ready", 6],
    ["ready", row(), "ready", 6],
    ["not used", row(null, { assigned: undefined }), "unused", 8],
  ] as [string, never, string, number][])("reads %s", (_, r, kind, rank) => {
    const got = reading(r, ["graylog"], one);
    expect(got.kind).toBe(kind);
    expect(got.rank).toBe(rank);
  });

  it("puts a waiting plugin before a working tunnel and after a broken one", () => {
    const waiting = reading(row(null, { assigned: "observium" }), ["graylog"], one);
    expect(waiting.kind).toBe("waiting");
    expect(waiting.rank).toBeGreaterThan(reading(row({ degraded: true }), ["graylog"], one).rank);
    expect(waiting.rank).toBeLessThan(reading(row(), ["graylog"], one).rank);
  });

  it("says waiting for ChatGPT only of a tunnel this page made", () => {
    const idle = row({ requests: 0, last_request_at: undefined });
    expect(reading(idle, ["graylog"], one, new Set(["t"])).kind).toBe("attach");
    expect(reading(idle, ["graylog"], one, new Set()).kind).toBe("ready");
  });

  // The listing says who owns a tunnel and the assignment says who runs it;
  // when they differ the key is refused, and the remedy is a move.
  it("names the account that owns a tunnel assigned to the wrong one", () => {
    const two = [account(), account({ id: "acct_2", name: "Nick" })];
    const r = reading(row({ state: "failed", message: "refused" }, { account: "acct_1", owners: ["acct_2"] }), ["graylog"], two);
    expect(r.kind).toBe("elsewhere");
    expect(r.detail).toContain("belongs to Nick");
    expect(r.detail).toContain("Move it to Nick");
  });

  // A tunnel several organisations share may be run by any of their keys,
  // so an assignment to one of them is right, and a shared tunnel is one
  // row rather than one per account that lists it.
  it("accepts any of the accounts a shared tunnel belongs to", () => {
    const two = [account(), account({ id: "acct_2", name: "Nick" })];
    const shared = row({ state: "connected" }, { account: "acct_2", owners: ["acct_1", "acct_2"] });
    expect(reading(shared, ["graylog"], two).kind).not.toBe("elsewhere");
  });
});
