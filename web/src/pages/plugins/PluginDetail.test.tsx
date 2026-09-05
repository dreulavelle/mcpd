import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, type MCPServer, type MCPTool, type PluginInstance } from "@/lib/api";
import { renderWith, sessionFor } from "@/test/render";
import { PluginDetail } from "./PluginDetail";

const SERVER: MCPServer = {
  name: "weather",
  title: "Weather",
  description: "Forecasts, from somebody else.",
  version: "1.0.0",
  schema_version: "2025-07-09",
  transport: "streamable-http",
  url: "https://weather.example/mcp",
  enabled: true,
  mounted: true,
  created_at: "2026-08-01T10:00:00Z",
  updated_at: "2026-08-20T10:00:00Z",
  readable: true,
  pending: 1,
  enabled_tools: 1,
  disabled: 0, extra_headers: [], declares_no_credential: false,
    discovery: {},
};

function tool(overrides: Partial<MCPTool> = {}): MCPTool {
  return {
    name: "forecast",
    descriptor: { name: "forecast", description: "Tomorrow's weather." },
    descriptor_hash: "aaaa1111",
    state: "pending",
    first_seen_at: "2026-08-01T10:00:00Z",
    last_seen_at: "2026-08-20T10:00:00Z",
    ...overrides,
  };
}

function stub(tools: MCPTool[] = [tool()]) {
  vi.spyOn(api, "plugins").mockResolvedValue({
    count: 1,
    plugins: [{
      name: "weather", type: "mcp", version: "1.0.0", title: "Weather",
      description: "Forecasts, from somebody else.",
      endpoint: "/mcp/weather", connect_url: "https://host/mcp/weather",
      health: "healthy", tools: [{ name: "weather_forecast", kind: "read" }],
      mutations: [], required: false, settings: [],
    }],
  });
  vi.spyOn(api, "instances").mockResolvedValue({
    count: 1,
    instances: [{
      name: "weather", type: "mcp", runtime: "mcp",
      from_file: false, enabled: true, mounted: true,
    }],
  });
  vi.spyOn(api, "settings").mockResolvedValue({
    groups: [], values: {}, secrets_set: {}, encryption_available: true, bootstrap: [],
  });
  vi.spyOn(api, "tunnel").mockResolvedValue({
    tunnels: [], can_manage: false, plugins: ["weather"], workspaces: [], assignments: {}, accounts: [],
  });
  vi.spyOn(api, "mcpServers").mockResolvedValue({ servers: [SERVER] });
  vi.spyOn(api, "mcpServerTools").mockResolvedValue({ tools, count: tools.length });
}

/**
 * A remote MCP server is managed where every other plugin is.
 *
 * It used to be half here and half on a Marketplace page: credentials on this
 * page, tools on that one, and a link to bounce between them. One installed
 * thing appeared in two sections of the console, and looking after it meant
 * knowing which half lived where.
 */
describe("a remote MCP server's plugin page", () => {
  beforeEach(() => stub());

  it("lists the tools here rather than sending the operator elsewhere", async () => {
    renderWith(<PluginDetail name="weather" />);

    expect(await screen.findByText("forecast")).toBeInTheDocument();
    expect(
      screen.queryByRole("link", { name: /Marketplace/ }),
    ).not.toBeInTheDocument();
  });

  // A pending tool is not served, and that is the fact an operator most often
  // has the wrong idea about. It is said in words, not left to be inferred
  // from a chip.
  it("says plainly that a pending tool is not being served", async () => {
    renderWith(<PluginDetail name="weather" />);

    expect(await screen.findByText(/waiting for you to\s+allow/)).toBeInTheDocument();
    // Twice: the filter offers the state, and the row is in it.
    expect(screen.getAllByText("Waiting on you")).toHaveLength(2);
  });

  it("opens the classify dialog for the descriptor that was on screen", async () => {
    renderWith(<PluginDetail name="weather" />);

    await userEvent.click(await screen.findByRole("button", { name: "Decide" }));

    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    expect(screen.getByText("What the server says about it")).toBeInTheDocument();
    expect(screen.getByText(/aaaa1111/)).toBeInTheDocument();
  });

  /**
   * Removal goes to the endpoint that owns the document.
   *
   * `DELETE /api/instances/{name}` refuses a remote server outright -- there
   * is no instances. key to delete -- so a Remove button wired to it would be
   * an error message with a delay in front of it.
   */
  it("removes through the server endpoint, not the instance one", async () => {
    const removeServer = vi.spyOn(api, "removeMCPServer")
      .mockResolvedValue({ status: "removed" });
    const removeInstance = vi.spyOn(api, "removeInstance");

    renderWith(<PluginDetail name="weather" />);

    await userEvent.click(await screen.findByRole("button", { name: "Remove" }));
    await userEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Remove" }));

    await waitFor(() => expect(removeServer).toHaveBeenCalledWith("weather"));
    expect(removeInstance).not.toHaveBeenCalled();
  });

  it("offers switching the server off from the same page", async () => {
    const setEnabled = vi.spyOn(api, "setMCPServerEnabled")
      .mockResolvedValue({ status: "saved" });

    renderWith(<PluginDetail name="weather" />);

    await userEvent.click(await screen.findByRole("button", { name: "Switch off" }));
    await waitFor(() => expect(setEnabled).toHaveBeenCalledWith("weather", false));
  });

  /**
   * Reading what a server offers takes read; deciding what is served takes
   * admin, and that is how the endpoints are gated. Offering the buttons to
   * somebody the API will refuse is a worse way of saying the same thing.
   */
  it("shows a reader the tools and none of the decisions", async () => {
    renderWith(<PluginDetail name="weather" />, { session: sessionFor("user") });

    expect(await screen.findByText("forecast")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Decide" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Discover" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Remove" })).not.toBeInTheDocument();
  });
});

describe("a builtin plugin's page", () => {
  beforeEach(() => {
    stub();
    vi.spyOn(api, "instances").mockResolvedValue({
      count: 1,
      instances: [{
        name: "weather", type: "weather", runtime: "builtin",
        from_file: false, enabled: true, mounted: true,
      }],
    });
  });

  // Nothing about a remote server belongs on the page of something this build
  // compiled in, and asking for a tool snapshot it has none of is a request
  // with no answer.
  it("carries none of the remote-server furniture", async () => {
    renderWith(<PluginDetail name="weather" />);

    expect(await screen.findByText("Read")).toBeInTheDocument();
    expect(screen.queryByText("The document")).not.toBeInTheDocument();
    expect(api.mcpServerTools).not.toHaveBeenCalled();
  });
});

/**
 * A plugin the configuration file declares can be removed here, and the page
 * must not let anybody believe their file changed. mcpd cannot write it: it is
 * mounted read-only in the container image, on a read-only root filesystem,
 * under a systemd unit with ProtectSystem=strict.
 */
describe("a plugin the configuration file declares", () => {
  function declared(overrides: Partial<PluginInstance> = {}) {
    stub();
    vi.spyOn(api, "instances").mockResolvedValue({
      count: 1,
      instances: [{
        name: "weather", type: "weather", runtime: "builtin",
        from_file: true, enabled: true, mounted: true,
        declaration: {
          type: "weather", enabled: true, required: false,
          settings_keys: ["api_token", "base_url"],
        },
        ...overrides,
      }],
    });
  }

  it("says the file is unchanged rather than implying it was edited", async () => {
    declared();
    renderWith(<PluginDetail name="weather" />);

    expect(await screen.findByText(/The file is unchanged/)).toBeInTheDocument();
    expect(screen.getByText(/after every restart/)).toBeInTheDocument();
    // The old dead end: "remove it there rather than here".
    expect(screen.queryByText(/Remove it there/)).not.toBeInTheDocument();
    expect(await screen.findByRole("button", { name: "Remove" })).toBeInTheDocument();
  });

  it("shows what the file declares, keys without values", async () => {
    declared();
    renderWith(<PluginDetail name="weather" />);

    expect(await screen.findByText("In the configuration file")).toBeInTheDocument();
    expect(screen.getByText("api_token")).toBeInTheDocument();
    expect(screen.getByText("base_url")).toBeInTheDocument();
  });

  /**
   * `required: true` is the deployment saying the host is meant not to run
   * without the integration. Overriding it is allowed and is not a
   * click-through: the API refuses without the acknowledgement.
   */
  it("acknowledges required before removing one", async () => {
    declared({ required: true, declaration: {
      type: "weather", enabled: true, required: true,
    } });
    const remove = vi.spyOn(api, "removeInstance").mockResolvedValue({ status: "removed" });

    renderWith(<PluginDetail name="weather" />);
    await userEvent.click(await screen.findByRole("button", { name: "Remove" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog.textContent).toMatch(/required: true/);
    expect(dialog.textContent).toMatch(/configuration file does not change/);
    await userEvent.click(within(dialog).getByRole("button", { name: "Remove" }));

    await waitFor(() => expect(remove).toHaveBeenCalledWith("weather", true));
  });

  /**
   * The settings used to be kept for a file-declared removal so that a restore
   * came back "as it was", and the confirmation said so. What the dashboard
   * holds goes now -- but a declaration may carry its own `settings:` block,
   * which this host does not write and cannot forget, so the sentence has to
   * carry both halves or it is wrong in one direction or the other.
   */
  it("says which settings go with a file-declared removal", async () => {
    declared();
    const remove = vi.spyOn(api, "removeInstance").mockResolvedValue({ status: "removed" });

    renderWith(<PluginDetail name="weather" />);
    expect(await screen.findByText(/Settings entered here, including credentials, are forgotten/))
      .toBeInTheDocument();
    expect(screen.getByText(/the configuration file itself provides stays/))
      .toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Remove" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog.textContent).toMatch(/Settings entered here, including credentials, are forgotten/);
    expect(dialog.textContent).toMatch(/configuration file itself provides stays/);
    expect(dialog.textContent).toMatch(/added again later/);
    await userEvent.click(within(dialog).getByRole("button", { name: "Remove" }));

    await waitFor(() => expect(remove).toHaveBeenCalledWith("weather", false));
  });

  /**
   * `enabled: false` in a file nobody on this host can edit was the same dead
   * end one step smaller, and it was refused with "change `enabled` there".
   */
  it("switches one off without touching the file", async () => {
    declared();
    const setEnabled = vi.spyOn(api, "setInstanceEnabled")
      .mockResolvedValue({ status: "saved" });

    renderWith(<PluginDetail name="weather" />);
    await userEvent.click(await screen.findByRole("button", { name: "Switch off" }));

    await waitFor(() => expect(setEnabled).toHaveBeenCalledWith("weather", false));
  });

  /**
   * The words are add and remove. What the page offers is not the plugin as it
   * was -- the removal took what the dashboard held -- so it says what comes
   * back: the file's listing, and only what the file provides. Who removed it
   * reads as a name, not as the `user:` id the API sends.
   */
  it("offers to add it again, and says who took it away", async () => {
    declared({
      enabled: false, mounted: false, removed: true,
      removed_by: "user:alice", removed_at: "2026-08-20T10:00:00Z",
    });
    const add = vi.spyOn(api, "restoreInstance").mockResolvedValue({ status: "restored" });

    renderWith(<PluginDetail name="weather" />);

    expect(await screen.findByText(/the settings entered here are gone/)).toBeInTheDocument();
    expect(screen.getByText(/Removed by alice on/)).toBeInTheDocument();
    expect(screen.queryByText(/user:alice/)).not.toBeInTheDocument();
    expect(screen.getByText(/so it can be added again/)).toBeInTheDocument();
    // Nothing is left to take, so nothing offers to take it.
    expect(screen.queryByRole("button", { name: "Forget its settings" }))
      .not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Restore" })).not.toBeInTheDocument();
    // One control, not two: removing something already removed is a button
    // with nothing to do.
    expect(screen.queryByRole("button", { name: "Remove" })).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Add" }));
    await waitFor(() => expect(add).toHaveBeenCalledWith("weather"));
  });

  /**
   * A plugin removed by a build that kept its settings deliberately arrives
   * here still holding credentials, and the Remove card is not drawn for
   * something already removed -- so without this there is nowhere to take
   * them. The removal is idempotent, so finishing it is the same call again.
   */
  it("offers to take the settings a removal left behind", async () => {
    declared({
      enabled: false, mounted: false, removed: true, stored_settings: true,
      removed_by: "user:alice", removed_at: "2026-08-20T10:00:00Z",
    });
    const remove = vi.spyOn(api, "removeInstance").mockResolvedValue({ status: "removed" });

    renderWith(<PluginDetail name="weather" />);

    expect(await screen.findByText(/settings entered here are still stored/))
      .toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Forget its settings" }));

    await waitFor(() => expect(remove).toHaveBeenCalledWith("weather", true));
  });
});

/**
 * The "How to use" section is the plugin type's own guide, shown under the
 * address for the person about to ask their first question. It appears only
 * when the type declares one.
 */
describe("a plugin's how-to-use guide", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    vi.spyOn(api, "plugins").mockResolvedValue({
      count: 1,
      plugins: [{
        name: "pbx", type: "threecx", version: "0.2.0", title: "3CX", description: "",
        endpoint: "/mcp/pbx", connect_url: "https://host/mcp/pbx",
        health: "healthy", tools: [{ name: "pbx_list_customers", kind: "read" }],
        mutations: [], required: false, settings: [],
      }],
    });
    vi.spyOn(api, "instances").mockResolvedValue({
      count: 1,
      instances: [{ name: "pbx", type: "threecx", runtime: "builtin", from_file: false, enabled: true, mounted: true }],
    });
    vi.spyOn(api, "settings").mockResolvedValue({
      groups: [], values: {}, secrets_set: {}, encryption_available: true, bootstrap: [],
    });
    vi.spyOn(api, "tunnel").mockResolvedValue({
      tunnels: [], can_manage: false, plugins: ["pbx"], workspaces: [], assignments: {}, accounts: [],
    });
  });

  it("shows the type's questions and notes under the address", async () => {
    vi.spyOn(api, "pluginTypes").mockResolvedValue({
      count: 1,
      types: [{
        name: "threecx", title: "3CX", description: "", configurable: true,
        guide: { questions: ["Are the phones at Acme working?"], notes: ["Read-only."] },
      }],
    });
    renderWith(<PluginDetail name="pbx" />);
    expect(await screen.findByText("How to use")).toBeInTheDocument();
    expect(screen.getByText(/Are the phones at Acme working\?/)).toBeInTheDocument();
    expect(screen.getByText("Read-only.")).toBeInTheDocument();
  });

  it("shows nothing when the type declares no guide", async () => {
    vi.spyOn(api, "pluginTypes").mockResolvedValue({
      count: 1,
      types: [{ name: "threecx", title: "3CX", description: "", configurable: true }],
    });
    renderWith(<PluginDetail name="pbx" />);
    await screen.findByText("Address");
    expect(screen.queryByText("How to use")).not.toBeInTheDocument();
  });
});
