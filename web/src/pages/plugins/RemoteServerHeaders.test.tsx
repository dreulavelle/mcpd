import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, type MCPServer } from "@/lib/api";
import { renderWith, sessionFor } from "@/test/render";
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
  declares_no_credential: true,
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
 * The credentials a document did not declare.
 *
 * Four in five published server.json documents name no header and no variable,
 * and this host could previously only send what one declared -- so a server
 * whose publisher left the credential out could be added and never used. These
 * defend the operator's way in, and the wording around it: a silent document
 * is not evidence that a server is open, and roughly a quarter of them are.
 */
describe("headers an operator adds to a remote server", () => {
  beforeEach(() => stub());

  it("says a silent document is silence, not an open server", async () => {
    renderWith(<PluginDetail name="weather" />);

    expect(
      await screen.findByText(/declares no credential/i),
    ).toBeInTheDocument();
    // The distinction the whole change exists for: offered, never asserted.
    expect(screen.getByText(/some servers need none/i)).toBeInTheDocument();
  });

  it("does not nag about credentials when the document declared some", async () => {
    stub({ declares_no_credential: false });
    renderWith(<PluginDetail name="weather" />);

    expect(await screen.findByRole("button", { name: "Add a header" })).toBeInTheDocument();
    expect(screen.queryByText(/declares no credential/i)).not.toBeInTheDocument();
  });

  it("declares a header and asks for its value elsewhere", async () => {
    const add = vi.spyOn(api, "addMCPServerHeader")
      .mockResolvedValue({ status: "added", note: "Fill the value in." });
    renderWith(<PluginDetail name="weather" />);

    await userEvent.click(await screen.findByRole("button", { name: "Add a header" }));
    await userEvent.type(screen.getByPlaceholderText("X-Api-Key"), "X-Syncro-API-Key");
    await userEvent.click(screen.getByRole("button", { name: "Add header" }));

    await waitFor(() => {
      expect(add).toHaveBeenCalledWith("weather", "X-Syncro-API-Key", "", true);
    });
  });

  it("lists what has been added without ever showing a value", async () => {
    stub({
      extra_headers: [
        { name: "X-Syncro-API-Key", description: "Admin > API Tokens.", secret: true },
      ],
    });
    renderWith(<PluginDetail name="weather" />);

    expect(await screen.findByText("X-Syncro-API-Key")).toBeInTheDocument();
    expect(screen.getByText("Admin > API Tokens.")).toBeInTheDocument();
  });

  it("offers no editing to somebody who cannot administer", async () => {
    stub({
      extra_headers: [{ name: "X-Api-Key", description: "", secret: true }],
    });
    renderWith(<PluginDetail name="weather" />, { session: sessionFor("user") });

    expect(await screen.findByText("X-Api-Key")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add a header" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Remove" })).not.toBeInTheDocument();
  });
});
