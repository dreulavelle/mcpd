import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
  api, type CallBucket, type CallSummary, type HealthReport, type Plugin,
  type TunnelStatus,
} from "@/lib/api";
import type { Item } from "@/components/Attention";
import { renderWith } from "@/test/render";
import { Overview, verdict } from "./Overview";

const hour = 3_600_000;
const noon = Date.UTC(2026, 7, 29, 12, 0, 0);

/** A day of buckets, `calls` of them in the last hour and none before it. */
function day(perHour: (i: number) => Partial<CallBucket> = () => ({})): CallSummary {
  const buckets: CallBucket[] = Array.from({ length: 24 }, (_, i) => ({
    at: new Date(noon - (23 - i) * hour).toISOString(),
    ok: 0, error: 0, denied: 0, rate_limited: 0,
    ...perHour(i),
  }));
  const sum = (k: keyof Omit<CallBucket, "at">) => buckets.reduce((t, b) => t + b[k], 0);
  return {
    hours: 24,
    buckets,
    plugins: [],
    total: sum("ok") + sum("error") + sum("denied") + sum("rate_limited"),
    errors: sum("error"),
    denied: sum("denied"),
  };
}

const plugin = (p: Partial<Plugin>): Plugin => ({
  name: "graylog", type: "graylog", version: "1", title: "Graylog", description: "",
  endpoint: "/mcp/graylog", connect_url: "", health: "healthy", tools: [],
  mutations: [], required: false, settings: [], ...p,
});

interface Stub {
  health?: HealthReport | null;
  plugins?: Plugin[];
  tunnels?: TunnelStatus[];
  summary?: CallSummary;
}

function stub({
  health = { status: "up", checks: [] },
  // One system and one connector, so the page is not the "nothing is set up
  // yet" case unless a test asks for it.
  plugins = [plugin({})],
  tunnels = [{ state: "connected", tunnel_id: "tun_1", requests: 4 }],
  summary = day(),
}: Stub = {}) {
  vi.spyOn(api, "operations").mockResolvedValue({ operations: [], count: 0 });
  vi.spyOn(api, "plugins").mockResolvedValue({ plugins, count: plugins.length });
  vi.spyOn(api, "instances").mockResolvedValue({ instances: [], count: 0 });
  vi.spyOn(api, "tunnel").mockResolvedValue({
    tunnels, can_manage: false, plugins: [], workspaces: [], assignments: {}, accounts: [],
  });
  vi.spyOn(api, "audit").mockResolvedValue({ records: [], count: 0 });
  vi.spyOn(api, "callers").mockResolvedValue({ callers: [], count: 0, days: 7 });
  vi.spyOn(api, "callSummary").mockResolvedValue(summary);
  vi.spyOn(api, "resources").mockResolvedValue({
    version: "0.16.0", started_at: "", uptime_seconds: 3 * 86_400, goroutines: 1,
    os_threads: 1, num_cpu: 1, gomaxprocs: 1, heap_in_use_bytes: 0, heap_alloc_bytes: 0,
    stack_in_use_bytes: 0, sys_bytes: 0, total_alloc_bytes: 0, gc_cycles: 0,
    gc_pause_total_ms: 0, gc_cpu_percent: 0,
  });
  if (health === null) {
    vi.spyOn(api, "health").mockRejectedValue(new Error("down"));
  } else {
    vi.spyOn(api, "health").mockResolvedValue(health);
  }
  // Every source the attention list asks for on its own account.
  vi.spyOn(api, "mcpServers").mockResolvedValue({ servers: [] });
  vi.spyOn(api, "certificates").mockResolvedValue({ certificates: [], count: 0 });
  vi.spyOn(api, "registrations").mockResolvedValue({ registrations: [], count: 0 });
  vi.spyOn(api, "updates").mockResolvedValue({
    enabled: false, current: "0.16.0", update_available: false, comparable: true,
  });
  vi.spyOn(api, "verifyAudit").mockResolvedValue({ intact: true, broken_at: 0 });
}

const item = (tone: Item["tone"]): Item =>
  ({ key: `k${Math.random()}`, tone, text: "x", to: "/", linkLabel: "x" });

/**
 * The sentence at the top says the state of the host in one line. It replaced
 * four counters that between them said how many of everything there was and
 * nothing about whether any of it was well.
 */
describe("the verdict", () => {
  const base = { items: [], connected: 1, summary: day((i) => (i === 23 ? { ok: 5 } : {})), healthUnread: false };

  it("says everything is working when nothing needs anybody", () => {
    expect(verdict({ ...base, systems: 2, connectors: 1 }).text)
      .toBe("Everything is working.");
  });

  it("counts what needs somebody, in words", () => {
    const one = verdict({ ...base, systems: 2, connectors: 1, items: [item("attention")] });
    expect(one.text).toBe("Everything is working. One thing needs you.");
    expect(one.tone).toBe("attention");

    const two = verdict({
      ...base, systems: 2, connectors: 1, items: [item("attention"), item("info")],
    });
    expect(two.text).toBe("Everything is working. Two things need you.");
  });

  // "Everything is working" beside a broken plugin is the sentence somebody
  // reads instead of the list under it.
  it("says something is wrong when one of them is a problem", () => {
    const v = verdict({
      ...base, systems: 2, connectors: 1, items: [item("problem"), item("attention")],
    });
    expect(v.text).toBe("Something is wrong. Two things need you.");
    expect(v.tone).toBe("problem");
  });

  /**
   * A host nobody has called for hours is not a host that is failing, and it is
   * not one that is fine either. Saying only "Everything is working" over a
   * connector that has served nothing since breakfast is the reassurance that
   * costs somebody an afternoon.
   */
  it("says how long it has been quiet, once quiet is long enough to mean something", () => {
    const twoHours = day((i) => (i <= 21 ? { ok: 4 } : {}));
    expect(verdict({ ...base, systems: 2, connectors: 1, summary: twoHours }).text)
      .toBe("Everything is working.");

    const six = day((i) => (i <= 17 ? { ok: 4 } : {}));
    expect(verdict({ ...base, systems: 2, connectors: 1, summary: six }).text)
      .toBe("Everything is working. Nothing has come through for 6 hours.");
  });

  // No connector is up, so nothing coming through is the arrangement rather
  // than a fault.
  it("does not call it quiet when no connector is up", () => {
    const six = day((i) => (i <= 17 ? { ok: 4 } : {}));
    expect(verdict({ ...base, systems: 2, connectors: 1, connected: 0, summary: six }).text)
      .toBe("Everything is working.");
  });

  it("says when there is nothing set up at all", () => {
    const v = verdict({ ...base, systems: 0, connectors: 0 });
    expect(v.text).toBe("Nothing is set up yet.");
    expect(v.tone).toBe("neutral");
  });

  // Nothing read is not nothing there: a person who may not list the plugins
  // must not be told this host has none.
  it("does not call an unread host empty", () => {
    expect(verdict({ ...base, systems: undefined, connectors: undefined }).text)
      .toBe("Everything is working.");
  });

  it("says when the host's health could not be read", () => {
    expect(verdict({ ...base, systems: 2, connectors: 1, healthUnread: true }).text)
      .toBe("Everything is working. This host's health could not be read.");
  });
});

describe("the overview", () => {
  beforeEach(() => {
    window.history.replaceState(null, "", "/");
  });

  it("leads with the verdict and offers a way to connect a client", async () => {
    stub({ summary: day((i) => (i === 23 ? { ok: 5 } : {})) });
    renderWith(<Overview />);

    expect(await screen.findByText("Everything is working.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Connect a client" }))
      .toHaveAttribute("href", "/clients");
  });

  // The chart is a picture. The totals behind it have to be readable without
  // one, and a screen reader gets them from the label.
  it("names the totals on the chart", async () => {
    stub({ summary: day((i) => (i === 23 ? { ok: 10, error: 2, denied: 1 } : {})) });
    renderWith(<Overview />);

    expect(await screen.findByRole("img", {
      name: "13 calls in the last 24 hours, 2 failed and 1 refused.",
    })).toBeInTheDocument();
  });

  it("says so when a day has gone by with no calls", async () => {
    stub();
    renderWith(<Overview />);

    expect(await screen.findByRole("img", { name: "No calls in the last 24 hours." }))
      .toBeInTheDocument();
    expect(screen.getByText("No calls in the last 24 hours.")).toBeInTheDocument();
  });

  /**
   * Overview is the one page everybody signed in can open, so a card whose
   * source refuses the reader has to go quiet rather than shout about it.
   */
  it("goes quiet about a window it was not allowed to read", async () => {
    stub();
    vi.spyOn(api, "callSummary").mockRejectedValue(new Error("forbidden"));
    renderWith(<Overview />);

    expect(await screen.findByText("Everything is working.")).toBeInTheDocument();
    expect(screen.queryByText("Last 24 hours")).not.toBeInTheDocument();
  });

  it("names every system and what it is doing", async () => {
    stub({
      plugins: [
        plugin({ name: "graylog" }),
        plugin({ name: "observium", health: "unhealthy", health_message: "Refused." }),
      ],
      summary: { ...day(), plugins: [{ plugin: "graylog", calls: 812, errors: 0 }] },
    });
    renderWith(<Overview />);

    const systems = (await screen.findByText("Systems")).closest("section")!;
    expect(within(systems).getByText("graylog")).toBeInTheDocument();
    expect(within(systems).getByText("working")).toBeInTheDocument();
    expect(within(systems).getByText("not working")).toBeInTheDocument();
    expect(within(systems).getByText("812 calls")).toBeInTheDocument();
  });
});

/**
 * Health is content here, because it stopped being a pill in the sidebar.
 *
 * "All good" beside the navigation could not say which check, or what the
 * check complained about, and the detail was in a tooltip -- which is not
 * somewhere a person on a phone can reach. The endpoint has always returned a
 * list with a message on each entry; the list is what gets rendered.
 */
describe("the host's health on the overview", () => {
  beforeEach(() => {
    window.history.replaceState(null, "", "/");
  });

  // A day with nothing wrong does not need a table of six passing rows on the
  // first screen; a day with something wrong is read for exactly that row.
  it("keeps the checks folded away while they all pass", async () => {
    stub({
      health: {
        status: "up",
        checks: [
          { name: "database", status: "up", critical: true },
          { name: "tunnel", status: "up", critical: false, message: "connected" },
        ],
      },
    });
    renderWith(<Overview />);

    expect(await screen.findByText("All 2 checks are passing.")).toBeInTheDocument();
    expect(screen.queryByText("database")).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Show checks" }));
    expect(screen.getByText("database")).toBeInTheDocument();
    expect(screen.getByText("connected")).toBeInTheDocument();
  });

  // The failing check is why anybody is reading this table, so it is open and
  // it does not have to be found among the passing ones.
  it("opens on a failing check, and puts it first with the reason it gave", async () => {
    stub({
      health: {
        status: "degraded",
        checks: [
          { name: "database", status: "up", critical: true },
          { name: "upstream", status: "degraded", critical: false, message: "slow to answer" },
        ],
      },
    });
    renderWith(<Overview />);

    const rows = await screen.findAllByRole("row");
    // Row 0 is the header.
    expect(rows[1]).toHaveTextContent("upstream");
    expect(rows[1]).toHaveTextContent("Degraded");
    expect(rows[1]).toHaveTextContent("slow to answer");
    expect(screen.getByText("1 of 2 checks is not passing.")).toBeInTheDocument();
  });

  // A check that is down and not critical is a different fact from a host that
  // is down, and saying which is the whole reason the field exists.
  it("marks a failing check that is not critical", async () => {
    stub({
      health: { status: "degraded", checks: [{ name: "upstream", status: "down", critical: false }] },
    });
    renderWith(<Overview />);

    expect(await screen.findByText("not critical")).toBeInTheDocument();
  });

  it("does not say a check passed when it never got an answer", async () => {
    stub({ health: null });
    renderWith(<Overview />);

    expect(await screen.findByText(/nothing here says whether its checks are passing/))
      .toBeInTheDocument();
    expect(screen.queryByText("Passing")).not.toBeInTheDocument();
    // And the sentence at the top does not claim a state it never read.
    expect(screen.getByText(/This host's health could not be read\.$/)).toBeInTheDocument();
  });
});
