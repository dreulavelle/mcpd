import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, type Plugin } from "@/lib/api";
import { renderWith, sessionFor } from "@/test/render";
import { Clients } from "./Clients";

function plugin(name: string): Plugin {
  return {
    name, type: name, version: "1", title: name, description: "",
    endpoint: `/mcp/${name}`, connect_url: `https://mcp.example.net/mcp/${name}`,
    health: "healthy", tools: [], mutations: [], required: false, settings: [],
  };
}

function stub(aggregate = "https://mcp.example.net/mcp") {
  vi.spyOn(api, "endpoints").mockResolvedValue({
    aggregate, per_plugin_example: `${aggregate}/{plugin}`,
    advertised: aggregate.startsWith("http"), port: "8080",
  });
  vi.spyOn(api, "plugins").mockResolvedValue({
    plugins: [plugin("graylog"), plugin("echo")], count: 2,
  });
}

describe("the clients page", () => {
  /**
   * The point of the page over the client's own documentation: the address
   * is this host's, not a placeholder, and choosing a plugin swaps in that
   * plugin's own address everywhere it appears.
   */
  it("fills this host's address into the snippet, and a plugin's when one is chosen", async () => {
    stub();
    renderWith(<Clients />, { path: "/clients" });

    expect(await screen.findByText(/claude mcp add --transport http mcpd https:\/\/mcp\.example\.net\/mcp/)).toBeInTheDocument();

    await userEvent.selectOptions(screen.getByLabelText("Reach"), "graylog");
    expect(await screen.findByText(/mcpd-graylog https:\/\/mcp\.example\.net\/mcp\/graylog/)).toBeInTheDocument();
  });

  /**
   * A key pasted into a snippet is a key pasted into a file somebody commits.
   * Every snippet reads the key from the environment or a prompt instead, so
   * the page must never render a real-looking key.
   */
  it("never writes a key into a snippet", async () => {
    stub();
    renderWith(<Clients />, { path: "/clients?client=cursor" });
    expect(await screen.findByText(/\$\{env:MCPD_KEY\}/)).toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(/mcpd_[A-Za-z0-9_-]{8,}/);
  });

  /**
   * With no advertised address the server returns the route bare. Rendering
   * "/mcp" into a client's config would produce something that cannot
   * connect and a support call about the client, so the page names the
   * setting instead.
   */
  it("says when the address is only a path, and names the setting", async () => {
    stub("/mcp");
    renderWith(<Clients />, { path: "/clients" });
    expect(await screen.findByText(/A guess from this page's host/)).toBeInTheDocument();
    expect(screen.getAllByText(/localhost:8080\/mcp/).length).toBeGreaterThan(0);
    expect(screen.getAllByRole("link", { name: "Settings › General" }).length).toBeGreaterThan(0);
  });

  /**
   * Issuing a key is admin; using one is not. A user setting up their own
   * client is told who to ask rather than shown a link that would refuse them.
   */
  it("offers the keys page to an administrator and names the administrator to anyone else", async () => {
    stub();
    const { unmount } = renderWith(<Clients />, { path: "/clients" });
    expect(await screen.findByRole("link", { name: /Issue a key/ })).toBeInTheDocument();
    unmount();

    renderWith(<Clients />, { path: "/clients", session: sessionFor("user") });
    expect(await screen.findByText(/An administrator issues keys/)).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /Issue a key/ })).not.toBeInTheDocument();
  });
});
