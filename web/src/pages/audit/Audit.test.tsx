import { describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, type AuditRecord } from "@/lib/api";
import { renderWith, sessionFor } from "@/test/render";
import { Audit } from "./Audit";

function record(seq: number, overrides: Partial<AuditRecord> = {}): AuditRecord {
  return {
    seq,
    at: "2026-08-22T09:00:00Z",
    kind: "operation.proposed",
    actor: "svc:assistant",
    operation_id: "op-1",
    plugin: "cnmaestro",
    action: "device.reboot",
    ...overrides,
  };
}

function mount(records: AuditRecord[], chain: { intact: boolean; broken_at: number }, role: "user" | "admin" = "admin") {
  vi.spyOn(api, "audit").mockResolvedValue({ records, count: records.length });
  const verify = vi.spyOn(api, "verifyAudit").mockResolvedValue(chain);
  const view = renderWith(<Audit />, { session: sessionFor(role) });
  return { ...view, verify };
}

const INTACT = { intact: true, broken_at: 0 };

describe("the audit trail", () => {
  it("lists what happened, newest first", async () => {
    mount([record(3), record(2), record(1)], INTACT);
    expect(await screen.findAllByText(/Suggested: device reboot/)).toHaveLength(3);
  });

  it("has something to say when nothing has happened", async () => {
    mount([], INTACT);
    expect(await screen.findByText("Nothing recorded yet")).toBeInTheDocument();
  });

  /**
   * A check that announced success on every visit would train somebody to skip
   * the one time it did not.
   */
  it("says nothing at all while the chain verifies", async () => {
    mount([record(2), record(1)], INTACT);
    await screen.findAllByText(/Suggested/);
    expect(screen.queryByText(/has been altered/)).not.toBeInTheDocument();
    expect(screen.queryByText(/chain breaks here/)).not.toBeInTheDocument();
  });

  /**
   * Tamper-evidence that is only a sentence at the top of the page is a claim.
   * The break is drawn where it happened, so which entries are still evidence
   * is something an operator can read off the page.
   */
  it("shows the break where it happened, as well as saying so", async () => {
    mount([record(3), record(2), record(1)], { intact: false, broken_at: 2 });

    expect(await screen.findByText(/has been altered/)).toBeInTheDocument();
    expect(screen.getByText(/does not follow the one below it/))
      .toBeInTheDocument();
  });

  /**
   * Only an administrator may run the check. Asking anyway and swallowing the
   * refusal would put a 403 in everyone else's network log for a question they
   * were never allowed to ask.
   */
  it("does not ask for a verification a plain user may not run", async () => {
    const { verify } = mount([record(1)], INTACT, "user");
    await screen.findAllByText(/Suggested/);
    await waitFor(() => expect(verify).not.toHaveBeenCalled());
  });

  /**
   * Append-only means the numbers run consecutively. A hole between two
   * entries on screen is rows that are no longer in the table, which is worth
   * saying even when the surviving chain still verifies.
   */
  it("points out entries that are missing between two on screen", async () => {
    mount([record(9), record(4)], INTACT);
    expect(await screen.findByText(/4 entries are not in this table/))
      .toBeInTheDocument();
  });

  // A filter narrows what is on screen and never what was asked for: the
  // endpoint takes a count and nothing else, and a filter that quietly
  // shrank the window would hide the entries somebody was looking for.
  it("narrows the loaded entries by kind, actor and words, and says how many match", async () => {
    mount([
      record(3, { kind: "operation.approved", actor: "user:sam@example.com" }),
      record(2),
      record(1, { plugin: "graylog", action: "stream.pause" }),
    ], INTACT);
    await screen.findAllByText(/Suggested/);

    await userEvent.selectOptions(screen.getByLabelText("Kind of entry"), "operation.approved");
    expect(await screen.findByText(/1 of the last 3 entries match/)).toBeInTheDocument();
    expect(screen.getByText(/Approved: device reboot/)).toBeInTheDocument();
    expect(screen.queryByText(/Suggested: device reboot/)).not.toBeInTheDocument();

    await userEvent.selectOptions(screen.getByLabelText("Kind of entry"), "");
    await userEvent.type(screen.getByLabelText("Find in these entries"), "stream pause");
    expect(await screen.findByText(/Suggested: stream pause/)).toBeInTheDocument();
    expect(screen.queryByText(/device reboot/)).not.toBeInTheDocument();
  });
});
