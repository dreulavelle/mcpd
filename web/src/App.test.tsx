import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, screen, waitFor } from "@testing-library/react";
import { render } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api } from "@/lib/api";
import { BUILTIN_ROLES, builtinPermissions } from "@/lib/permissions";
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
  // Both, as the endpoint sends both: `name` is resolved and never empty,
  // `display_name` is the stored value an edit field round-trips.
  name: "Smoke Test",
  display_name: "Smoke Test",
  csrf_token: "test-csrf",
  expires_at: "2026-08-23T13:51:51Z",
  // An ordinary account: somebody decided about it, and it has a password.
  // A pending one is the interesting case and gets its own test.
  status: "active" as const,
  has_password: true,
};

function stubApi(role: "user" | "admin" = "admin") {
  vi.spyOn(api, "meta").mockResolvedValue({
    version: "dev", auth_mode: "static", needs_setup: false,
  });
  const roleId = role === "admin" ? "role_administrator" : "role_operator";
  vi.spyOn(api, "session").mockResolvedValue({
    ...SESSION,
    role: roleId,
    role_name: BUILTIN_ROLES[roleId]?.name ?? roleId,
    plugins: ["*"],
    grants: [{ plugin: "*", level: "write" }],
    permissions: builtinPermissions(roleId),
  });
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
        { name: "echo_get_echo", kind: "read" },
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
  vi.spyOn(api, "catalog").mockResolvedValue({
    source: "registry.modelcontextprotocol.io",
    entries: [{
      name: "com.example/weather",
      suggested_name: "weather",
      title: "Weather",
      description: "Forecasts and observations.",
      version: "1.0.0",
      transport: "streamable-http",
      url: "https://weather.example/mcp",
      updated_at: "2026-08-01T10:00:00Z",
      addable: true,
      source: "registry.modelcontextprotocol.io",
    }],
    stale: false,
    retrieved_at: "2026-08-22T09:00:00Z",
    sources: [{
      source: "registry.modelcontextprotocol.io",
      ok: true, stale: false, entries: 1,
    }],
  });
  vi.spyOn(api, "tunnel").mockResolvedValue({
    tunnels: [], can_manage: false, plugins: ["echo"], workspaces: [],
    assignments: {}, accounts: [], missing: "an OpenAI admin key and organization ID",
  });
  vi.spyOn(api, "endpoints").mockResolvedValue({
    aggregate: "http://127.0.0.1:18080/mcp", per_plugin_example: "/mcp/{plugin}",
    advertised: true, port: "18080",
  });
  vi.spyOn(api, "settings").mockResolvedValue({
    groups: [], values: {}, secrets_set: {}, encryption_available: true,
    bootstrap: [],
  });
  vi.spyOn(api, "users").mockResolvedValue({ users: [], count: 0 });
  vi.spyOn(api, "approvalPolicy").mockResolvedValue({
    rules: [], wildcard: "*", ceilings: ["low", "medium", "high"],
    default: "Every change is put to a person unless a rule authorises it.", unmatched: "none",
  });
  vi.spyOn(api, "mcpServerTools").mockResolvedValue({ tools: [], count: 0 });
  // A host with no providers and no sign-ups, which is what an upgrade
  // produces: the console must draw the password form and nothing else.
  vi.spyOn(api, "authOptions").mockResolvedValue({
    providers: [], registration: false,
  });
  vi.spyOn(api, "identities").mockResolvedValue({ identities: [], available: [] });
  vi.spyOn(api, "registrations").mockResolvedValue({ registrations: [], count: 0 });
  vi.spyOn(api, "redirectURIs").mockResolvedValue({ base: "", redirect_uris: {} });
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
  it("refuses a user, naming the permission they lack", async () => {
    stubApi("user");
    window.history.replaceState(null, "", "/marketplace");

    render(<App />);
    expect(await screen.findByText("Not for this account")).toBeInTheDocument();
    expect(screen.getByText("plugins:write")).toBeInTheDocument();
    await waitFor(() => expect(api.mcpServers).not.toHaveBeenCalled());
  });

  it("lets an administrator through", async () => {
    stubApi("admin");
    window.history.replaceState(null, "", "/marketplace");

    render(<App />);
    // Discovery, not an inventory: the catalogue is what the page is for, and
    // what is already installed is read only so the catalogue can say so.
    expect(await screen.findByText("Weather")).toBeInTheDocument();
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
        pending: 0, enabled_tools: 0, disabled: 0, extra_headers: [], declares_no_credential: false,
    discovery: {},
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
 * A section in the map that the router does not build renders "Nothing here",
 * and the sidebar happily links to it. The two are meant to be one table, so
 * the wiring is worth a test that goes through the real router.
 */
describe("the approval policy page", () => {
  beforeEach(() => {
    window.history.replaceState(null, "", "/settings/policy");
  });

  it("opens for a user, who may read the rules but not change them", async () => {
    stubApi("user");
    render(<App />);

    // By role: the sidebar links to it by the same words, and the point of
    // this test is that the page behind the link was built.
    expect(await screen.findByRole("heading", { name: "Policies", level: 1 }))
      .toBeInTheDocument();
    expect(screen.queryByText("Nothing here")).not.toBeInTheDocument();
    expect(screen.queryByText("Not for this account")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save rules" })).toBeNull();
  });

  it("offers an administrator the controls the API will accept", async () => {
    stubApi("admin");
    render(<App />);

    expect(await screen.findByRole("button", { name: "Add an allow rule" }))
      .toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add an always-ask rule" }))
      .toBeInTheDocument();
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
    expect(screen.getByText(/cannot change it yourself here/i)).toBeInTheDocument();
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

/**
 * The checklist is mounted by the console rather than by a page, so this is
 * where its wiring is defended. It waits for the browser to be idle before it
 * asks anything, which is why the idle callback is stubbed: jsdom has none, and
 * the fallback timer would make this a race.
 */
describe("the getting-started checklist", () => {
  const idle: IdleRequestCallback[] = [];

  beforeEach(() => {
    idle.length = 0;
    vi.stubGlobal("requestIdleCallback", (fn: IdleRequestCallback) => idle.push(fn));
    vi.stubGlobal("cancelIdleCallback", () => {});
    localStorage.clear();
    window.history.replaceState(null, "", "/");
  });

  afterEach(() => vi.unstubAllGlobals());

  async function whenIdle() {
    await act(async () => {
      idle.splice(0).forEach((fn) => fn({ didTimeout: false, timeRemaining: () => 0 }));
      await Promise.resolve();
    });
  }

  it("sits on the console without taking the page or the focus", async () => {
    stubApi("admin");
    render(<App />);
    await screen.findByText("Hello, Smoke");
    await whenIdle();

    expect(screen.getByRole("button", { name: /get started/i })).toBeInTheDocument();
    // Nothing waiting on the overview is still what the page is about.
    expect(screen.getByText("Nothing waiting")).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(document.body).toHaveFocus();
  });

  // Every step it lists takes admin to do. Offering them to somebody the server
  // would refuse is worse than offering nothing.
  it("is not offered to a user who could not complete any of it", async () => {
    stubApi("user");
    render(<App />);
    await screen.findByText("Hello, Smoke");
    await whenIdle();

    expect(screen.queryByRole("button", { name: /get started/i })).toBeNull();
  });
});
