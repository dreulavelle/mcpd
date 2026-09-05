import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
  api, type CallBucket, type CallSummary, type HealthReport, type Plugin,
  type PluginInstance, type TunnelStatus,
} from "@/lib/api";
import type { Item } from "@/components/Attention";
import { renderWith, sessionFor } from "@/test/render";
import { Overview, verdict } from "./Overview";

const hour = 3_600_000;
// Anchored where the endpoint anchors it: the last bucket is the hour in
// progress. A fixture stuck in the past reads as a day nobody has called in
// for months, which is a different sentence.
const top = Math.floor(Date.now() / hour) * hour;

/** A day of hourly buckets, shaped by `perHour`. */
function day(perHour: (i: number) => Partial<CallBucket> = () => ({})): CallSummary {
  const buckets: CallBucket[] = Array.from({ length: 24 }, (_, i) => ({
    at: new Date(top - (23 - i) * hour).toISOString(),
    ok: 0, error: 0, denied: 0, rate_limited: 0,
    ...perHour(i),
  }));
  const sum = (k: keyof Omit<CallBucket, "at" | "last_at">) =>
    buckets.reduce((t, b) => t + (b[k] as number), 0);
  const busiest = [...buckets].reverse()
    .find((b) => b.ok + b.error + b.denied + b.rate_limited > 0);
  return {
    hours: 24,
    buckets,
    plugins: [],
    total: sum("ok") + sum("error") + sum("denied") + sum("rate_limited"),
    errors: sum("error"),
    denied: sum("denied"),
    // The host answers with the moment of the last call; the fixture puts it
    // where a bucket that had something in it opened.
    last_at: busiest?.at,
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
  instances?: PluginInstance[];
  tunnels?: TunnelStatus[];
  summary?: CallSummary;
}

function stub({
  health = { status: "up", checks: [] },
  // One system and one connector, so the page is not the "nothing is set up
  // yet" case unless a test asks for it.
  plugins = [plugin({})],
  instances = [],
  tunnels = [{ state: "connected", tunnel_id: "tun_1", requests: 4 }],
  summary = day(),
}: Stub = {}) {
  vi.spyOn(api, "operations").mockResolvedValue({ operations: [], count: 0 });
  vi.spyOn(api, "plugins").mockResolvedValue({ plugins, count: plugins.length });
  vi.spyOn(api, "instances").mockResolvedValue({ instances, count: instances.length });
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
  const well: HealthReport = { status: "up", checks: [] };
  const base = {
    items: [], connected: 1, health: well,
    summary: day((i) => (i === 23 ? { ok: 5 } : {})),
  };
  const say = (over: Partial<Parameters<typeof verdict>[0]>) =>
    verdict({ ...base, systems: 2, connectors: 1, ...over })!;

  it("says everything is working when nothing needs anybody", () => {
    expect(say({}).text).toBe("Everything is working.");
  });

  it("counts what needs somebody, in words", () => {
    const one = say({ items: [item("attention")] });
    expect(one.text).toBe("Everything is working. One thing needs you.");
    expect(one.tone).toBe("attention");

    expect(say({ items: [item("attention"), item("problem")] }).text)
      .toBe("Something is wrong. Two things need you.");
  });

  /**
   * A change waiting on a decision is exactly a thing that needs the reader,
   * and it sat in its own card being counted by nothing. The sentence said
   * "Everything is working." over two proposals about to run out of time.
   */
  it("counts a change waiting on a decision as a thing that needs you", () => {
    expect(say({ waiting: 1 }).text)
      .toBe("Everything is working. One thing needs you.");
    expect(say({ waiting: 2, items: [item("attention")] }).text)
      .toBe("Everything is working. Three things need you.");
  });

  /**
   * "mcpd 0.17 is available" is worth reading and is not a thing anybody has
   * to do today. Counted, it turned the headline amber every time a release
   * shipped, and an amber headline that means nothing is one nobody reads.
   */
  it("does not let a line that is only news turn the headline amber", () => {
    const v = say({ items: [item("info")] });
    expect(v.text).toBe("Everything is working.");
    expect(v.tone).toBe("good");
  });

  // "Everything is working" beside a broken system is the sentence somebody
  // reads instead of the list under it.
  it("says something is wrong when one of them is a problem", () => {
    const v = say({ items: [item("problem"), item("attention")] });
    expect(v.text).toBe("Something is wrong. Two things need you.");
    expect(v.tone).toBe("problem");
  });

  /**
   * The host's own checks were not in the sentence at all, so a host whose
   * critical check was down led with "Everything is working." above a Host
   * card reading "1 of 3 checks is not passing". Nothing in the attention list
   * covers them; they are a separate fact and they have to be read.
   *
   * A degraded check replaces the state word rather than following it: a
   * sentence should not be contradicted by the one after it.
   */
  it("reads the host's own checks", () => {
    const down = say({ health: { status: "down", checks: [] } });
    expect(down.text).toBe("Something is wrong.");
    expect(down.tone).toBe("problem");

    const degraded = say({ health: { status: "degraded", checks: [] } });
    expect(degraded.text).toBe("A check on this host is not passing.");
    expect(degraded.tone).toBe("attention");

    expect(say({ health: { status: "degraded", checks: [] }, waiting: 1 }).text)
      .toBe("A check on this host is not passing. One thing needs you.");
  });

  /**
   * A host nobody has called for hours is not a host that is failing, and it
   * is not one that is fine either. Saying only "Everything is working" over a
   * connector that has served nothing since breakfast is the reassurance that
   * costs somebody an afternoon.
   *
   * The moment the host reported, not a count of empty bars: an hour of bars
   * is anywhere between a minute and two hours of real time, and the hour a
   * bucket opened is not when the call inside it was made.
   */
  it("says when the last call came in, once quiet is long enough to mean something", () => {
    expect(say({ summary: day((i) => (i <= 21 ? { ok: 4 } : {})) }).text)
      .toBe("Everything is working.");

    // The host says the call landed at 41 minutes past, not on the hour its
    // bucket opened, and that is the time in the sentence.
    const six = day((i) => (i <= 17 ? { ok: 4 } : {}));
    const lastCall = new Date(new Date(six.buckets[17]!.at).getTime() + 41 * 60_000);
    six.last_at = lastCall.toISOString();
    const shown = lastCall.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });

    expect(say({ summary: six }).text)
      .toBe(`Everything is working. Nothing has come through since ${shown}.`);
  });

  // No connector is up, so nothing coming through is the arrangement rather
  // than a fault.
  it("does not call it quiet when no connector is up", () => {
    const six = day((i) => (i <= 17 ? { ok: 4 } : {}));
    expect(say({ connected: 0, summary: six }).text).toBe("Everything is working.");
  });

  it("says when there is nothing set up at all", () => {
    const v = say({ systems: 0, connectors: 0 });
    expect(v.text).toBe("Nothing is set up yet.");
    expect(v.tone).toBe("neutral");
  });

  /**
   * An empty deployment is the last thing considered, not the first. It used
   * to return before the host's checks, the attention list and the approvals
   * were looked at, so a host with nothing mounted and a failing check read
   * "Nothing is set up yet."
   */
  it("does not let an empty deployment hide what is wrong with it", () => {
    expect(say({ systems: 0, connectors: 0, health: { status: "down", checks: [] } }).text)
      .toBe("Something is wrong.");
    expect(say({ systems: 0, connectors: 0, waiting: 1 }).text)
      .toBe("Nothing is set up yet. One thing needs you.");
  });

  // Nothing read is not nothing there: a person who may not list the systems
  // must not be told this host has none.
  it("does not call an unread host empty", () => {
    expect(say({ systems: undefined, connectors: undefined }).text)
      .toBe("Everything is working.");
  });

  /**
   * The bug this pair exists for: with the server unreachable on a first load,
   * every request failed, and health failing was the only thing that counted
   * as an answer. The page led with a green "Everything is working." over an
   * empty screen -- a claim made from an absence of answers.
   */
  it("does not call a host well on the strength of a failed read", () => {
    const blind = verdict({
      items: [], connected: 0, health: null, summary: undefined,
    })!;
    expect(blind.text).toBe("This host's health could not be read.");
    expect(blind.tone).not.toBe("good");
    expect(blind.tone).toBe("attention");
  });

  it("still says how the host is when something else answered", () => {
    const v = say({ health: null });
    expect(v.text)
      .toBe("Everything is working. This host's health could not be read.");
    expect(v.tone).toBe("attention");
  });

  /**
   * A custom role holding none of the read permissions gets nothing to say a
   * sentence about. A claim about a host this account cannot see is worse than
   * no claim.
   */
  it("says nothing at all when nothing could be read", () => {
    expect(verdict({ items: [], connected: 0, health: undefined, summary: undefined }))
      .toBeNull();
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

  /**
   * The chart is a picture. The totals behind it have to be readable without
   * one, and a screen reader gets them from the label.
   *
   * Refused counts both the gate's refusals and the rate limiter's, because
   * the bars stack both. It read only the first, so the number under the chart
   * disagreed with the chart.
   */
  it("names the totals on the chart, counting every refusal", async () => {
    stub({ summary: day((i) => (i === 23 ? { ok: 10, error: 2, denied: 1, rate_limited: 1 } : {})) });
    renderWith(<Overview />);

    expect(await screen.findByRole("img", {
      name: "14 calls in the last 24 hours, 2 failed and 2 refused.",
    })).toBeInTheDocument();
    expect(screen.getByText("· 2 refused")).toBeInTheDocument();
  });

  // "1 calls" is the sort of thing a person notices and nobody fixes.
  it("counts one call as a call", async () => {
    stub({ summary: day((i) => (i === 23 ? { ok: 1 } : {})) });
    renderWith(<Overview />);

    expect(await screen.findByRole("img", {
      name: "1 call in the last 24 hours, 0 failed and 0 refused.",
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

  /**
   * A system somebody switched off is in the instance list and not the serving
   * one, so it had no row at all -- and a host whose systems are every one of
   * them switched off read "Nothing is set up yet.", which is a different
   * thing and a different afternoon.
   */
  it("keeps a row for a system that is switched off", async () => {
    stub({
      plugins: [],
      instances: [{
        name: "observium", type: "observium", from_file: false,
        enabled: false, mounted: false,
      }],
      tunnels: [],
    });
    renderWith(<Overview />);

    const systems = (await screen.findByText("Systems")).closest("section")!;
    expect(within(systems).getByText("observium")).toBeInTheDocument();
    expect(within(systems).getByText("disabled")).toBeInTheDocument();
    expect(screen.queryByText("Nothing is set up yet.")).not.toBeInTheDocument();
  });

  it("says where to set a connector up when there is none", async () => {
    stub({ tunnels: [] });
    renderWith(<Overview />);

    expect(await screen.findByText(/No connector is set up yet/)).toBeInTheDocument();
    const connectors = screen.getByText("Connectors").closest("section")!;
    expect(within(connectors).getByRole("link", { name: "Tunnels" }))
      .toHaveAttribute("href", "/tunnels");
  });

  /**
   * Overview is the one route every signed-in account can open, and a custom
   * role can hold none of the read permissions behind it. A header over a
   * blank page reads as a dashboard that is broken rather than one this
   * account may not see.
   */
  it("says so to an account that may read none of it", async () => {
    stub();
    const blind = sessionFor("admin", { permissions: [] });
    renderWith(<Overview />, { session: blind });

    expect(await screen.findByText(/Nothing on this host is visible to your account/))
      .toBeInTheDocument();
    expect(screen.queryByText("Systems")).not.toBeInTheDocument();
    // And nothing offers a page that would refuse them.
    expect(screen.queryByRole("link", { name: "Connect a client" }))
      .not.toBeInTheDocument();
  });

  /**
   * A system that stopped serving still took the calls it took, and the count
   * is half the reason somebody looks at the row.
   */
  it("keeps the call count on a system that is no longer serving", async () => {
    stub({
      plugins: [],
      instances: [{
        name: "graylog", type: "graylog", from_file: false,
        enabled: true, mounted: false,
      }],
      summary: { ...day(), plugins: [{ plugin: "graylog", calls: 812, errors: 0 }] },
    });
    renderWith(<Overview />);

    const systems = (await screen.findByText("Systems")).closest("section")!;
    expect(within(systems).getByText("waiting on settings")).toBeInTheDocument();
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
