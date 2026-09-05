import { describe, expect, it } from "vitest";
import { attention } from "./Attention";
import type { Certificate, MCPServer, Operation, Plugin, PluginInstance } from "@/lib/api";

const nothing = {
  admin: true, plugins: [], instances: [], tunnels: [], unknown: [],
  registrations: [], servers: [], certificates: [], updates: null, chainBrokenAt: null,
};

const plugin = (p: Partial<Plugin>): Plugin => ({
  name: "x", type: "x", version: "1", title: "X", description: "", endpoint: "/mcp/x",
  connect_url: "", health: "healthy", tools: [], mutations: [], required: false,
  settings: [], ...p,
});

describe("what needs attention", () => {
  it("says so when nothing does", () => {
    expect(attention(nothing)).toEqual([]);
  });

  // Worst first: a tampered history and a change that may have landed come
  // before a version number, whatever order the sources answered in.
  it("puts what matters most first", () => {
    const items = attention({
      ...nothing,
      updates: { enabled: true, current: "1.0.0", latest: "1.1.0", update_available: true, comparable: true },
      unknown: [{ id: "op" } as Operation],
      chainBrokenAt: 12,
    });
    expect(items.map((i) => i.key)).toEqual(["chain", "unknown", "update"]);
    expect(items[1]!.to).toBe("/approvals?state=indeterminate");
  });

  it("names each plugin that is not well, and the ones switched on but not serving", () => {
    const items = attention({
      ...nothing,
      plugins: [plugin({ name: "graylog", health: "degraded", health_message: "slow" }), plugin({ name: "fine" })],
      instances: [{ name: "cnmaestro", type: "cnmaestro", from_file: false, enabled: true, mounted: false } as PluginInstance],
    });
    expect(items.map((i) => i.key)).toEqual(["plugin:graylog", "instances"]);
    expect(items[0]!.text).toContain("slow");
    expect(items[1]!.text).toContain("cnmaestro");
  });

  // An operator who cannot open the page is not told about it: a line that
  // leads to a refusal is worse than no line.
  it("keeps administrative items from an account that cannot act on them", () => {
    const input = {
      ...nothing,
      registrations: [{ email: "new@example.com" } as never],
      certificates: [{ status: "expired" } as Certificate],
    };
    expect(attention({ ...input, admin: true }).map((i) => i.key)).toEqual(["registrations", "certificates"]);
    expect(attention({ ...input, admin: false })).toEqual([]);
  });

  it("points at a remote server whose tools nobody has classified", () => {
    const items = attention({
      ...nothing,
      servers: [{ name: "remote", enabled: true, pending: 3, discovery: {} } as MCPServer],
    });
    expect(items[0]!.text).toContain("3 tools");
    expect(items[0]!.to).toBe("/plugins/remote");
  });

  // Each tunnel state is a different thing to do: a tunnel OpenAI no longer
  // has needs remaking, one the supervisor gave up on needs a person, one
  // being retried needs nothing yet, and a degraded one is worth a look.
  it("tells a gone tunnel from a stopped one from one being retried", () => {
    const items = attention({
      ...nothing,
      tunnels: [
        { state: "connected", plugin: "a", tunnel_id: "t1", upstream: "missing" },
        { state: "failed", plugin: "b", tunnel_id: "t2", message: "bad key" },
        { state: "failed", plugin: "c", tunnel_id: "t3", attempts: 2, next_retry_at: "2026-09-02T10:00:00Z" },
        { state: "connected", plugin: "d", tunnel_id: "t4", degraded: true },
        { state: "connected", plugin: "e", tunnel_id: "t5" },
      ],
    });
    expect(items.map((i) => [i.key, i.tone])).toEqual([
      ["tunnel-gone:t1", "problem"],
      ["tunnel-failed:t2", "problem"],
      ["tunnel-retrying:t3", "attention"],
      ["tunnel-degraded:t4", "attention"],
    ]);
  });

  // A diagnosis can run to a paragraph. The digest keeps its first sentence;
  // the rest is on the plugin's page. It is quoted, because the plugin wrote
  // it and the dashboard did not: run into the line it reads as the
  // dashboard's own words and as a paragraph of route names.
  it("keeps a long health message to its first sentence, and quotes it", () => {
    const items = attention({
      ...nothing,
      plugins: [plugin({ name: "textable", health: "degraded",
        health_message: "/health timed out inside Textable (HTTP 408). This is not transient: the whole list has to be built in one response. Do not retry." })],
    });
    expect(items[0]!.text).toBe(
      `textable is having trouble. It said: "/health timed out inside Textable (HTTP 408). …"`);
  });

  // The bug: a tunnel's raw client log line, its wrapped Go error and the
  // control plane's error code were all glued into the digest line, so the
  // Overview read `The echo connector has stopped and will not restart on its
  // own: tunnel: OpenAI refused this account's key for this tunnel
  // (tunnel_use_forbidden): …`. None of the three belongs in a sentence.
  it("never puts evidence in a sentence", () => {
    const carrying = {
      state: "failed" as const,
      plugin: "echo",
      tunnel_id: "t1",
      message: "OpenAI no longer accepts this account's key. Paste a new runtime key into the account under Settings › ChatGPT.",
      detail: "tunnel: OpenAI refused this account's key (token_invalidated)",
      code: "token_invalidated",
      trouble: `time=2026-09-04T03:37:01Z level=WARN msg="poll failed; backing off" client_instance_id=abc`,
      degraded: true,
    };
    const items = attention({ ...nothing, tunnels: [carrying, { ...carrying, tunnel_id: "t2", state: "connected" }] });

    expect(items.length).toBeGreaterThan(0);
    for (const item of items) {
      expect(item.text).not.toContain(carrying.trouble);
      expect(item.text).not.toContain("level=WARN");
      expect(item.text).not.toContain("tunnel: ");
      expect(item.text).not.toMatch(/\([a-z]+(_[a-z]+)+\)/);
    }
  });
});
