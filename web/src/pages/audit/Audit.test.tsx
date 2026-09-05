import { describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, type AuditRecord } from "@/lib/api";
import { renderWith, sessionFor } from "@/test/render";
import { Audit } from "./Audit";

/** A moment today, so the page's own "Today" heading is the one under test. */
function today(hour: number, minute: number): string {
  const d = new Date();
  d.setHours(hour, minute, 0, 0);
  return d.toISOString();
}

function yesterday(hour: number, minute: number): string {
  const d = new Date();
  d.setDate(d.getDate() - 1);
  d.setHours(hour, minute, 0, 0);
  return d.toISOString();
}

/**
 * A whole sentence, which is split across elements because the systems, keys
 * and changes named in it are links. The default text query sees only a node's
 * own text nodes, so it would find "created " and never the key after it.
 */
function line(text: string) {
  return (_content: string, el: Element | null) =>
    el?.tagName === "P" && (el.textContent ?? "").includes(text);
}

function record(seq: number, overrides: Partial<AuditRecord> = {}): AuditRecord {
  return {
    seq,
    at: today(9, 0),
    kind: "operation.proposed",
    actor: "svc:chatgpt:work",
    operation_id: "op_1",
    plugin: "cnmaestro",
    action: "device.reboot",
    ...overrides,
  };
}

const USERS = {
  users: [{ id: "u_7", email: "sam@example.com", name: "Sam Vimes" }],
  count: 1,
};
const KEYS = { keys: [{ id: "key_993f", name: "ledger-refusal-test" }], count: 1 };

function mount(
  records: AuditRecord[],
  chain: { intact: boolean; broken_at: number } = { intact: true, broken_at: 0 },
  role: "user" | "admin" = "admin",
) {
  vi.spyOn(api, "audit").mockResolvedValue({ records, count: records.length });
  const verify = vi.spyOn(api, "verifyAudit").mockResolvedValue(chain);
  // Both take a permission the reader may not hold, so both are mocked loosely
  // and the page has to cope with either answer.
  vi.spyOn(api, "users").mockResolvedValue(USERS as never);
  vi.spyOn(api, "keys").mockResolvedValue(KEYS as never);
  const view = renderWith(<Audit />, { session: sessionFor(role) });
  return { ...view, verify };
}

/**
 * A day with everything on it: a change threaded from proposal to confirmed
 * outcome, the approval window that cleared it, a key issued and revoked, and
 * mcpd's own tidying.
 */
const BUSY_DAY: AuditRecord[] = [
  record(12, {
    kind: "audit.pruned", actor: "system:retention", at: today(7, 32),
    operation_id: undefined, plugin: undefined, action: undefined,
    detail: { removed_entries: 16, older_than: "2026-07-29T12:00:00Z" },
  }),
  record(11, {
    kind: "operation.succeeded", actor: "system:executor", at: today(6, 25),
    plugin: "echo", action: "label.set", from_state: "executing", to_state: "succeeded",
    detail: { verified: true, upstream_ref: "", error_code: "" },
  }),
  record(10, {
    kind: "operation.executing", actor: "system:executor", at: today(6, 25),
    plugin: "echo", action: "label.set", from_state: "approved", to_state: "executing",
    detail: { instance: "mcpd-1", attempt: "a_1", drift: "none", verifiable: true },
  }),
  record(9, {
    kind: "operation.approved", actor: "system:policy", at: today(6, 25),
    plugin: "echo", action: "label.set", from_state: "pending_approval", to_state: "approved",
    detail: {
      channel: "policy", authority: "bypass:byp_2", reason: "inside an open window",
      proposed_by: "user:sam@example.com", asked_a_person: false,
    },
  }),
  record(8, {
    kind: "operation.proposed", actor: "user:sam@example.com", at: today(6, 25),
    plugin: "echo", action: "label.set", to_state: "pending_approval", risk: "medium",
    detail: {
      impact: "Changes the label reported upstream",
      changes: [{ field: "label", from: "multi-window", to: "narrower-window" }],
      assurance: "reviewed_change", verifiable: true, drift_checked: true, reversible: true,
    },
  }),
  record(7, {
    kind: "approval.bypass.opened", actor: "user:sam@example.com", at: today(6, 10),
    operation_id: undefined, plugin: "byp_2", action: undefined,
    detail: {
      minutes: 30, plugin: "echo", ceiling: "low", reason: "narrower, right plugin",
      expires_at: today(6, 40),
    },
  }),
  record(6, {
    kind: "apikey.revoked", actor: "user:sam@example.com", at: yesterday(16, 4),
    operation_id: undefined, plugin: "key_993f", action: undefined,
    detail: { name: "ledger-refusal-test" },
  }),
  record(5, {
    kind: "apikey.created", actor: "user:sam@example.com", at: yesterday(15, 50),
    operation_id: undefined, plugin: "key_993f", action: undefined,
    detail: {
      name: "ledger-refusal-test", role: "role_operator", groups: [],
      grants: [{ plugin: "cnmaestro", level: "write" }], expires_at: null,
    },
  }),
];

describe("the audit trail", () => {
  it("groups a busy day under its own heading, newest first", async () => {
    mount(BUSY_DAY);
    expect(await screen.findByText("Today")).toBeInTheDocument();
    expect(screen.getByText("Yesterday")).toBeInTheDocument();

    const headings = screen.getAllByRole("heading", { level: 2 });
    expect(headings.map((h) => h.textContent)).toEqual([
      expect.stringContaining("Today"),
      expect.stringContaining("Yesterday"),
    ]);
  });

  /**
   * The change is five rows in the table and one thing that happened. Reading
   * it as five unrelated lines is what made the page hard to follow.
   */
  it("draws a change as one story with its steps under it", async () => {
    mount(BUSY_DAY);
    await screen.findByText("Today");

    // The proposal heads it, and names the person who asked.
    const head = screen.getByText(line("asked to set the label on echo"));
    expect(head).toHaveTextContent("Sam Vimes asked to set the label on echo");

    // Every later entry is a step, and none of them is a row of its own.
    expect(screen.getByText(/approved by an open approval window/)).toBeInTheDocument();
    expect(screen.getByText(/^being applied by mcpd/)).toBeInTheDocument();
    expect(screen.getByText(/^applied by mcpd/)).toBeInTheDocument();
    expect(screen.queryByText(/applied the change to set the label/)).not.toBeInTheDocument();

    // The exact fields, which are what make it a reviewed change.
    expect(screen.getByText("label")).toBeInTheDocument();
    expect(screen.getByText("multi-window")).toBeInTheDocument();
    expect(screen.getByText("narrower-window")).toBeInTheDocument();
  });

  /**
   * A standing rule can approve without anybody being asked, so the trail's
   * authority is a separate fact from whoever proposed the change.
   */
  it("names what cleared a change rather than the host that ran it", async () => {
    mount(BUSY_DAY);
    await screen.findByText("Today");
    expect(screen.getByText(/approved by an open approval window/)).toBeInTheDocument();
    expect(screen.queryByText(/approved by mcpd/)).not.toBeInTheDocument();
  });

  // Null is "nobody checked" and true is "checked, and it matched".
  it("says the outcome was confirmed against the system", async () => {
    mount(BUSY_DAY);
    await screen.findByText("Today");
    expect(screen.getByText(/checked against echo: the change is in place/))
      .toBeInTheDocument();
  });

  it("reads an approval window, a key and mcpd's own tidying as sentences", async () => {
    mount(BUSY_DAY);
    await screen.findByText("Today");

    expect(screen.getByText(/opened a 30-minute approval window/)).toBeInTheDocument();
    expect(screen.getByText(/echo only/)).toBeInTheDocument();
    expect(screen.getByText(/up to low risk/)).toBeInTheDocument();

    expect(screen.getByText(line("created the key ledger-refusal-test"))).toBeInTheDocument();
    expect(screen.getByText(line("revoked the key ledger-refusal-test"))).toBeInTheDocument();

    expect(screen.getByText(/removed 16 entries older than/)).toBeInTheDocument();
  });

  /**
   * The key's identifier was what the old page showed where a name belongs,
   * and it reads like the credential itself. Nothing in a sentence is an
   * identifier; they live in the raw entry, closed until somebody wants them.
   */
  it("keeps identifiers out of the sentences and in the raw entry", async () => {
    mount(BUSY_DAY);
    await screen.findByText("Today");

    const created = screen.getByText(line("created the key ledger-refusal-test"));
    expect(created.textContent).not.toMatch(/key_993f/);

    const raws = screen.getAllByText("Raw entry");
    for (const summary of raws) {
      expect(summary.closest("details")).not.toHaveAttribute("open");
    }

    const entry = created.closest("li")!;
    await userEvent.click(within(entry).getByText("Raw entry"));
    expect(within(entry).getByText(/key_993f/)).toBeInTheDocument();
  });

  it("has something to say when nothing has happened", async () => {
    mount([]);
    expect(await screen.findByText("Nothing recorded yet")).toBeInTheDocument();
  });

  /**
   * A check that announced success loudly on every visit would train somebody
   * to skip the one time it did not.
   */
  it("says the chain holds once, quietly, and shouts only when it does not", async () => {
    mount(BUSY_DAY);
    expect(await screen.findByText(/Every entry follows the one before it/))
      .toBeInTheDocument();
    expect(screen.queryByText(/changed the record directly/)).not.toBeInTheDocument();
  });

  /**
   * Tamper-evidence that is only a sentence at the top of the page is a claim.
   * The break is drawn where it happened, so which entries are still evidence
   * is something an operator can read off the page.
   */
  it("shows the break where it happened, as well as saying so", async () => {
    mount(BUSY_DAY, { intact: false, broken_at: 7 });
    expect(await screen.findByText(/changed the record directly/)).toBeInTheDocument();
    expect(screen.getByText(/does not follow the one below it/)).toBeInTheDocument();
    expect(screen.queryByText(/Every entry follows the one before it/))
      .not.toBeInTheDocument();
  });

  /**
   * Verifying is gated on history:read, same as reading the trail. Asking
   * anyway and swallowing the refusal would put a 403 in the network log for a
   * question the principal was never allowed to ask.
   */
  it("does not ask for a verification a principal without history:read may not run", async () => {
    vi.spyOn(api, "audit").mockResolvedValue({ records: [record(1)], count: 1 });
    const verify = vi.spyOn(api, "verifyAudit").mockResolvedValue({ intact: true, broken_at: 0 });
    vi.spyOn(api, "users").mockResolvedValue(USERS as never);
    vi.spyOn(api, "keys").mockResolvedValue(KEYS as never);
    renderWith(<Audit />, {
      session: sessionFor("user", { permissions: ["settings:read"] }),
    });

    await screen.findAllByText(line("asked to"));
    await waitFor(() => expect(verify).not.toHaveBeenCalled());
  });

  /**
   * Naming an account or a key takes a permission the reader may not hold. A
   * sentence without them is a worse sentence and still a sentence.
   */
  it("still says something when it may not look up who a key belongs to", async () => {
    vi.spyOn(api, "audit").mockResolvedValue({
      records: [BUSY_DAY[7]!], count: 1,
    });
    vi.spyOn(api, "verifyAudit").mockResolvedValue({ intact: true, broken_at: 0 });
    const users = vi.spyOn(api, "users").mockRejectedValue(new Error("nope"));
    renderWith(<Audit />, {
      session: sessionFor("user", { permissions: ["history:read"] }),
    });

    expect(await screen.findByText(line("created the key ledger-refusal-test")))
      .toBeInTheDocument();
    // The actor's own name is not available either, so it falls back to the
    // local part rather than to a raw principal.
    expect(screen.getByText(line("sam created the key ledger-refusal-test")))
      .toBeInTheDocument();
    await waitFor(() => expect(users).not.toHaveBeenCalled());
  });

  /**
   * Append-only means the numbers run consecutively. A hole between two
   * entries on screen is rows that are no longer in the table, which is worth
   * saying even when the surviving chain still verifies.
   */
  it("points out entries that are no longer in the table", async () => {
    mount([
      record(9, { operation_id: undefined, kind: "apikey.revoked", plugin: "key_993f", detail: { name: "ledger-refusal-test" } }),
      record(4, { operation_id: undefined, kind: "apikey.created", plugin: "key_993f", detail: { name: "ledger-refusal-test" } }),
    ]);
    expect(await screen.findByText(/4 entries are no longer in the table/))
      .toBeInTheDocument();
  });

  /**
   * A filter narrows what is on screen and never what was asked for, and it
   * lives in the address so a link can arrive with it set.
   */
  it("narrows by who, by what and by words, and says so in the address", async () => {
    mount(BUSY_DAY);
    await screen.findByText("Today");

    await userEvent.selectOptions(screen.getByLabelText("What happened"), "access");
    expect(await screen.findByText(/2 of the last 8 entries match/)).toBeInTheDocument();
    expect(new URLSearchParams(window.location.search).get("what")).toBe("access");
    expect(screen.getByText(line("created the key ledger-refusal-test"))).toBeInTheDocument();
    expect(screen.queryByText(line("asked to set the label"))).not.toBeInTheDocument();

    await userEvent.selectOptions(screen.getByLabelText("What happened"), "");
    await userEvent.type(screen.getByLabelText("Find in these entries"), "approval window");
    expect(await screen.findByText(/opened a 30-minute approval window/)).toBeInTheDocument();
    expect(new URLSearchParams(window.location.search).get("q")).toBe("approval window");
  });

  /**
   * mcpd applied the change and a rule approved it, so filtering by the person
   * who proposed it must keep the whole story rather than leaving a proposal
   * that appears to have come to nothing.
   */
  it("keeps a change whole when the filter matches any one of its steps", async () => {
    mount(BUSY_DAY);
    await screen.findByText("Today");

    await userEvent.selectOptions(screen.getByLabelText("Who"), "Sam Vimes");
    expect(await screen.findByText(line("asked to set the label on echo"))).toBeInTheDocument();
    expect(screen.getByText(/^applied by mcpd/)).toBeInTheDocument();
    expect(screen.getByText(/approved by an open approval window/)).toBeInTheDocument();
    // mcpd's own tidying is not Sam's doing, and goes.
    expect(screen.queryByText(/removed 16 entries older than/)).not.toBeInTheDocument();
  });

  it("says what to do when a filter matches nothing", async () => {
    mount(BUSY_DAY);
    await screen.findByText("Today");
    await userEvent.type(screen.getByLabelText("Find in these entries"), "nothing at all");
    expect(await screen.findByText("Nothing matches")).toBeInTheDocument();
  });
});
