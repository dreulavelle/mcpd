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

/**
 * The endpoint as it actually behaves: one state at most, and a limit the
 * server applies per plugin rather than across the answer.
 */
function mount(operations: Operation[], path = "/approvals") {
  vi.spyOn(api, "operations").mockImplementation(async (state) => {
    const matching = state
      ? operations.filter((op) => op.state === state)
      : operations;
    return { operations: matching, count: matching.length };
  });
  // Only keys need resolving client-side; users arrive resolved on the record.
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

  /**
   * The server's limit is per plugin, not across the answer.
   *
   * A system that settles two hundred changes between polls fills its own
   * slice of the unfiltered page, and a proposal still waiting on somebody
   * falls off it. The queue is the half of this screen that cannot be allowed
   * to lie, so it is fetched as its own call and merged by id.
   */
  it("finds a waiting proposal the unfiltered page has no room for", async () => {
    const waiting = operation({
      id: "op-buried", plugin: "graylog", action: "alert.silence",
      state: "pending_approval", terminal: false, verified: undefined,
      terminal_at: undefined, requested_at: at(-90_000), expires_at: at(600),
    });
    const flood = Array.from({ length: 200 }, (_, i) => operation({
      id: `op-noise-${i}`, requested_at: at(-i),
    }));

    vi.spyOn(api, "operations").mockImplementation(async (state) =>
      state === "pending_approval"
        ? { operations: [waiting], count: 1 }
        // What the unfiltered call returns once one plugin has used up the
        // page: everything settled, and nothing that is waiting.
        : { operations: flood, count: flood.length });
    vi.spyOn(api, "keys").mockResolvedValue({ keys: [], count: 0 });
    renderWith(<ApprovalsList />, { session: sessionFor("admin") });

    const region = await screen.findByRole("region", { name: /waiting on you/i });
    expect(within(region).getByRole("heading", { level: 3 }).textContent)
      .toBe("Silence the alert on graylog");
  });

  it("counts a change that came back from both calls once", async () => {
    mount(aBusyHost());
    const region = await screen.findByRole("region", { name: /waiting on you/i });
    expect(within(region).getAllByRole("heading", { level: 3 })).toHaveLength(2);
  });

  /**
   * The two calls are concurrent and either can be the older answer, but a
   * state only moves forward out of pending. So a change the unfiltered call
   * says is applied has been applied, and the pending copy of it is stale --
   * letting it overwrite would put a settled change back in the queue and
   * offer somebody a decision on something already done.
   */
  it("does not put a change back in the queue on a stale pending answer", async () => {
    const id = "op-raced";
    const settled = operation({ id, plugin: "echo", action: "label.set", state: "succeeded" });
    const stale = operation({
      id, plugin: "echo", action: "label.set", state: "pending_approval",
      terminal: false, verified: undefined, terminal_at: undefined,
    });

    vi.spyOn(api, "operations").mockImplementation(async (state) =>
      state === "pending_approval"
        ? { operations: [stale], count: 1 }
        : { operations: [settled], count: 1 });
    vi.spyOn(api, "keys").mockResolvedValue({ keys: [], count: 0 });
    renderWith(<ApprovalsList />, { session: sessionFor("admin") });

    expect(await screen.findByText("Nothing is waiting on you.")).toBeInTheDocument();
    const lately = screen.getByRole("region", { name: "Lately" });
    expect(within(lately).getAllByRole("listitem")).toHaveLength(1);
    expect(within(lately).getByRole("listitem").textContent).toContain("applied");
  });

  // Past the deadline, "Runs out 5 minutes ago" is arithmetic rather than the
  // fact, and the fact is that it is too late.
  it("says a lapsed proposal is out of time rather than counting backwards", async () => {
    mount([operation({
      id: "op-late", plugin: "echo", action: "label.set",
      state: "pending_approval", terminal: false, verified: undefined,
      terminal_at: undefined, expires_at: at(-300),
    })]);

    const region = await screen.findByRole("region", { name: /waiting on you/i });
    expect(within(region).getByText("Out of time")).toBeInTheDocument();
    expect(within(region).queryByText(/Runs out/)).not.toBeInTheDocument();
  });

  // "Nothing to decide" is a claim about the host. With two changes waiting
  // and a filter hiding them it is a false one, and the reader is one click
  // from seeing that it is.
  it("does not say there is nothing to decide when a filter is what emptied the page", async () => {
    mount(aBusyHost());
    await screen.findByRole("region", { name: /waiting on you/i });

    await userEvent.type(screen.getByLabelText("Find a change"), "zzzz");
    expect(await screen.findByText("Nothing here")).toBeInTheDocument();
    expect(screen.queryByText("Nothing to decide")).not.toBeInTheDocument();
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

  /**
   * Rejecting never writes `approved_by`, so falling back to the requester
   * had the row read "ChatGPT (work) · turned down an hour ago" -- an
   * assistant turning its own change down, which is not a thing that happens.
   */
  it("does not present the requester as the person who settled it", async () => {
    mount([operation({
      id: "op-r", plugin: "threecx", action: "password.reset",
      state: "rejected", verified: undefined, changes: undefined,
      approved_by: undefined, requested_by: "svc:chatgpt:work",
    })]);

    const row = (await screen.findByText(/Reset the password on threecx/))
      .closest("li")!;
    // The who-fragment says what it knows -- who asked -- and the outcome
    // beside it keeps no subject at all.
    expect(row.textContent).toMatch(/proposed by ChatGPT \(work\) · turned down/);
  });

  it("names the person where the record does say who decided", async () => {
    // Succeeded, because a rejected change with an approver on it is a record
    // the server never writes: rejecting leaves `approved_by` empty.
    mount([operation({
      id: "op-a", plugin: "threecx", action: "password.reset",
      state: "succeeded", verified: true, changes: undefined,
      approved_by: "user:dreu@example.com", approved_by_name: "Dreu Lavelle",
    })]);

    const row = (await screen.findByText(/Reset the password on threecx/))
      .closest("li")!;
    // Resolved server-side for every session, so no request is made for it.
    expect(row.textContent).toContain("Dreu Lavelle · applied");
    expect(row.textContent).not.toContain("proposed by");
  });

  /**
   * Approved and then withdrawn is a legal path, and the two acts are not the
   * same person's.
   *
   * `approved_by` records the approval. Printing it beside "withdrawn" put
   * alice's name on an act that was somebody else's, so on the states where
   * the field is only ever the approver -- rejected, cancelled, expired --
   * the name keeps its own verb and the outcome stays subjectless.
   */
  it("does not put the approver's name on a withdrawal", async () => {
    mount([operation({
      id: "op-c", plugin: "echo", action: "label.set",
      state: "cancelled", verified: undefined, changes: undefined,
      approved_by: "user:alice@example.com", approved_by_name: "Alice Doe",
      approved_at: at(-3300),
    })]);

    const row = (await screen.findByText(/Set the label on echo/)).closest("li")!;
    // The fragment line itself, so "approved by" has to be the first thing on
    // it rather than merely somewhere in the row.
    expect(row.querySelectorAll("p")[1]!.textContent)
      .toMatch(/^approved by Alice Doe · withdrawn/);
  });

  it("says the same of a change a rule approved and a clock then ran out on", async () => {
    mount([operation({
      id: "op-e", plugin: "echo", action: "label.set",
      state: "expired", verified: undefined, changes: undefined,
      approved_by: "system:policy", authorized_by_rule: "routine-labels",
    })]);

    const row = (await screen.findByText(/Set the label on echo/)).closest("li")!;
    expect(row.querySelectorAll("p")[1]!.textContent)
      .toMatch(/^approved by a standing rule · ran out of time/);
  });

  // A delete records no new value. Without the plugin's own sentence, two
  // page deletions on one system were the same row twice.
  it("falls back to the impact where a change records no new value", async () => {
    mount([
      operation({
        id: "op-d1", plugin: "bookstack", action: "page.delete",
        changes: undefined, impact: "Deletes the page “Runbook: failover”.",
      }),
      operation({
        id: "op-d2", plugin: "bookstack", action: "page.delete",
        changes: undefined, impact: "Deletes the page “Onboarding”.",
      }),
    ]);

    const lately = await screen.findByRole("region", { name: "Lately" });
    const rows = within(lately).getAllByRole("listitem").map((li) => li.textContent);
    expect(rows[0]).toContain("Runbook: failover");
    expect(rows[1]).toContain("Onboarding");
    expect(rows[0]).not.toBe(rows[1]);
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

  // The address bar is somewhere anybody can type, and a build that trusted
  // it indexed GROUPS with a key nobody defined and rendered a blank page.
  it("treats a group it has never heard of as no filter at all", async () => {
    mount(aBusyHost(), "/approvals?show=bogus");

    const everything = await screen.findByRole("radio", { name: "Everything (8)" });
    expect(everything).toHaveAttribute("aria-checked", "true");
    expect(within(screen.getByRole("region", { name: "Lately" }))
      .getAllByRole("listitem")).toHaveLength(8);
  });

  it("ignores a state it has never heard of the same way", async () => {
    mount(aBusyHost(), "/approvals?state=nonsense");
    expect(await screen.findByRole("radio", { name: "Everything (8)" }))
      .toHaveAttribute("aria-checked", "true");
  });

  /**
   * Choosing Everything has to actually mean everything.
   *
   * The chip writes `show` and the legacy link carries `state`. Clearing only
   * `show` left `state=indeterminate` behind, `groupForState` read it again,
   * and the view snapped back to Unknown -- a control that undoes itself.
   */
  it("drops the legacy state when a chip clears the new one", async () => {
    mount(aBusyHost(), "/approvals?state=indeterminate");
    await screen.findByRole("region", { name: "Lately" });

    await userEvent.click(screen.getByRole("radio", { name: "Everything (8)" }));
    expect(window.location.search).toBe("");
    expect(screen.getByRole("radio", { name: "Everything (8)" }))
      .toHaveAttribute("aria-checked", "true");
    expect(within(screen.getByRole("region", { name: "Lately" }))
      .getAllByRole("listitem")).toHaveLength(8);
  });

  it("drops the legacy plugin when the system select clears the new one", async () => {
    mount(aBusyHost(), "/approvals?plugin=graylog");
    await screen.findByRole("region", { name: "Lately" });

    await userEvent.selectOptions(screen.getByLabelText("System"), "");
    expect(window.location.search).toBe("");
    expect(screen.getByRole("heading", { name: "Set the label on echo" }))
      .toBeInTheDocument();
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
