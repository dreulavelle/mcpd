import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, type SettingGroup, type SettingsPayload } from "@/lib/api";
import { renderWith, sessionFor } from "@/test/render";
import { Advanced } from "./Advanced";
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
    // On Advanced, not here. A group declares its own section in Go and the
    // dashboard renders where it is told, so a fixture that put it here would
    // be testing an arrangement this host does not have.
    section: "advanced",
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
    // With the rules it times, on Policies under Govern.
    section: "approvals",
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


/**
 * Whether a group's card is on the page.
 *
 * A name can also appear on the tab strip above, which is a link rather than a
 * card, so only an occurrence outside a link answers the question.
 */
function sectionShown(title: string): boolean {
  return screen.queryAllByText(title).some((el) => el.closest("a") === null);
}

describe("the settings page, after the config file shrank", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    stub();
  });

  /**
   * General is what an operator sets when the host goes up and rarely again.
   *
   * It used to be every setting this host has -- thirty-one in one column,
   * behind a row of filter chips that sat under a row of tabs. Timeouts are on
   * Advanced now and the approval timings are on Policies, beside the rules
   * they time.
   */
  it("shows the settings a host is set up with, and not the rest", async () => {
    renderWith(<General />, { session: sessionFor("admin") });

    expect(await screen.findByLabelText(/Address assistants use/)).toHaveValue(
      "https://mcp.example.net",
    );
    expect(sectionShown("Timeouts")).toBe(false);
    expect(sectionShown("Approvals")).toBe(false);
  });

  /**
   * Search reaches every tab, not this one.
   *
   * Somebody who cannot find a setting does not know which tab it is on --
   * that is what not finding it means -- so a search scoped to the tab in
   * front of them would confirm their belief that it does not exist. "Wait for
   * the whole request" lives on Advanced, and is found from here.
   */
  it("finds a setting that lives on another tab", async () => {
    const user = userEvent.setup();
    renderWith(<General />, { session: sessionFor("admin") });
    await screen.findByLabelText(/Address assistants use/);

    await user.type(screen.getByLabelText("Search every setting"), "timeout");

    expect(screen.getByLabelText(/Wait for the whole request/)).toBeInTheDocument();
    expect(screen.queryByLabelText(/Address assistants use/)).not.toBeInTheDocument();
  });

  // And says where it lives, or a match found from another section looks like
  // it was here all along and the reader learns nothing for next time.
  it("says which tab a match came from", async () => {
    const user = userEvent.setup();
    renderWith(<General />, { session: sessionFor("admin") });
    await screen.findByLabelText(/Address assistants use/);

    await user.type(screen.getByLabelText("Search every setting"), "timeout");
    expect(screen.getByText("On the Advanced tab")).toBeInTheDocument();
  });

  /**
   * Clearing the search puts the tab back.
   *
   * This used to click a row of filter chips that sat under the tabs. Two
   * navigations stacked is one too many, and these were worse than that: the
   * chips only meant anything once the tabs had been used, and anybody
   * describing the page out loud called both of them tabs.
   */
  it("puts the tab back when the search is cleared", async () => {
    const user = userEvent.setup();
    renderWith(<General />, { session: sessionFor("admin") });
    await screen.findByLabelText(/Address assistants use/);

    const box = screen.getByLabelText("Search every setting");
    await user.type(box, "timeout");
    expect(screen.queryByLabelText(/Address assistants use/)).not.toBeInTheDocument();

    await user.clear(box);
    expect(await screen.findByLabelText(/Address assistants use/)).toBeInTheDocument();
  });

  // The chip existed and nothing declared ApplyRestart, so it had never
  // rendered. Now several settings do, and it has to be reachable rather than
  // decorative.
  it("says which settings need a restart, including the switches", async () => {
    renderWith(<General />, { session: sessionFor("admin") });
    await screen.findByLabelText(/Address assistants use/);

    // Two on this tab. The third restart-only field in the fixture is a
    // timeout, which lives on Advanced now.
    const chips = screen.getAllByText("needs a restart");
    expect(chips.length).toBe(2);

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
  //
  // Asserted through the search, which is the one view that holds every
  // section at once -- the two durations in the fixture are now on two
  // different tabs.
  it("says what unit each duration is counted in", async () => {
    const user = userEvent.setup();
    renderWith(<General />, { session: sessionFor("admin") });
    await screen.findByLabelText(/Address assistants use/);

    await user.type(screen.getByLabelText("Search every setting"), "after");
    expect(screen.getByText("In minutes.")).toBeInTheDocument();

    await user.clear(screen.getByLabelText("Search every setting"));
    await user.type(screen.getByLabelText("Search every setting"), "request");
    expect(screen.getByText("In seconds.")).toBeInTheDocument();
  });

  // "No inline approval at all" is the strictest setting, and an option
  // reading "none" in a list of risk levels reads as a level instead.
  it("spells the strictest inline ceiling as a word", async () => {
    const user = userEvent.setup();
    renderWith(<General />, { session: sessionFor("admin") });
    await screen.findByLabelText(/Address assistants use/);

    // It lives with the approval rules now, and is reachable from here by
    // searching -- which is the behaviour that makes moving it safe.
    await user.type(screen.getByLabelText("Search every setting"), "conversation");
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
    // The field is on Advanced, which renders the same form component from the
    // same payload -- so this is still one test of saving rather than one per
    // tab.
    renderWith(<Advanced />, { session: sessionFor("admin") });

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
