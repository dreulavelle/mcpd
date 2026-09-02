import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, type ChatGPTAccount, type TunnelInfo, type TunnelStatus } from "@/lib/api";
import { renderWith, sessionFor } from "@/test/render";
import { reading, Tunnels } from "./Tunnels";

function account(overrides: Partial<ChatGPTAccount> = {}): ChatGPTAccount {
  return {
    id: "acct_1", name: "Work", principal: "svc:chatgpt:work", role: "user", plugins: ["*"],
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
  });

  // A page opened because something is wrong puts the wrong thing at the
  // top, and opens on it.
  it("lists worst first and opens on the worst", async () => {
    vi.spyOn(api, "tunnel").mockResolvedValue(info());
    renderWith(<Tunnels />);
    const list = await screen.findByRole("list", { name: "Tunnels" });
    const rows = within(list.parentElement!).getAllByRole("listitem");
    expect(rows[0]).toHaveTextContent("mcpd: echo");
    expect(rows[0]).toHaveTextContent("Stopped");
    expect(rows[0]).toHaveAttribute("aria-current", "true");
    // The inspector tells the story of the selected one.
    expect(screen.getByText(/It will not restart on its own/)).toBeInTheDocument();
    expect(screen.getByText(/connects as/)).toHaveTextContent("Lab");
  });

  it("selects a tunnel from its row and keeps the choice in the address", async () => {
    vi.spyOn(api, "tunnel").mockResolvedValue(info());
    renderWith(<Tunnels />);
    await userEvent.click((await screen.findAllByText("mcpd: graylog"))[0]!.closest("[role=listitem]")!);
    expect(window.location.search).toBe("?tunnel=tunnel_a");
    expect(screen.getByRole("heading", { name: "mcpd: graylog" })).toBeInTheDocument();
  });

  it("narrows to what needs somebody from the chips", async () => {
    vi.spyOn(api, "tunnel").mockResolvedValue(info());
    renderWith(<Tunnels />);
    await screen.findByRole("list", { name: "Tunnels" });
    await userEvent.click(screen.getByRole("button", { name: /Ready 1/ }));
    const list = screen.getByRole("list", { name: "Tunnels" }).parentElement!;
    const rows = within(list).getAllByRole("listitem");
    expect(rows).toHaveLength(1);
    expect(rows[0]).toHaveTextContent("mcpd: graylog");
  });

  it("restarts the selected tunnel from the inspector", async () => {
    vi.spyOn(api, "tunnel").mockResolvedValue(info());
    const restart = vi.spyOn(api, "restartTunnel").mockResolvedValue({ status: "restarted", tunnels: [] });
    renderWith(<Tunnels />);
    await userEvent.click(await screen.findByRole("button", { name: /Restart/ }));
    await waitFor(() => expect(restart).toHaveBeenCalledWith("tunnel_b"));
  });

  // mcpd's half is done when the tunnel is made; the setup steps carry the
  // other half, and the first request through is what ticks it.
  it("walks through the ChatGPT step for a connected tunnel nothing has used", async () => {
    vi.spyOn(api, "tunnel").mockResolvedValue(info({
      tunnels: [status({ requests: 0, last_request_at: undefined })],
      available: [{ id: "tunnel_a", name: "mcpd: graylog", account_id: "acct_1" }] as never,
    }));
    renderWith(<Tunnels />);
    expect((await screen.findAllByText(/Waiting for ChatGPT/)).length).toBeGreaterThan(0);
    expect(screen.getByText(/choose Create, pick/)).toBeInTheDocument();
  });

  it("offers an operator the state and nothing to press", async () => {
    vi.spyOn(api, "tunnel").mockResolvedValue(info({ can_manage: false, available: undefined }));
    renderWith(<Tunnels />, { session: sessionFor("user") });
    await screen.findByRole("list", { name: "Tunnels" });
    expect(screen.queryByRole("button", { name: /Restart/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Remove" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Make a tunnel/ })).not.toBeInTheDocument();
  });
});

// One table decides what a tunnel is, so the row, the inspector and the
// chips cannot disagree.
describe("what a tunnel is doing", () => {
  const row = (s: Partial<TunnelStatus> | null = {}, extra: Record<string, unknown> = {}) =>
    ({ id: "t", name: "t", account: "acct_1", assigned: "graylog", status: s === null ? undefined : status(s), ...extra }) as never;
  const one = [account()];
  it.each([
    ["gone from OpenAI", row({ upstream: "missing" }), "gone", 0],
    ["stopped for good", row({ state: "failed", message: "bad key" }), "stopped", 0],
    ["retrying", row({ state: "failed", attempts: 2, next_retry_at: new Date(Date.now() + 60_000).toISOString() }), "retrying", 1],
    ["degraded", row({ degraded: true }), "degraded", 2],
    ["waiting for ChatGPT", row({ requests: 0, last_request_at: undefined }), "attach", 4],
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
});
