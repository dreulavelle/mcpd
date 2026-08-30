import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import { api, type MCPServer } from "@/lib/api";
import { renderWith } from "@/test/render";
import { PluginDetail } from "./PluginDetail";

const SERVER: MCPServer = {
  name: "weather",
  title: "Weather",
  description: "Forecasts, from somebody else.",
  version: "1.0.0",
  schema_version: "2025-12-11",
  transport: "streamable-http",
  url: "https://weather.example/mcp",
  enabled: true,
  mounted: true,
  created_at: "2026-08-01T10:00:00Z",
  updated_at: "2026-08-20T10:00:00Z",
  readable: true,
  pending: 0,
  enabled_tools: 1,
  disabled: 0,
  extra_headers: [],
  declares_no_credential: false,
  discovery: {},
};

function stub(server: Partial<MCPServer> = {}) {
  const s = { ...SERVER, ...server };
  vi.spyOn(api, "plugins").mockResolvedValue({
    count: 1,
    plugins: [{
      name: "weather", type: "mcp", version: "1.0.0", title: "Weather",
      description: "Forecasts, from somebody else.",
      endpoint: "/mcp/weather", connect_url: "https://host/mcp/weather",
      health: "healthy", tools: [], mutations: [], required: false, settings: [],
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
  vi.spyOn(api, "mcpServers").mockResolvedValue({ servers: [s] });
  vi.spyOn(api, "mcpServerTools").mockResolvedValue({ tools: [], count: 0 });
}

/**
 * How old the tool list is.
 *
 * The page shows a stored snapshot rather than dialling the server when a tab
 * is opened, which is the right trade — but it means the list can be old
 * without looking old. Discovery now runs on a schedule, so the age is a fact
 * the page has and must state.
 */
describe("the age of a remote server's tool list", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    window.localStorage.clear();
  });

  it("says when the list was last confirmed", async () => {
    stub({ discovery: { last_attempted: "2026-08-29T09:00:00Z", last_succeeded: "2026-08-29T09:00:00Z" } });
    renderWith(<PluginDetail name="weather" />);

    expect(await screen.findByText(/This list was confirmed/)).toBeInTheDocument();
  });

  // A server nothing has asked yet must not read as freshly confirmed. This is
  // the case the wire format had to be fixed for: a zero timestamp used to
  // serialise as the year 1 and rendered as a real date.
  it("says so when nothing has checked yet", async () => {
    stub({ discovery: {} });
    renderWith(<PluginDetail name="weather" />);

    expect(await screen.findByText(/Not checked yet/)).toBeInTheDocument();
  });

  // The two facts are separate on purpose. A failing check does not make the
  // tools on screen any newer, so the confirmed-at date stays put and the
  // failure is said as well, not instead.
  it("reports a failing check without ageing the data", async () => {
    stub({
      discovery: {
        last_succeeded: "2026-08-25T09:00:00Z",
        last_attempted: "2026-08-29T09:00:00Z",
        error: "connection refused",
      },
    });
    renderWith(<PluginDetail name="weather" />);

    expect(await screen.findByText(/This list was confirmed/)).toBeInTheDocument();
    expect(screen.getByText(/connection refused/)).toBeInTheDocument();
  });
});
