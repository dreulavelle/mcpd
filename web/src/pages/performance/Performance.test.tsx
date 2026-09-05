import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import { api, type Performance as Perf } from "@/lib/api";
import { renderWith } from "@/test/render";
import { Performance } from "./Performance";

const BUDGET = 40_000;

function surface(over: Partial<Perf> = {}): Perf {
  return { tools: [], upstream: [], cache: [], result_budget_bytes: BUDGET, ...over };
}

function stub(p: Perf) {
  vi.spyOn(api, "performance").mockResolvedValue(p);
}

describe("the performance page", () => {
  /**
   * A host that has served nothing has nothing to show, and that is not a
   * fault. Rendering zeroes would read as measurements taken, which is a
   * different and wrong claim.
   */
  it("says nothing has been called rather than showing zeroes", async () => {
    stub(surface());
    renderWith(<Performance />);
    expect(await screen.findByText(/Nothing has been called yet/)).toBeInTheDocument();
    expect(screen.queryByText("Tool calls")).not.toBeInTheDocument();
  });

  /**
   * Refused and failed are different numbers calling for different actions, so
   * the table keeps them in separate columns rather than summing them into a
   * single "not ok".
   */
  it("keeps refusals apart from failures", async () => {
    stub(surface({
      tools: [{
        plugin: "graylog",
        tool: "search_messages",
        calls: { ok: 10, error: 2, denied: 3, rate_limited: 1 },
      }],
    }));
    renderWith(<Performance />);

    expect(await screen.findByText("Every tool")).toBeInTheDocument();
    const row = screen.getByText("graylog · search_messages").closest("tr")!;
    // 16 calls, 2 failed, 4 refused (denied plus rate limited).
    expect(row).toHaveTextContent("16");
    expect(row).toHaveTextContent("2");
    expect(row).toHaveTextContent("4");
  });

  /**
   * The whole point of measuring result size: an answer past the budget is one
   * the client cuts mid-JSON, and the page has to say so rather than showing a
   * large number that reads as merely large.
   */
  it("marks a tool whose answers are past the budget", async () => {
    stub(surface({
      tools: [{
        plugin: "cnmaestro",
        tool: "list_devices",
        calls: { ok: 4, error: 0, denied: 0, rate_limited: 0 },
        result_bytes: {
          count: 4,
          sum: 400_000,
          p50: 90_000,
          p95: 120_000,
          buckets: [
            { le: 512, count: 0 },
            { le: BUDGET, count: 1 },
            { le: null, count: 3 },
          ],
        },
      }],
    }));
    renderWith(<Performance />);

    expect(await screen.findByText("Largest answer (p95)")).toBeInTheDocument();
    // Rendered in the problem tone, which is the reading: not just big, cut.
    const tile = screen.getByText("Largest answer (p95)").parentElement!;
    expect(tile.querySelector(".text-problem")).not.toBeNull();
  });

  /**
   * The reading that started this: one echo benchmark call of 63us was
   * reported as a p95 of 4.75ms, because a lone sample interpolates to 95% of
   * the way through whichever bucket holds it. The estimate is not wrong so
   * much as unanswerable, and the exact mean sits in the same payload.
   */
  it("shows the exact mean beside a quantile one call cannot support", async () => {
    stub(surface({
      tools: [{
        plugin: "echo",
        tool: "get_benchmark_payload",
        calls: { ok: 1, error: 0, denied: 0, rate_limited: 0 },
        duration: {
          count: 1,
          sum: 0.0000631,
          // Where the estimate lands: most of the way through bucket one.
          p50: 0.00025,
          p95: 0.000475,
          buckets: [
            { le: 0.0005, count: 1 },
            { le: 0.001, count: 0 },
          ],
        },
      }],
    }));
    renderWith(<Performance />);

    expect(await screen.findByText("Every tool")).toBeInTheDocument();
    const row = screen.getByText("echo · get_benchmark_payload").closest("tr")!;
    // The call itself, exactly -- not the bucket it happened to fall in.
    expect(row).toHaveTextContent("63\u00b5s");
    // And the estimate is still shown, so the gap between them is legible.
    expect(row).toHaveTextContent("475\u00b5s");
  });

  /**
   * The bug the count replaced a percentile for.
   *
   * Ninety-nine terse answers and one that was cut: the 95th percentile sits
   * among the terse ones, so a `p95 >= budget` test reports nothing wrong
   * while a reply has already been truncated mid-JSON. A rare cut answer is
   * the ordinary shape of this fault.
   */
  it("flags one answer past the budget that a percentile hides", async () => {
    stub(surface({
      tools: [{
        plugin: "graylog",
        tool: "search_messages",
        calls: { ok: 100, error: 0, denied: 0, rate_limited: 0 },
        result_bytes: {
          count: 100,
          sum: 299_000,
          p50: 1_000,
          p95: 1_986,
          buckets: [
            { le: 512, count: 0 },
            { le: 2048, count: 99 },
            { le: BUDGET, count: 0 },
            { le: null, count: 1 },
          ],
        },
      }],
    }));
    renderWith(<Performance />);

    expect(await screen.findByText("Largest answer (p95)")).toBeInTheDocument();
    const tile = screen.getByText("Largest answer (p95)").parentElement!;
    expect(tile.querySelector(".text-problem")).not.toBeNull();
    expect(tile).toHaveTextContent("1 too large");
  });

  /**
   * The other side of the boundary, which is the easy one to break while
   * fixing the first: the budget is the ceiling a plugin builds *against*, so
   * an answer of exactly that size is inside it and was not cut. Counting the
   * bucket bounded at the budget -- `le >= budget` rather than `le > budget`
   * -- would report every well-behaved large answer as a fault.
   */
  it("leaves an answer of exactly the budget unflagged", async () => {
    stub(surface({
      tools: [{
        plugin: "echo",
        tool: "get_benchmark_payload",
        calls: { ok: 4, error: 0, denied: 0, rate_limited: 0 },
        result_bytes: {
          count: 4,
          sum: 4 * BUDGET,
          p50: 30_000,
          p95: 39_000,
          buckets: [
            { le: 20_000, count: 0 },
            { le: BUDGET, count: 4 },
            { le: 60_000, count: 0 },
          ],
        },
      }],
    }));
    renderWith(<Performance />);

    expect(await screen.findByText("Largest answer (p95)")).toBeInTheDocument();
    const tile = screen.getByText("Largest answer (p95)").parentElement!;
    expect(tile.querySelector(".text-problem")).toBeNull();
  });

  /**
   * A shared fetch still went upstream once. Counting it as a hit would
   * overstate what the cache is holding, which is the number an operator
   * decides a lifetime against.
   */
  it("counts a shared fetch apart from a hit", async () => {
    stub(surface({
      tools: [{
        plugin: "observium",
        tool: "list_devices",
        calls: { ok: 1, error: 0, denied: 0, rate_limited: 0 },
      }],
      cache: [{ plugin: "observium", kind: "devices", hit: 6, miss: 2, shared: 2 }],
    }));
    renderWith(<Performance />);

    expect(await screen.findByText("Read caches")).toBeInTheDocument();
    const row = screen.getByText("observium · devices").closest("tr")!;
    // Six of ten reads were hits: 60%, not the 80% that folding in shared gives.
    expect(row).toHaveTextContent("60%");
  });
});
