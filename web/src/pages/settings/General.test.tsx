import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, type SettingGroup, type SettingsPayload } from "@/lib/api";
import { renderWith, sessionFor } from "@/test/render";
import { General } from "./General";

/** The groups the host's own settings moved into, as the API sends them. */
const groups: SettingGroup[] = [
  {
    name: "server",
    title: "Addresses",
    section: "settings",
    fields: [
      {
        key: "server.public_url", label: "Address assistants use",
        kind: "string", group: "server", apply: "live",
      },
      {
        key: "server.tls_mode", label: "Certificate for the MCP endpoint",
        kind: "enum", group: "server", apply: "restart",
        options: ["off", "self-signed"], default: "off",
      },
      {
        key: "server.frontend_enabled", label: "Serve this dashboard",
        kind: "bool", group: "server", apply: "restart", default: true,
      },
    ],
  },
  {
    name: "timeouts",
    title: "Timeouts",
    section: "settings",
    fields: [
      {
        key: "server.read_timeout_seconds", label: "Wait for the whole request",
        kind: "duration", unit: "seconds", group: "timeouts", apply: "restart",
        default: 60,
      },
    ],
  },
  {
    name: "approval",
    title: "Approvals",
    section: "settings",
    fields: [
      {
        key: "approval.proposal_ttl_minutes", label: "Suggestions expire after",
        kind: "duration", unit: "minutes", group: "approval", apply: "live",
        default: 30,
      },
      {
        key: "approval.inline_max_risk", label: "Approve in the conversation up to",
        kind: "enum", group: "approval", apply: "live", default: "medium",
        options: ["none", "low", "medium", "high", "critical"],
      },
    ],
  },
];

function payload(overrides: Partial<SettingsPayload> = {}): SettingsPayload {
  return {
    groups,
    values: {
      "server.public_url": "https://mcp.example.net",
      "server.tls_mode": "off",
      "server.frontend_enabled": true,
      "server.read_timeout_seconds": 60,
      "approval.proposal_ttl_minutes": 30,
      "approval.inline_max_risk": "medium",
    },
    secrets_set: {},
    encryption_available: true,
    bootstrap: [
      {
        key: "server.listen", label: "Where assistants connect",
        value: "127.0.0.1:9080",
        help: "A bind address stored in the database could lock you out.",
      },
      {
        key: "storage.path", label: "Where everything is stored",
        value: "/var/lib/mcpd/mcpd.db",
      },
    ],
    ...overrides,
  };
}

function stub(overrides: Partial<SettingsPayload> = {}) {
  vi.spyOn(api, "settings").mockResolvedValue(payload(overrides));
}

describe("the settings page, after the config file shrank", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    stub();
  });

  it("shows the host's own settings, which used to be in a file", async () => {
    renderWith(<General />, { session: sessionFor("admin") });

    expect(await screen.findByLabelText(/Address assistants use/)).toHaveValue(
      "https://mcp.example.net",
    );
    expect(screen.getByText("Timeouts")).toBeInTheDocument();
    expect(screen.getByText("Approvals")).toBeInTheDocument();
  });

  // The chip existed and nothing declared ApplyRestart, so it had never
  // rendered. Now several settings do, and it has to be reachable rather than
  // decorative.
  it("says which settings need a restart, including the switches", async () => {
    renderWith(<General />, { session: sessionFor("admin") });
    await screen.findByLabelText(/Address assistants use/);

    const chips = screen.getAllByText("needs a restart");
    expect(chips.length).toBe(3);

    // And a switch is one of them: a restart-only bool used to render without
    // the chip, because the chip was only drawn beside text boxes.
    const dashboard = screen.getByText("Serve this dashboard").closest("label");
    expect(dashboard?.textContent).toContain("needs a restart");

    // A live setting is not labelled as needing one.
    const address = screen.getByText("Address assistants use").closest("label");
    expect(address?.textContent).not.toContain("needs a restart");
  });

  // A duration counted in the wrong unit is a real mistake: 60 read as minutes
  // is an hour-long request timeout somebody set to a minute.
  it("says what unit each duration is counted in", async () => {
    renderWith(<General />, { session: sessionFor("admin") });
    await screen.findByLabelText(/Address assistants use/);

    expect(screen.getByText("In seconds.")).toBeInTheDocument();
    expect(screen.getByText("In minutes.")).toBeInTheDocument();
  });

  // "No inline approval at all" is the strictest setting, and an option
  // reading "none" in a list of risk levels reads as a level instead.
  it("spells the strictest inline ceiling as a word", async () => {
    renderWith(<General />, { session: sessionFor("admin") });
    await screen.findByLabelText(/Address assistants use/);

    const select = screen.getByLabelText(/Approve in the conversation up to/);
    expect(select).toHaveValue("medium");
    expect(
      Array.from(select.querySelectorAll("option")).map((o) => o.textContent),
    ).toEqual(["Nothing", "low", "medium", "high", "critical"]);
  });

  // "Everything is on this page" is only useful to know if the exceptions are
  // named. An operator hunting for the bind address should find out where it
  // lives, not conclude it does not exist.
  it("names the few values that are still in the startup file", async () => {
    renderWith(<General />, { session: sessionFor("admin") });
    await screen.findByLabelText(/Address assistants use/);

    expect(screen.getByText("In the startup file")).toBeInTheDocument();
    expect(screen.getByText("Where assistants connect")).toBeInTheDocument();
    expect(screen.getByText("127.0.0.1:9080")).toBeInTheDocument();
    expect(screen.getByText("/var/lib/mcpd/mcpd.db")).toBeInTheDocument();
  });

  it("saves a changed setting and says when a restart is needed", async () => {
    const save = vi.spyOn(api, "saveSettings").mockResolvedValue({
      applied: ["server.read_timeout_seconds"],
      restart_required: ["server.read_timeout_seconds"],
    });
    renderWith(<General />, { session: sessionFor("admin") });

    const field = await screen.findByLabelText(/Wait for the whole request/);
    await userEvent.clear(field);
    await userEvent.type(field, "90");
    await userEvent.click(screen.getByRole("button", { name: /Save changes/ }));

    await waitFor(() => {
      expect(save).toHaveBeenCalledWith(
        { "server.read_timeout_seconds": "90" }, [],
      );
    });
    expect(
      await screen.findByText(/Saved — some of it needs a restart/),
    ).toBeInTheDocument();
  });

  // Read to see, admin to change. A form somebody fills in and then meets a
  // 403 on is worse than one that says up front it is not theirs.
  it("is read-only for somebody who cannot administer the host", async () => {
    renderWith(<General />, { session: sessionFor("user") });
    await screen.findByLabelText(/Address assistants use/);

    expect(screen.getByLabelText(/Address assistants use/)).toBeDisabled();
    expect(
      screen.queryByRole("button", { name: /Save changes/ }),
    ).not.toBeInTheDocument();
  });
});
