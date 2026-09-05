import { describe, expect, it } from "vitest";
import type { Plugin, PluginInstance, PluginType } from "@/lib/api";
import { toRows } from "./PluginsList";

function plugin(name: string, overrides: Partial<Plugin> = {}): Plugin {
  return {
    name, type: name, version: "1", title: name, description: "",
    endpoint: `/mcp/${name}`, connect_url: `https://host/mcp/${name}`,
    health: "healthy", tools: [], mutations: [], required: false, settings: [],
    ...overrides,
  };
}

function instance(name: string, overrides: Partial<PluginInstance> = {}): PluginInstance {
  return {
    name, type: name, from_file: false, enabled: true, mounted: true, ...overrides,
  };
}

/**
 * The split is read from `runtime`, never inferred from a name or a type.
 *
 * The two kinds are managed in entirely different places, and a name has never
 * been a reliable way to tell them apart -- a remote server may be called
 * anything its operator likes, including the name of a builtin.
 */
describe("splitting plugins by runtime", () => {
  it("reads the runtime the instances endpoint sends", () => {
    const rows = toRows(
      [plugin("cnmaestro"), plugin("weather")],
      [
        instance("cnmaestro", { runtime: "builtin" }),
        instance("weather", { runtime: "mcp" }),
      ],
      [],
    );
    expect(rows.find((r) => r.name === "cnmaestro")?.runtime).toBe("builtin");
    expect(rows.find((r) => r.name === "weather")?.runtime).toBe("mcp");
  });

  // A remote server named after a builtin is exactly the case that breaks any
  // scheme based on the name.
  it("does not guess from the name", () => {
    const rows = toRows(
      [plugin("cnmaestro")],
      [instance("cnmaestro", { runtime: "mcp" })],
      [],
    );
    expect(rows[0]?.runtime).toBe("mcp");
  });

  // An older host sends no runtime at all. Builtin is the safe reading: it is
  // what every plugin compiled into the binary is, and a remote server always
  // has a row that says so.
  it("treats a missing runtime as builtin", () => {
    const rows = toRows([plugin("cnmaestro")], [instance("cnmaestro")], []);
    expect(rows[0]?.runtime).toBe("builtin");
  });
});

/**
 * A plugin that has not been configured is not mounted, so it is absent from
 * the plugins endpoint -- and its page is exactly where its settings form
 * lives. Dropping it from the list left an operator with a notice saying what
 * an integration needed and nowhere to type it.
 */
describe("instances that are configured but not serving", () => {
  const types: PluginType[] = [
    { name: "netbox", title: "NetBox", description: "IPAM", configurable: true },
  ];

  it("lists one that has never mounted", () => {
    const rows = toRows([], [instance("netbox", { mounted: false, missing: ["token"] })], types);
    expect(rows).toHaveLength(1);
    expect(rows[0]?.running).toBe(false);
    expect(rows[0]?.title).toBe("NetBox");
    expect(rows[0]?.healthMessage).toBe("Waiting on token.");
  });

  it("says it is disabled rather than broken", () => {
    const rows = toRows([], [instance("netbox", { mounted: false, enabled: false })], types);
    expect(rows[0]?.healthMessage).toBe("Disabled.");
  });

  /**
   * A removed plugin is not on this host and holds nothing, so it is not one
   * of the things this host serves. It used to stay here as a dimmed row with
   * a Restore beside it, which promised settings the removal had taken.
   */
  it("leaves out a plugin that was removed", () => {
    const rows = toRows(
      [],
      [
        instance("netbox", {
          mounted: false, enabled: false, removed: true, removed_by: "user:alice",
        }),
        instance("graylog", { mounted: false }),
      ],
      types,
    );
    expect(rows.map((r) => r.name)).toEqual(["graylog"]);
  });

  it("prefers a reported problem over the generic wording", () => {
    const rows = toRows(
      [],
      [instance("netbox", { mounted: false, problem: "the token was refused" })],
      types,
    );
    expect(rows[0]?.healthMessage).toBe("the token was refused");
  });

  it("does not list a mounted plugin twice", () => {
    const rows = toRows([plugin("netbox")], [instance("netbox")], types);
    expect(rows).toHaveLength(1);
    expect(rows[0]?.running).toBe(true);
  });
});

describe("tool counts", () => {
  it("separates reads from writes", () => {
    const rows = toRows(
      [plugin("cnmaestro", {
        tools: [
          { name: "cnmaestro_list_devices", kind: "read" },
          { name: "cnmaestro_list_sites", kind: "read" },
          { name: "cnmaestro_device_reboot", kind: "propose" },
        ],
      })],
      [instance("cnmaestro")],
      [],
    );
    expect(rows[0]?.reads).toBe(2);
    expect(rows[0]?.writes).toBe(1);
  });
});
