import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, type MCPServer, type MCPTool } from "@/lib/api";
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
  disabled: 0,
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
    tunnels: [], can_manage: false, plugins: ["weather"], workspaces: [], assignments: {},
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

    expect(await screen.findByText(/waiting to be\s+classified/)).toBeInTheDocument();
    // Twice: the filter offers the state, and the row is in it.
    expect(screen.getAllByText("Waiting on you")).toHaveLength(2);
  });

  it("opens the classify dialog for the descriptor that was on screen", async () => {
    renderWith(<PluginDetail name="weather" />);

    await userEvent.click(await screen.findByRole("button", { name: "Review" }));

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
    vi.spyOn(window, "confirm").mockReturnValue(true);

    renderWith(<PluginDetail name="weather" />);

    await userEvent.click(await screen.findByRole("button", { name: "Remove" }));

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
    expect(screen.queryByRole("button", { name: "Review" })).not.toBeInTheDocument();
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
