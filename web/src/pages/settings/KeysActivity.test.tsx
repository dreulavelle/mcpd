import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import { api, type ApiKey, type Caller } from "@/lib/api";
import { builtinPermissions } from "@/lib/permissions";
import { renderWith } from "@/test/render";
import { Keys } from "./Keys";

function apiKey(overrides: Partial<ApiKey> = {}): ApiKey {
  return {
    id: "key_abc",
    name: "reporting agent",
    role: "role_operator",
    role_name: "Operator",
    grants: [{ plugin: "*", level: "write" }],
    reaches: [
      { plugin: "graylog", level: "write" },
      { plugin: "observium", level: "write" },
      { plugin: "echo", level: "write" },
    ],
    permissions: builtinPermissions("role_operator"),
    groups: [],
    status: "active",
    created_by: "user:admin",
    created_at: "2026-08-01T09:00:00Z",
    ...overrides,
  };
}

function stub(keys: ApiKey[], callers: Caller[]) {
  vi.spyOn(api, "keys").mockResolvedValue({ keys, count: keys.length });
  vi.spyOn(api, "groups").mockResolvedValue({ groups: [], count: 0 });
  vi.spyOn(api, "callers").mockResolvedValue({
    callers, count: callers.length, days: 30,
  });
}

/**
 * What a key may reach and what it has reached are different facts, and the
 * gap between them is the whole of a grant review.
 */
describe("what a key has actually reached", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    window.localStorage.clear();
  });

  it("shows what a key touched, beside what it is allowed to touch", async () => {
    stub([apiKey()], [{
      principal: "key:key_abc", role: "user", calls: 40, errors: 0, denied: 0,
      first_seen: "2026-08-20T09:00:00Z", last_seen: "2026-08-30T09:00:00Z",
      plugins: ["graylog"],
    }]);
    renderWith(<Keys />);

    // Permitted three, reached one: the case worth seeing.
    expect(await screen.findByText("graylog")).toBeInTheDocument();
    expect(screen.getByText(/40 calls/)).toBeInTheDocument();
  });

  // A key that has never called anything is the clearest candidate for
  // revoking, so an empty result is a statement rather than a blank cell.
  it("says plainly when a key has never been used", async () => {
    stub([apiKey({ expires_at: "2026-12-01T00:00:00Z" })], []);
    renderWith(<Keys />);

    await screen.findByText("reporting agent");
    // "Never" as the key's own use, not its Expires column, which the fixture
    // gives a real date so the two cannot be confused for one another.
    expect(screen.getByText("Never")).toBeInTheDocument();
  });

  it("counts refusals, which are the interesting ones", async () => {
    stub([apiKey()], [{
      principal: "key:key_abc", role: "user", calls: 12, errors: 0, denied: 5,
      first_seen: "2026-08-20T09:00:00Z", last_seen: "2026-08-30T09:00:00Z",
      plugins: ["graylog"],
    }]);
    renderWith(<Keys />);

    expect(await screen.findByText(/5 refused/)).toBeInTheDocument();
  });

  // A host not keeping a record answers 501, and the column must degrade to
  // the truthful thing rather than breaking the page.
  it("still lists keys when nothing is being recorded", async () => {
    vi.spyOn(api, "keys").mockResolvedValue({
      keys: [apiKey({ expires_at: "2026-12-01T00:00:00Z" })], count: 1,
    });
    vi.spyOn(api, "groups").mockResolvedValue({ groups: [], count: 0 });
    vi.spyOn(api, "callers").mockRejectedValue(new Error("not configured"));
    renderWith(<Keys />);

    expect(await screen.findByText("reporting agent")).toBeInTheDocument();
    expect(screen.getByText("Never")).toBeInTheDocument();
  });
});
