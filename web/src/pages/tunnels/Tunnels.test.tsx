import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, type ChatGPTAccount, type TunnelInfo, type TunnelStatus } from "@/lib/api";
import { renderWith, sessionFor } from "@/test/render";
import { Liveness, Tunnels } from "./Tunnels";

function account(overrides: Partial<ChatGPTAccount> = {}): ChatGPTAccount {
  return {
    id: "acct_1", name: "Work", principal: "svc:chatgpt:work", role: "user", plugins: ["*"],
    rate_per_sec: 0, enabled: true, organization_id: "org_1", has_admin_key: true,
    can_manage: true, created_at: "2026-08-27T09:00:00Z", workspaces: ["ws_1"], ...overrides,
  };
}

function status(overrides: Partial<TunnelStatus> = {}): TunnelStatus {
  return { state: "connected", tunnel_id: "tunnel_a", plugin: "graylog", requests: 0, degraded: false, ...overrides };
}

function info(overrides: Partial<TunnelInfo> = {}): TunnelInfo {
  return {
    tunnels: [status()],
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
    window.localStorage.clear();
  });

  // An account is an organisation and a tunnel lives in exactly one, so the
  // heading says which rather than a column in one long table.
  it("lists each account's tunnels under its own heading", async () => {
    vi.spyOn(api, "tunnel").mockResolvedValue(info());
    renderWith(<Tunnels />);
    const work = (await screen.findByRole("heading", { name: "Work" })).closest("section")!;
    const lab = screen.getByRole("heading", { name: "Lab" }).closest("section")!;
    expect(within(work).getByText("mcpd: graylog")).toBeInTheDocument();
    expect(within(work).queryByText("mcpd: echo")).not.toBeInTheDocument();
    expect(within(lab).getByText("mcpd: echo")).toBeInTheDocument();
    expect(within(work).getByText(/svc:chatgpt:work/)).toBeInTheDocument();
  });

  it("restarts a tunnel from its row", async () => {
    vi.spyOn(api, "tunnel").mockResolvedValue(info());
    const restart = vi.spyOn(api, "restartTunnel").mockResolvedValue({ status: "restarted", tunnels: [] });
    renderWith(<Tunnels />);
    await userEvent.click(await screen.findByRole("button", { name: /Restart/ }));
    await waitFor(() => expect(restart).toHaveBeenCalledWith("tunnel_a"));
  });

  // mcpd's half is done when the tunnel is made; the notice is the other
  // half, and the first request through the tunnel is what ends it.
  it("walks through the ChatGPT step for a tunnel it made, until ChatGPT connects", async () => {
    const t = vi.spyOn(api, "tunnel").mockResolvedValue(info());
    vi.spyOn(api, "createTunnel").mockResolvedValue({ id: "tunnel_a", name: "mcpd: graylog" } as never);
    renderWith(<Tunnels />);
    await screen.findByRole("heading", { name: "Work" });
    await userEvent.click(screen.getByRole("button", { name: "Make" }));
    expect(await screen.findByText(/Finish in ChatGPT/)).toBeInTheDocument();
    expect(window.localStorage.getItem("mcpd.tunnels.awaiting")).toContain("tunnel_a");

    // The next poll shows a request came through.
    t.mockResolvedValue(info({ tunnels: [status({ requests: 3, last_request_at: "2026-09-02T10:00:00Z" })] }));
    await waitFor(() => expect(screen.queryByText(/Finish in ChatGPT/)).not.toBeInTheDocument(), { timeout: 12_000 });
    expect(window.localStorage.getItem("mcpd.tunnels.awaiting")).toBeNull();
  }, 15_000);

  it("offers an operator the state and nothing to press", async () => {
    vi.spyOn(api, "tunnel").mockResolvedValue(info({ can_manage: false, available: undefined }));
    renderWith(<Tunnels />, { session: sessionFor("user") });
    await screen.findByText("Ready");
    expect(screen.queryByRole("button", { name: /Restart/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Remove" })).not.toBeInTheDocument();
  });
});

// "Connected" is decided once, on the first poll. What is said beside it is
// the part that changes, and each case is a different thing to do.
describe("what a tunnel is doing", () => {
  const cases: [string, TunnelStatus, RegExp][] = [
    ["nothing sent yet", status({ connected_at: new Date().toISOString() }), /has not sent anything/],
    ["requests served", status({ requests: 12, last_request_at: new Date().toISOString() }), /12 requests/],
    ["degraded", status({ degraded: true, trouble: "poll failed; backing off" }), /reporting errors with nothing served/],
    ["retrying", status({ state: "failed", attempts: 3, next_retry_at: new Date(Date.now() + 60_000).toISOString(), message: "unreachable" }), /Retrying \(attempt 3\)/],
    ["stopped for good", status({ state: "failed", message: "OpenAI did not recognise that key" }), /will not restart on its own/],
    ["gone from OpenAI", status({ upstream: "missing" }), /Gone from OpenAI/],
  ];
  it.each(cases)("says so when %s", (_, s, want) => {
    renderWith(<Liveness status={s} unassigned={false} waitingOn="" />);
    expect(screen.getByText(want)).toBeInTheDocument();
  });
});
