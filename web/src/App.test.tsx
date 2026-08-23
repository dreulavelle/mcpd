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
  vi.spyOn(api, "settings").mockResolvedValue({
    groups: [], values: {}, secrets_set: {}, encryption_available: true,
    bootstrap: [],
  });
  vi.spyOn(api, "users").mockResolvedValue({ users: [], count: 0 });
  vi.spyOn(api, "mcpServerTools").mockResolvedValue({ tools: [], count: 0 });
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
    // Discovery, not an inventory: the catalog is what the page is for, and
    // the catalog API is not merged yet.
    expect(await screen.findByText("No catalog yet")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add Custom MCP" })).toBeInTheDocument();
  });
});

/**
 * The marketplace stopped being where an installed server is managed.
 *
 * It is a plugin, and it is managed with the plugins. Somebody has the old
 * address bookmarked, so it lands on the page the thing moved to rather than
 * on "nothing here".
 */
describe("an old marketplace deep link", () => {
  beforeEach(() => {
    window.history.replaceState(null, "", "/");
    stubApi("admin");
  });

  it("lands on the server's plugin page", async () => {
    vi.spyOn(api, "instances").mockResolvedValue({
      count: 1,
      instances: [{
        name: "weather", type: "mcp", runtime: "mcp",
        from_file: false, enabled: true, mounted: false,
      }],
    });
    vi.spyOn(api, "mcpServers").mockResolvedValue({
      servers: [{
        name: "weather", title: "Weather", description: "Forecasts.",
        version: "1.0.0", schema_version: "2025-07-09",
        transport: "streamable-http", url: "https://weather.example/mcp",
        enabled: true, mounted: false, readable: true,
        created_at: "2026-08-01T10:00:00Z", updated_at: "2026-08-20T10:00:00Z",
        pending: 0, enabled_tools: 0, disabled: 0,
      }],
    });
    window.history.replaceState(null, "", "/marketplace/weather");

    render(<App />);

    expect(await screen.findByText("Remote MCP server")).toBeInTheDocument();
    expect(window.location.pathname).toBe("/plugins/weather");
  });

  // Two segments were a server. Three never named anything, and the catalog
  // is not a sensible answer to an address that meant nothing.
  it("still says nothing here for an address that never existed", async () => {
    window.history.replaceState(null, "", "/marketplace/weather/tools");

    render(<App />);
    expect(await screen.findByText("Nothing here")).toBeInTheDocument();
  });

  // Replaced rather than pushed: back should go where the operator came from,
  // not to an address that immediately forwards again.
  it("does not leave the old address in the history", async () => {
    const before = window.history.length;
    window.history.replaceState(null, "", "/marketplace/weather");

    render(<App />);
    await screen.findByText("weather", { selector: "h1" });
    expect(window.history.length).toBe(before);
  });
});

/**
 * Your own profile is not an administrative surface.
 *
 * It is the one destination the map marks "signed-in" rather than gating on a
 * capability, and a route with no capability has to be representable -- an
 * unknown path renders "nothing here", and this must not fall into that hole.
 */
describe("the profile", () => {
  beforeEach(() => {
    window.history.replaceState(null, "", "/profile");
  });

  it("opens for a user who holds nothing but read", async () => {
    stubApi("user");
    render(<App />);

    expect(await screen.findByText("What you may do")).toBeInTheDocument();
    expect(screen.queryByText("Not for this account")).not.toBeInTheDocument();
    expect(screen.queryByText("Nothing here")).not.toBeInTheDocument();
  });

  // The endpoint that changes an account takes admin, so a user is told to ask
  // rather than shown a form that answers 403.
  it("does not offer a user a password form it cannot submit", async () => {
    stubApi("user");
    render(<App />);

    await screen.findByText("What you may do");
    expect(screen.getByText(/no self-service password endpoint/i)).toBeInTheDocument();
    expect(screen.queryByLabelText("New password")).not.toBeInTheDocument();
    expect(api.users).not.toHaveBeenCalled();
  });

  it("offers an administrator the fields the API will accept", async () => {
    stubApi("admin");
    render(<App />);

    expect(await screen.findByLabelText("Display name")).toBeInTheDocument();
    expect(screen.getByLabelText("New password")).toBeInTheDocument();
  });
});
