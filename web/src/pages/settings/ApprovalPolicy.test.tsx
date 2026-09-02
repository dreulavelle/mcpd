import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, ApiError, type ApprovalPolicy as Policy } from "@/lib/api";
import { renderWith, sessionFor } from "@/test/render";
import { ApprovalPolicy, warningsByRule } from "./ApprovalPolicy";

const ALWAYS_ASK = {
  id: "never-reboot", plugin: "*", action: "device.reboot",
  principal: "*", max_risk: "",
  note: "a reboot drops every client on the radio",
};

const ALLOW = {
  id: "routine-radio", plugin: "cnmaestro", action: "*",
  principal: "*", max_risk: "low",
  note: "a channel change is undone by another channel change",
};

function policy(overrides: Partial<Policy> = {}): Policy {
  return {
    rules: [ALLOW, ALWAYS_ASK],
    wildcard: "*",
    ceilings: ["low", "medium", "high"],
    default: "Every change is put to a person unless a rule authorises it.", unmatched: "none",
    ...overrides,
  };
}

function mount(p: Policy | ApiError, role: "user" | "admin" = "admin") {
  if (p instanceof ApiError) {
    vi.spyOn(api, "approvalPolicy").mockRejectedValue(p);
  } else {
    vi.spyOn(api, "approvalPolicy").mockResolvedValue(p);
  }
  return renderWith(<ApprovalPolicy />, { session: sessionFor(role) });
}

/** The rule rows, in the order the page lists them: exclusions, then grants. */
function rows(): HTMLElement[] {
  return screen.getAllByRole("listitem");
}

beforeEach(() => {
  vi.spyOn(api, "plugins").mockResolvedValue({
    count: 1,
    plugins: [{
      name: "cnmaestro", type: "cnmaestro", version: "1.0.0",
      title: "Cambium", description: "Radios.",
      endpoint: "/mcp/cnmaestro", connect_url: "http://127.0.0.1:18080/mcp/cnmaestro",
      health: "healthy", tools: [], mutations: ["device.reboot", "radio.channel.set"],
      required: false, settings: [],
    }],
  });
});

/**
 * "Always ask" is a kind of rule, not the bottom of the risk scale. Offering it
 * as a level in one ordered list would say the opposite of what it does, so the
 * two are separate lists and only an allow rule has a ceiling to set.
 */
describe("the two kinds of rule", () => {
  it("lists them apart, under headings that say what each does", async () => {
    mount(policy());

    expect(await screen.findByRole("heading", { name: "Always ask" }))
      .toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Allow automatically" }))
      .toBeInTheDocument();

    // Always-ask rows come first, and only the allow row has a ceiling.
    expect(within(rows()[0]!).getByDisplayValue("never-reboot")).toBeInTheDocument();
    expect(within(rows()[1]!).getByDisplayValue("routine-radio")).toBeInTheDocument();
  });

  it("gives an always-ask rule no ceiling to set, because it has none", async () => {
    mount(policy());
    await screen.findByRole("heading", { name: "Allow automatically" });

    const ask = rows().find((r) => within(r).queryByDisplayValue("never-reboot"));
    const allow = rows().find((r) => within(r).queryByDisplayValue("routine-radio"));

    expect(within(ask!).queryByLabelText("Up to")).toBeNull();
    expect(within(allow!).getByLabelText("Up to")).toBeInTheDocument();
  });

  // The empty value is deliberately absent from `ceilings`: offering it would
  // make "always ask" look like the bottom of the scale.
  it("never offers the empty ceiling as a level", async () => {
    mount(policy());
    await screen.findByRole("heading", { name: "Allow automatically" });

    const allow = rows().find((r) => within(r).queryByDisplayValue("routine-radio"))!;
    const options = within(allow).getByLabelText("Up to")
      .querySelectorAll("option");
    expect([...options].map((o) => o.getAttribute("value")))
      .toEqual(["low", "medium", "high"]);
  });

  // There is no exception to an always-ask rule, which is the part that
  // surprises people, so the page says it where somebody would hit it.
  it("says how to write the nobody-but-Alice case", async () => {
    mount(policy());
    expect(await screen.findByText(/write just her allow rule and nothing here/i))
      .toBeInTheDocument();
  });

  it("moves a rule between the two by an explicit act, not by clearing a field", async () => {
    mount(policy());
    await screen.findByRole("heading", { name: "Allow automatically" });

    const allow = rows().find((r) => within(r).queryByDisplayValue("routine-radio"))!;
    await userEvent.click(within(allow).getByRole("button", { name: "Change to always ask" }));

    // Both rows are now always-ask, so neither offers a ceiling.
    expect(screen.queryByLabelText("Up to")).toBeNull();
    expect(rows()).toHaveLength(2);
  });
});

/**
 * The whole set is the unit, because it is the only one at which "no two rules
 * cover the same thing" can be checked.
 */
describe("saving", () => {
  it("sends every rule, canonicalised, and never the row's own key", async () => {
    const save = vi.spyOn(api, "saveApprovalPolicy").mockResolvedValue(policy());
    mount(policy({ rules: [ALLOW] }));
    await screen.findByRole("heading", { name: "Allow automatically" });

    const allow = rows()[0]!;
    await userEvent.clear(within(allow).getByLabelText("Proposed by"));
    await userEvent.type(within(allow).getByLabelText("Proposed by"), "svc:chatgpt");
    await userEvent.click(screen.getByRole("button", { name: "Save rules" }));

    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(save.mock.calls[0]![0]).toEqual([{
      id: "routine-radio", plugin: "cnmaestro", action: "*",
      principal: "svc:chatgpt", max_risk: "low",
      note: "a channel change is undone by another channel change",
    }]);
  });

  // The server refuses `""` so that "anything" has one spelling. A field the
  // operator emptied means the wildcard, and sending the empty string would
  // turn a harmless edit into a refusal.
  it("sends the wildcard for a selector left blank", async () => {
    const save = vi.spyOn(api, "saveApprovalPolicy").mockResolvedValue(policy());
    mount(policy({ rules: [ALLOW] }));
    await screen.findByRole("heading", { name: "Allow automatically" });

    await userEvent.clear(within(rows()[0]!).getByLabelText("Plugin"));
    await userEvent.click(screen.getByRole("button", { name: "Save rules" }));

    await waitFor(() => expect(save).toHaveBeenCalled());
    expect(save.mock.calls[0]![0][0]!.plugin).toBe("*");
  });

  it("will not save a rule with no name, which is what the trail records", async () => {
    mount(policy({ rules: [] }));
    await screen.findByText("No allow rules");

    await userEvent.click(screen.getByRole("button", { name: "Add an allow rule" }));
    expect(screen.getByRole("button", { name: "Save rules" })).toBeDisabled();
    expect(screen.getByText(/Every rule needs a name/i))
      .toBeInTheDocument();
  });

  it("says nothing was stored when the set is refused, and shows why", async () => {
    vi.spyOn(api, "saveApprovalPolicy").mockRejectedValue(new ApiError(
      400, "invalid_rules",
      'operations: rules "a" and "b" both cover cnmaestro/* for *; one scope, one rule',
    ));
    mount(policy({ rules: [ALLOW] }));
    await screen.findByRole("heading", { name: "Allow automatically" });

    await userEvent.clear(within(rows()[0]!).getByLabelText("Proposed by"));
    await userEvent.type(within(rows()[0]!).getByLabelText("Proposed by"), "svc:chatgpt");
    await userEvent.click(screen.getByRole("button", { name: "Save rules" }));

    // Everything is validated before anything is stored, so a refusal means
    // the set is exactly as it was -- which is the operator's next question.
    expect(
      await screen.findByText(/Nothing was saved — the rules are as they were/i),
    ).toBeInTheDocument();
    expect(screen.getByText(/one scope, one rule/)).toBeInTheDocument();
  });

  it("has nothing to save until something changes", async () => {
    mount(policy());
    await screen.findByRole("heading", { name: "Allow automatically" });

    expect(screen.getByRole("button", { name: "Save rules" })).toBeDisabled();
    expect(screen.getByText("Nothing to save.")).toBeInTheDocument();
  });
});

/**
 * Somebody who may read the host's configuration may read this. Changing it is
 * an administrator's, and the page renders read-only rather than offering
 * controls that answer 403.
 */
describe("a reader who is not an administrator", () => {
  it("sees the rules and no way to change them", async () => {
    mount(policy(), "user");

    expect(await screen.findByText("never-reboot")).toBeInTheDocument();
    expect(screen.getByText("cnmaestro / any, proposed by any")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save rules" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Add an allow rule" })).toBeNull();
    expect(screen.queryByLabelText("Name")).toBeNull();
  });
});

/**
 * A misspelled exclusion is the dangerous case, and it is dangerous quietly:
 * it authorises nothing, so it looks safe, but it never fires for the action
 * it was written for and the broader grant decides instead.
 */
describe("warnings", () => {
  const typo = 'rule "never-reboot" names action "device.rebooot", which no mounted '
    + "plugin registers, so it matches nothing";

  it("puts the warning beside the rule it is about", async () => {
    mount(policy({ warnings: [typo] }));
    await screen.findByRole("heading", { name: "Allow automatically" });

    const exclusion = rows().find((r) => within(r).queryByDisplayValue("never-reboot"))!;
    expect(within(exclusion).getByText(typo)).toBeInTheDocument();
    expect(within(exclusion).getByText(/not sending anything to a person/i))
      .toBeInTheDocument();
  });

  it("does not put that consequence on an allow rule, where it is not true", async () => {
    const allowTypo = 'rule "routine-radio" names plugin "cnmaestroo", which is not '
      + "mounted here, so it matches nothing";
    mount(policy({ warnings: [allowTypo] }));
    await screen.findByRole("heading", { name: "Allow automatically" });

    const allow = rows().find((r) => within(r).queryByDisplayValue("routine-radio"))!;
    expect(within(allow).getByText(allowTypo)).toBeInTheDocument();
    expect(within(allow).queryByText(/not sending anything to a person/i))
      .toBeNull();
  });

  // The parse is the fragile part, so a sentence it cannot place is shown
  // rather than dropped: a warning nobody sees is worse than one at the top.
  it("still shows a warning it cannot attribute", async () => {
    mount(policy({ warnings: ["something the parser has never seen"] }));
    expect(await screen.findByText("something the parser has never seen"))
      .toBeInTheDocument();
  });
});

describe("attributing warnings to rules", () => {
  it("reads the rule out of the server's phrasing", () => {
    const { byRule, loose } = warningsByRule(
      ['rule "a" names plugin "x", which is not mounted here'],
      ["a", "b"],
    );
    expect(byRule.get("a")).toHaveLength(1);
    expect(loose).toEqual([]);
  });

  it("keeps one naming a rule this page is not showing", () => {
    const warning = 'rule "gone" names plugin "x", which is not mounted here';
    const { byRule, loose } = warningsByRule([warning], ["a"]);
    expect(byRule.size).toBe(0);
    expect(loose).toEqual([warning]);
  });

  it("has nothing to say when the server sent none", () => {
    const { byRule, loose } = warningsByRule(undefined, ["a"]);
    expect(byRule.size).toBe(0);
    expect(loose).toEqual([]);
  });
});

/**
 * "No rules" and "the stored rules do not read" produce the same behaviour --
 * everything goes to a person -- and are different facts. The page has to say
 * which, because only one of them is what somebody configured.
 */
describe("rules that do not read", () => {
  const unreadable = () => new ApiError(
    409, "unreadable_rules",
    "the stored rules are not valid, so every change is being put to a person: "
    + "operations: a rule's plugin is null",
  );

  it("says so, rather than rendering an empty set", async () => {
    mount(unreadable());

    expect(await screen.findByText(/The saved rules can't be read/i))
      .toBeInTheDocument();
    expect(screen.queryByText("Always ask")).not.toBeInTheDocument();
  });

  it("does not offer a reader a way out they cannot take", async () => {
    mount(unreadable(), "user");

    await screen.findByText(/The saved rules can't be read/i);
    expect(screen.queryByRole("button", { name: /Start over with no rules/ }))
      .toBeNull();
  });

  // Without this the page is a dead end: the editor cannot be drawn over a set
  // it could not read, and `PUT /api/settings` refuses this key on purpose, so
  // nothing else in the console can replace it.
  it("lets an administrator start over with no rules", async () => {
    const save = vi.spyOn(api, "saveApprovalPolicy")
      .mockResolvedValue(policy({ rules: [] }));
    mount(unreadable());

    await userEvent.click(
      await screen.findByRole("button", { name: "Start over with no rules" }),
    );
    await userEvent.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Start over" }),
    );

    await waitFor(() => expect(save).toHaveBeenCalledWith([]));
    expect(await screen.findByText("No allow rules")).toBeInTheDocument();
    expect(screen.queryByText(/The saved rules can't be read/i)).not.toBeInTheDocument();
  });

  it("does nothing if the confirmation is declined", async () => {
    const save = vi.spyOn(api, "saveApprovalPolicy")
      .mockResolvedValue(policy({ rules: [] }));
    mount(unreadable());

    await userEvent.click(
      await screen.findByRole("button", { name: "Start over with no rules" }),
    );
    await userEvent.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Cancel" }),
    );

    expect(save).not.toHaveBeenCalled();
    expect(screen.getByText(/The saved rules can't be read/i)).toBeInTheDocument();
  });
});

/**
 * Asking the host what it would do is worth more than any amount of
 * explanatory copy: resolution is deterministic, so the answer is a fact.
 */
describe("asking whether a change would be authorised", () => {
  function ask() {
    return within(screen.getByRole("form", { name: "Would this run without asking?" }));
  }

  it("sends the change as asked and shows the server's own reason", async () => {
    const evaluate = vi.spyOn(api, "evaluateApprovalPolicy").mockResolvedValue({
      auto_approve: false,
      rule: ALWAYS_ASK,
      reason: "rule never-reboot (*/device.reboot for *) excludes this from "
        + "automatic authorisation",
    });
    mount(policy());
    await screen.findByRole("heading", { name: "Allow automatically" });

    await userEvent.type(ask().getByLabelText("Plugin"), "cnmaestro");
    await userEvent.type(ask().getByLabelText("Action"), "device.reboot");
    await userEvent.type(ask().getByLabelText("Proposed by"), "user:alice@example.com");
    await userEvent.click(ask().getByRole("button", { name: "Ask" }));

    await waitFor(() => expect(evaluate).toHaveBeenCalledWith({
      plugin: "cnmaestro",
      action: "device.reboot",
      principal: "user:alice@example.com",
      risk: "low",
      reversible: true,
    }));

    expect(await screen.findByText("Goes to a person")).toBeInTheDocument();
    expect(screen.getByText(/excludes this from automatic authorisation/))
      .toBeInTheDocument();
    expect(screen.getByText(/— always ask/))
      .toBeInTheDocument();
  });

  it("will not ask about half a change", async () => {
    mount(policy());
    await screen.findByRole("heading", { name: "Allow automatically" });

    await userEvent.type(ask().getByLabelText("Plugin"), "cnmaestro");
    expect(ask().getByRole("button", { name: "Ask" })).toBeDisabled();
  });

  // The endpoint reads what is stored. An answer that silently ignored the
  // edits on screen would be right about the host and wrong about the page.
  it("says the answer does not include unsaved edits", async () => {
    mount(policy({ rules: [ALLOW] }));
    await screen.findByRole("heading", { name: "Allow automatically" });

    expect(screen.queryByText(/aren't included/i)).not.toBeInTheDocument();

    await userEvent.clear(within(rows()[0]!).getByLabelText("Proposed by"));
    await userEvent.type(within(rows()[0]!).getByLabelText("Proposed by"), "svc:chatgpt");

    expect(screen.getByText(/aren't included/i)).toBeInTheDocument();
  });
});

/**
 * The page used to assert "anything no rule covers goes to a person" in its
 * own words, which stopped being true the moment that became a setting. A page
 * that describes a policy the host is not running is worse than one that says
 * nothing, because nobody has reason to doubt it.
 */
describe("what happens when no rule covers a change", () => {
  it("says what this host actually does", async () => {
    mount(policy({
      rules: [],
      default: "A change no rule covers goes ahead, on the understanding that "
        + "the assistant asked.",
      unmatched: "high",
    }));

    expect(await screen.findByText(/goes ahead, on the understanding/)).toBeInTheDocument();
    expect(screen.queryByText(/Anything no rule covers goes to a person/)).toBeNull();
  });

  it("offers the choice where the rules are", async () => {
    mount(policy({ rules: [], unmatched: "high" }));

    const choice = await screen.findByLabelText("When no rule covers a change");
    expect((choice as HTMLSelectElement).value).toBe("high");
  });

  it("saves the choice", async () => {
    const save = vi.spyOn(api, "saveSettings").mockResolvedValue({ applied: [] } as never);
    mount(policy({ rules: [], unmatched: "high" }));

    await userEvent.selectOptions(
      await screen.findByLabelText("When no rule covers a change"), "none");
    expect(save).toHaveBeenCalledWith({ "approval.unmatched": "none" });
  });
});
