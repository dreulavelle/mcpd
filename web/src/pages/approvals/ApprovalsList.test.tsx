import { describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, type Operation } from "@/lib/api";
import { renderWith, sessionFor } from "@/test/render";
import { ApprovalsList } from "./ApprovalsList";

const NOW = Date.parse("2026-08-22T12:00:00Z");
const at = (offsetSeconds: number) =>
  new Date(NOW + offsetSeconds * 1000).toISOString();

function operation(overrides: Partial<Operation> = {}): Operation {
  return {
    id: "op-1",
    plugin: "cnmaestro",
    action: "radio.channel.set",
    state: "succeeded",
    risk: "low",
    impact: "Moves one radio to another channel.",
    changes: [{ field: "channel", from: 36, to: 44 }],
    requested_by: "svc:chatgpt:work",
    requested_at: at(-3600),
    expires_at: at(-1800),
    terminal_at: at(-3000),
    attempts: 1,
    terminal: true,
    verified: true,
    assurance: "reviewed_change",
    drift_checked: true,
    outcome_verifiable: true,
    ...overrides,
  };
}

function mount(operations: Operation[], path = "/approvals") {
  vi.spyOn(api, "operations").mockResolvedValue({
    operations, count: operations.length,
  });
  // The page names people rather than principals where it may read the
  // accounts, which an administrator's session can.
  vi.spyOn(api, "users").mockResolvedValue({ users: [], count: 0 });
  vi.spyOn(api, "keys").mockResolvedValue({ keys: [], count: 0 });
  return renderWith(<ApprovalsList />, { session: sessionFor("admin"), path });
}

/**
 * A host in the state the page exists for: two changes waiting on somebody,
 * and a tail of settled ones in every outcome the machine produces.
 */
function aBusyHost(): Operation[] {
  return [
    // Waiting, and the later of the two deadlines.
    operation({
      id: "op-w2", plugin: "graylog", action: "alert.silence",
      state: "pending_approval", terminal: false, verified: undefined,
      risk: "medium", terminal_at: undefined,
      changes: [{ field: "silenced", from: false, to: true }],
      requested_at: at(-300), expires_at: at(7200),
    }),
    // Waiting, and the one that runs out first.
    operation({
      id: "op-w1", plugin: "echo", action: "label.set",
      state: "pending_approval", terminal: false, verified: undefined,
      risk: "low", terminal_at: undefined,
      changes: [{ field: "window", from: "multi-window", to: "narrower-window" }],
      requested_at: at(-600), expires_at: at(1800),
    }),

    operation({ id: "op-1", state: "succeeded", verified: true }),
    operation({
      id: "op-2", plugin: "graylog", action: "stream.pause",
      state: "succeeded", verified: null,
      approved_by: "system:policy", authorized_by_rule: "routine-streams",
      changes: undefined,
    }),
    operation({
      id: "op-3", plugin: "threecx", action: "password.reset",
      state: "rejected", verified: undefined,
      approved_by: "user:dreu@example.com", changes: undefined,
    }),
    operation({ id: "op-4", plugin: "cnmaestro", action: "device.update", state: "indeterminate", verified: null }),
    operation({ id: "op-5", plugin: "cnmaestro", action: "device.reboot", state: "failed", verified: false }),
    operation({ id: "op-6", plugin: "echo", action: "label.set", state: "expired", verified: undefined }),
    operation({ id: "op-7", plugin: "echo", action: "label.set", state: "cancelled", verified: undefined }),
    operation({ id: "op-8", plugin: "netbox", action: "site.create", state: "succeeded", verified: true }),
  ];
}

/**
 * The queue, which is the reason somebody opens this page.
 *
 * It is ordered by the deadline they are acting against rather than by when
 * the assistant happened to ask: a proposal expires, and the one expiring
 * soonest is the one a delay actually costs something on.
 */
describe("what is waiting", () => {
  it("leads with the waiting changes, soonest to run out first", async () => {
    mount(aBusyHost());

    const waiting = await screen.findByRole("region", { name: /waiting on you/i });
    expect(within(waiting).getByRole("heading", { name: /Waiting on you \(2\)/ }))
      .toBeInTheDocument();

    const headings = within(waiting).getAllByRole("heading", { level: 3 })
      .map((h) => h.textContent);
    expect(headings).toEqual([
      "Set the label on echo",
      "Silence the alert on graylog",
    ]);
  });

  it("puts the change itself in the card, values and all", async () => {
    mount(aBusyHost());

    const waiting = await screen.findByRole("region", { name: /waiting on you/i });
    const card = within(waiting).getByRole("link", { name: /Set the label on echo/ });
    expect(card).toHaveAttribute("href", "/approvals/op-w1");
    expect(card.textContent).toContain("from");
    expect(card.textContent).toContain("multi-window");
    expect(card.textContent).toContain("narrower-window");
    // Who asked, what it risks and what the record will prove, as words.
    expect(card.textContent).toContain("ChatGPT (work)");
    expect(card.textContent).toContain("low risk");
    expect(card.textContent).toContain("a reviewed change");
    expect(card.textContent).toContain("Read and decide");
  });

  // The machine's name for the change is evidence, and evidence is not prose.
  it("never puts the raw action or a principal on the page", async () => {
    mount(aBusyHost());
    await screen.findByRole("region", { name: /waiting on you/i });

    expect(screen.queryByText(/label\.set/)).not.toBeInTheDocument();
    expect(screen.queryByText(/radio\.channel\.set/)).not.toBeInTheDocument();
    expect(screen.queryByText(/svc:chatgpt/)).not.toBeInTheDocument();
    expect(screen.queryByText(/system:policy/)).not.toBeInTheDocument();
  });

  it("says so plainly when nothing is waiting, and still shows the rest", async () => {
    mount(aBusyHost().filter((op) => op.state !== "pending_approval"));

    expect(await screen.findByText("Nothing is waiting on you.")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Lately" })).toBeInTheDocument();
  });

  it("says a fresh host has had nothing proposed rather than showing an empty queue", async () => {
    mount([]);
    expect(await screen.findByText(/No assistant has proposed anything yet/i))
      .toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Lately" })).not.toBeInTheDocument();
  });
});

/** The tail: what became of everything else, in one line each. */
describe("what became of the rest", () => {
  it("names who settled each one and what happened, without a table", async () => {
    mount(aBusyHost());

    const lately = await screen.findByRole("region", { name: "Lately" });
    expect(within(lately).queryByRole("table")).not.toBeInTheDocument();

    const rows = within(lately).getAllByRole("listitem").map((li) => li.textContent);
    expect(rows[0]).toContain("Set the radio channel on cnmaestro");
    expect(rows[0]).toContain("from 36 to 44");
    expect(rows[0]).toContain("confirmed");
  });

  // A standing rule approving a change is not somebody clicking Approve. The
  // record says `approved_by: "system:policy"`, which is not an account.
  it("says a standing rule settled the one a rule authorised", async () => {
    mount(aBusyHost());

    const lately = await screen.findByRole("region", { name: "Lately" });
    const row = within(lately).getByText(/Pause the stream on graylog/)
      .closest("li")!;
    expect(row.textContent).toContain("a standing rule");
    expect(row.textContent).toContain("applied");
    // Nobody read the system again. That is a third answer, not a tick.
    expect(row.textContent).toContain("not checked");
  });

  // Indeterminate is not failed. Reading it as settled invites a retry that
  // applies the change twice.
  it("keeps an unknown outcome apart from one that did not run", async () => {
    mount(aBusyHost());

    const lately = await screen.findByRole("region", { name: "Lately" });
    const unknown = within(lately).getByText(/Change the device on cnmaestro/)
      .closest("li")!;
    expect(unknown.textContent).toContain("ended in an unknown state");
    expect(unknown.textContent).not.toMatch(/didn't run/);

    const failed = within(lately).getByText(/Restart the device on cnmaestro/)
      .closest("li")!;
    expect(failed.textContent).toContain("didn't run");
    expect(failed.textContent).toContain("did not match");
  });

  it("shows ten and offers the rest rather than every change this host has made", async () => {
    const many = [
      ...aBusyHost(),
      ...Array.from({ length: 6 }, (_, i) => operation({
        id: `op-extra-${i}`, requested_at: at(-7200 - i),
      })),
    ];
    mount(many);

    const lately = await screen.findByRole("region", { name: "Lately" });
    expect(within(lately).getAllByRole("listitem")).toHaveLength(10);

    await userEvent.click(screen.getByRole("button", { name: /Show more \(4 left\)/ }));
    expect(within(lately).getAllByRole("listitem")).toHaveLength(14);
  });
});

/** The chips, and the older links that have to keep landing in the right place. */
describe("cutting the list", () => {
  it("counts each outcome and narrows to it", async () => {
    mount(aBusyHost());

    const applied = await screen.findByRole("radio", { name: "Applied (3)" });
    await userEvent.click(applied);

    const lately = screen.getByRole("region", { name: "Lately" });
    expect(within(lately).getAllByRole("listitem")).toHaveLength(3);
    expect(window.location.search).toBe("?show=applied");
  });

  it("groups turned down, withdrawn and expired as one answer", async () => {
    mount(aBusyHost());
    await userEvent.click(await screen.findByRole("radio", { name: "Turned down (3)" }));

    const rows = within(screen.getByRole("region", { name: "Lately" }))
      .getAllByRole("listitem").map((li) => li.textContent);
    expect(rows).toHaveLength(3);
    expect(rows.join(" ")).toMatch(/turned down/);
    expect(rows.join(" ")).toMatch(/withdrawn/);
    expect(rows.join(" ")).toMatch(/ran out of time/);
  });

  // Attention links here with the old parameter, and so does anything anybody
  // bookmarked. A link that lands on the wrong list is worse than no link.
  it("still answers ?state=indeterminate, as the Unknown chip", async () => {
    mount(aBusyHost(), "/approvals?state=indeterminate");

    const unknown = await screen.findByRole("radio", { name: "Unknown (1)" });
    expect(unknown).toHaveAttribute("aria-checked", "true");
    const rows = within(screen.getByRole("region", { name: "Lately" }))
      .getAllByRole("listitem");
    expect(rows).toHaveLength(1);
    expect(rows[0]!.textContent).toContain("ended in an unknown state");
  });

  // A host with several systems proposing changes needs the list cut by one of
  // them, and the cut lives in the address so a system's page can link to it.
  it("narrows to one system, in the address", async () => {
    mount(aBusyHost());
    await screen.findByRole("region", { name: "Lately" });

    await userEvent.selectOptions(screen.getByLabelText("System"), "graylog");
    expect(window.location.search).toBe("?system=graylog");
    expect(screen.getByRole("heading", { name: "Silence the alert on graylog" }))
      .toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Set the label on echo" }))
      .not.toBeInTheDocument();
  });

  it("searches the sentence a person can actually see", async () => {
    mount(aBusyHost());
    await screen.findByRole("region", { name: "Lately" });

    await userEvent.type(screen.getByLabelText("Find a change"), "password");
    const lately = screen.getByRole("region", { name: "Lately" });
    const rows = within(lately).getAllByRole("listitem");
    expect(rows).toHaveLength(1);
    expect(rows[0]!.textContent).toContain("Reset the password on threecx");
    expect(screen.getByText("Nothing matching that is waiting on you."))
      .toBeInTheDocument();
  });
});
