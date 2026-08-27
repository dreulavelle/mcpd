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
