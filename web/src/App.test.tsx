import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import { render } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api } from "@/lib/api";
import App from "@/App";

/**
 * Payloads copied from a running mcpd, not invented.
 *
 * The shapes that matter are the ones the server actually sends, and two of
 * them are only obvious from a live instance: `operations` comes back as null
 * rather than an empty array, and `instances` is where `runtime` lives. A
 * fixture written from the Go struct would have missed both.
 */
const SESSION = {
  email: "smoke@example.com",
  display_name: "Smoke Test",
  role: "admin" as const,
  plugins: ["*"],
  csrf_token: "test-csrf",
  expires_at: "2026-08-23T13:51:51Z",
};

function stubApi(role: "user" | "admin" = "admin") {
  vi.spyOn(api, "meta").mockResolvedValue({
    version: "dev", auth_mode: "static", needs_setup: false,
  });
  vi.spyOn(api, "session").mockResolvedValue({ ...SESSION, role });
  vi.spyOn(api, "health").mockResolvedValue({
    status: "up",
    checks: [{ name: "database", status: "up", critical: true }],
  });
  // Null rather than [], which is what the endpoint really returns when
  // nothing matches. Mapping over it is the bug this guards.
  vi.spyOn(api, "operations").mockResolvedValue(
    { operations: null as never, count: 0 },
  );
  vi.spyOn(api, "plugins").mockResolvedValue({
    count: 1,
    plugins: [{
      name: "echo", type: "echo", version: "1.0.0", title: "Echo",
      description: "A test integration.",
      endpoint: "/mcp/echo", connect_url: "http://127.0.0.1:18080/mcp/echo",
      health: "healthy",
      tools: [
        { name: "echo_echo", kind: "read" },
        { name: "echo_label_set", kind: "propose" },
      ],
      mutations: ["label.set"], required: false, settings: [],
    }],
  });
  vi.spyOn(api, "instances").mockResolvedValue({
    count: 1,
    instances: [{
      name: "echo", type: "echo", runtime: "builtin",
      from_file: true, enabled: true, mounted: true,
    }],
  });
  vi.spyOn(api, "pluginTypes").mockResolvedValue({ types: [], count: 0 });
  vi.spyOn(api, "audit").mockResolvedValue({ records: [], count: 0 });
  vi.spyOn(api, "mcpServers").mockResolvedValue({ servers: [] });
  vi.spyOn(api, "tunnel").mockResolvedValue({
    tunnels: [], can_manage: false, plugins: ["echo"], workspaces: [],
    assignments: {}, missing: "an OpenAI admin key and organization ID",
  });
  vi.spyOn(api, "endpoints").mockResolvedValue({
    aggregate: "http://127.0.0.1:18080/mcp", per_plugin_example: "/mcp/{plugin}",
  });
}

describe("the console", () => {
  beforeEach(() => {
    // The router reads window.location, which survives a test. Without this
    // reset, one test's navigation decides where the next one starts.
    window.history.replaceState(null, "", "/");
    stubApi();
  });

  it("signs a returning session straight in, without flashing the form", async () => {
    render(<App />);
    expect(await screen.findByRole("link", { name: "Overview" })).toBeInTheDocument();
    expect(screen.queryByLabelText("Password")).not.toBeInTheDocument();
  });

  it("lands on the overview and survives an empty deployment", async () => {
    render(<App />);
    expect(await screen.findByText("Hello, Smoke")).toBeInTheDocument();
    expect(await screen.findByText("Nothing waiting")).toBeInTheDocument();
  });

  it("navigates to a section and back without a reload", async () => {
    render(<App />);
    await screen.findByRole("link", { name: "Plugins" });

    await userEvent.click(screen.getByRole("link", { name: "Plugins" }));
    expect(await screen.findByText("Built in")).toBeInTheDocument();
    expect(window.location.pathname).toBe("/plugins");

    await userEvent.click(screen.getByRole("link", { name: "Audit" }));
    expect(await screen.findByText("Nothing recorded yet")).toBeInTheDocument();
    expect(window.location.pathname).toBe("/audit");
  });

  it("splits builtin plugins from remote MCP servers", async () => {
    render(<App />);
    await screen.findByRole("link", { name: "Plugins" });
    await userEvent.click(screen.getByRole("link", { name: "Plugins" }));

    expect(await screen.findByText("Built in")).toBeInTheDocument();
    // Nothing remote is configured, so that heading must not appear at all.
    expect(screen.queryByText("Remote MCP servers")).not.toBeInTheDocument();
  });
});

describe("a signed-out visitor", () => {
  it("is offered a sign-in form", async () => {
    vi.spyOn(api, "meta").mockResolvedValue({
      version: "dev", auth_mode: "static", needs_setup: false,
    });
    vi.spyOn(api, "session").mockRejectedValue(new Error("no session"));

    render(<App />);
    expect(await screen.findByLabelText("Password")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Sign in" })).toBeInTheDocument();
  });

  // An unclaimed instance has no credentials to ask for yet, so asking for
  // them would be asking for something that cannot exist.
  it("is offered a way to claim an unclaimed instance instead", async () => {
    vi.spyOn(api, "meta").mockResolvedValue({
      version: "dev", auth_mode: "static", needs_setup: true,
    });
    vi.spyOn(api, "session").mockRejectedValue(new Error("no session"));

    render(<App />);
    expect(await screen.findByText("Create the first account")).toBeInTheDocument();
  });
});

/**
 * Typing a URL is not a way around the sidebar.
 *
 * Hiding a link is not access control -- the server refuses every call again
 * -- but a page that renders its chrome and then fails every fetch is a worse
 * answer than a sentence saying why.
 */
describe("reaching an admin-only section by URL", () => {
  it("refuses a user, naming the capability they lack", async () => {
    stubApi("user");
    window.history.replaceState(null, "", "/marketplace");

    render(<App />);
    expect(await screen.findByText("Not for this account")).toBeInTheDocument();
    expect(screen.getByText("admin")).toBeInTheDocument();
    await waitFor(() => expect(api.mcpServers).not.toHaveBeenCalled());
  });

  it("lets an administrator through", async () => {
    stubApi("admin");
    window.history.replaceState(null, "", "/marketplace");

    render(<App />);
    expect(await screen.findByText("No remote servers")).toBeInTheDocument();
  });
});
