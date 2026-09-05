import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api } from "@/lib/api";
import { renderWith, sessionFor } from "@/test/render";
import { CommandPalette } from "./CommandPalette";

function stub() {
  vi.spyOn(api, "plugins").mockResolvedValue({
    plugins: [{
      name: "graylog", type: "graylog", version: "1", title: "Graylog", description: "",
      endpoint: "/mcp/graylog", connect_url: "", health: "healthy", tools: [],
      mutations: [], required: false, settings: [],
    }],
    count: 1,
  });
  vi.spyOn(api, "operations").mockResolvedValue({
    operations: [{
      id: "op_1", plugin: "cnmaestro", action: "device.reboot", state: "pending_approval",
      risk: "high", impact: "", requested_by: "svc:agent", requested_at: "2026-09-01T00:00:00Z",
      expires_at: "2026-09-02T00:00:00Z", assurance: "gated_call", drift_checked: false,
      outcome_verifiable: false, attempts: 0, terminal: false,
    }],
    count: 1,
  } as never);
  vi.spyOn(api, "tunnel").mockResolvedValue({
    tunnels: [], can_manage: false, accounts: [], plugins: [], workspaces: [],
  });
}

describe("the command palette", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    stub();
  });

  it("offers the pages, the plugins and the changes waiting on somebody", async () => {
    renderWith(<CommandPalette open onOpenChange={() => undefined} onSignOut={() => undefined} />);
    expect(await screen.findByRole("option", { name: /Approvals/ })).toBeInTheDocument();
    expect(await screen.findByRole("option", { name: /graylog/ })).toBeInTheDocument();
    // The change as a sentence, the same words the approvals page uses. The
    // palette is where somebody reaches for a proposal by name, so "device
    // reboot" was the one name they had not been taught.
    expect(await screen.findByRole("option", { name: /Restart the device on cnmaestro/ }))
      .toBeInTheDocument();
    expect(screen.queryByRole("option", { name: /device reboot/ })).not.toBeInTheDocument();
  });

  // The raw action is still searchable, because somebody who knows it should
  // find the change by it; it is just not what they are shown.
  it("still finds a change by the machine's name for it", async () => {
    renderWith(<CommandPalette open onOpenChange={() => undefined} onSignOut={() => undefined} />);
    await screen.findByRole("option", { name: /Restart the device on cnmaestro/ });
    await userEvent.type(screen.getByRole("combobox"), "device.reboot");
    expect(await screen.findByRole("option", { name: /Restart the device on cnmaestro/ }))
      .toBeInTheDocument();
  });

  // A result the account cannot open is worse than no result: it would
  // answer with a refusal.
  it("hides what the account may not open", async () => {
    renderWith(
      <CommandPalette open onOpenChange={() => undefined} onSignOut={() => undefined} />,
      { session: sessionFor("user") },
    );
    await screen.findByRole("option", { name: /Approvals/ });
    // An operator holds history:read, so Logs is now open to it -- Marketplace
    // and API Keys still take plugins:write and access:read, which it does not.
    expect(screen.queryByRole("option", { name: /Marketplace/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("option", { name: /API Keys/ })).not.toBeInTheDocument();
  });

  it("narrows as you type, and goes where Enter points", async () => {
    const onOpenChange = vi.fn();
    renderWith(<CommandPalette open onOpenChange={onOpenChange} onSignOut={() => undefined} />);
    await screen.findByRole("option", { name: /graylog/ });
    await userEvent.type(screen.getByRole("combobox"), "grayl");
    await waitFor(() =>
      expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual(
        expect.arrayContaining([expect.stringMatching(/graylog/)]),
      ));
    expect(screen.queryByRole("option", { name: /Approvals/ })).not.toBeInTheDocument();
    await userEvent.keyboard("{Enter}");
    expect(onOpenChange).toHaveBeenCalledWith(false);
    expect(window.location.pathname).toBe("/plugins/graylog");
  });

  it("signs out from the list, and says so before doing it", async () => {
    const onSignOut = vi.fn();
    renderWith(<CommandPalette open onOpenChange={() => undefined} onSignOut={onSignOut} />);
    await userEvent.click(await screen.findByRole("option", { name: /Sign out/ }));
    expect(onSignOut).toHaveBeenCalled();
  });
});
