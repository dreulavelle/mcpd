import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, ApiError, type ApprovalPolicy as Policy } from "@/lib/api";
import { renderWith, sessionFor } from "@/test/render";
import { ApprovalPolicy, warningsByRule } from "./ApprovalPolicy";

const EXCLUSION = {
  id: "never-reboot", plugin: "*", action: "device.reboot",
  principal: "*", max_risk: "",
  note: "a reboot drops every client on the radio",
};

const GRANT = {
  id: "routine-radio", plugin: "cnmaestro", action: "*",
  principal: "*", max_risk: "low",
  note: "a channel change is undone by another channel change",
};

function policy(overrides: Partial<Policy> = {}): Policy {
  return {
    rules: [GRANT, EXCLUSION],
    wildcard: "*",
    ceilings: ["low", "medium", "high"],
    default: "Every change is put to a person unless a rule authorises it.",
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
 * An exclusion is a distinct choice, not a level.
 *
 * An empty ceiling authorises nothing and beats every grant it overlaps,
 * whatever their scopes are. Offering it as the bottom of one ordered list of
 * levels would say the opposite of what it does, so the two live in separate
 * lists and only a grant is given a ceiling to set.
 */
describe("exclusions and grants", () => {
  it("lists them apart, and says which is which", async () => {
    mount(policy());

    expect(await screen.findByText("Exclusions — always ask")).toBeInTheDocument();
    expect(screen.getByText("Grants — authorised in advance")).toBeInTheDocument();
    expect(screen.getByText("Exclusion — always ask")).toBeInTheDocument();
    expect(screen.getByText("Grant — up to Low")).toBeInTheDocument();
  });

  it("gives an exclusion no ceiling to set, because it has none", async () => {
    mount(policy());
    await screen.findByText("Exclusion — always ask");

    const exclusion = rows().find((r) => within(r).queryByDisplayValue("never-reboot"));
    const grant = rows().find((r) => within(r).queryByDisplayValue("routine-radio"));

    expect(within(exclusion!).queryByLabelText("Authorises up to")).toBeNull();
    expect(within(grant!).getByLabelText("Authorises up to")).toBeInTheDocument();
  });

  // The empty value is deliberately absent from `ceilings`. A dropdown that
  // offered it would make "never" look like the bottom of the scale rather
  // than the thing that beats everything on it.
  it("never offers the empty ceiling as a level", async () => {
    mount(policy());
    await screen.findByText("Grant — up to Low");

    const grant = rows().find((r) => within(r).queryByDisplayValue("routine-radio"))!;
    const options = within(grant).getByLabelText("Authorises up to")
      .querySelectorAll("option");
    expect([...options].map((o) => o.getAttribute("value")))
      .toEqual(["low", "medium", "high"]);
  });

  // A grant cannot carve an exception out of an exclusion, which is the one
  // consequence of deny-wins that surprises people. It is stated rather than
  // left to be discovered.
  it("states the cost of exclusion-wins", async () => {
    mount(policy());
    expect(await screen.findByText(/the absence of a grant already means ask/i))
      .toBeInTheDocument();
  });

  it("moves a rule between the two by an explicit act, not by clearing a field", async () => {
    mount(policy());
    await screen.findByText("Grant — up to Low");

    const grant = rows().find((r) => within(r).queryByDisplayValue("routine-radio"))!;
    await userEvent.click(within(grant).getByRole("button", { name: "Make it an exclusion" }));

    expect(screen.queryByText("Grant — up to Low")).not.toBeInTheDocument();
    expect(screen.getAllByText("Exclusion — always ask")).toHaveLength(2);
  });
});

/**
 * The whole set is the unit, because it is the only one at which "no two rules
 * cover the same thing" can be checked.
 */
describe("saving", () => {
  it("sends every rule, canonicalised, and never the row's own key", async () => {
    const save = vi.spyOn(api, "saveApprovalPolicy").mockResolvedValue(policy());
    mount(policy({ rules: [GRANT] }));
    await screen.findByText("Grant — up to Low");

    const grant = rows()[0]!;
    await userEvent.clear(within(grant).getByLabelText("Principal"));
    await userEvent.type(within(grant).getByLabelText("Principal"), "svc:chatgpt");
    await userEvent.click(screen.getByRole("button", { name: "Save all rules" }));

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
    mount(policy({ rules: [GRANT] }));
    await screen.findByText("Grant — up to Low");

    await userEvent.clear(within(rows()[0]!).getByLabelText("Plugin"));
    await userEvent.click(screen.getByRole("button", { name: "Save all rules" }));

    await waitFor(() => expect(save).toHaveBeenCalled());
    expect(save.mock.calls[0]![0][0]!.plugin).toBe("*");
  });

  it("will not save a rule with no id, because the id is what the trail names", async () => {
    mount(policy({ rules: [] }));
    await screen.findByText("No grants");

    await userEvent.click(screen.getByRole("button", { name: "Add a grant" }));
    expect(screen.getByRole("button", { name: "Save all rules" })).toBeDisabled();
    expect(screen.getByText(/needs an id before the set can be saved/i))
      .toBeInTheDocument();
  });

  it("says nothing was stored when the set is refused, and shows why", async () => {
    vi.spyOn(api, "saveApprovalPolicy").mockRejectedValue(new ApiError(
      400, "invalid_rules",
      'operations: rules "a" and "b" both cover cnmaestro/* for *; one scope, one rule',
    ));
    mount(policy({ rules: [GRANT] }));
    await screen.findByText("Grant — up to Low");

    await userEvent.clear(within(rows()[0]!).getByLabelText("Principal"));
    await userEvent.type(within(rows()[0]!).getByLabelText("Principal"), "svc:chatgpt");
    await userEvent.click(screen.getByRole("button", { name: "Save all rules" }));

    // Everything is validated before anything is stored, so a refusal means
    // the set is exactly as it was -- which is the operator's next question.
    expect(
      await screen.findByText(/Every rule is checked before any of them is stored/i),
    ).toBeInTheDocument();
    expect(screen.getByText(/one scope, one rule/)).toBeInTheDocument();
  });

  it("has nothing to save until something changes", async () => {
    mount(policy());
    await screen.findByText("Grant — up to Low");

    expect(screen.getByRole("button", { name: "Save all rules" })).toBeDisabled();
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
    expect(screen.getByText("cnmaestro/* for *")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save all rules" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Add a grant" })).toBeNull();
    expect(screen.queryByLabelText("Rule id")).toBeNull();
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
    await screen.findByText("Exclusion — always ask");

    const exclusion = rows().find((r) => within(r).queryByDisplayValue("never-reboot"))!;
    expect(within(exclusion).getByText(typo)).toBeInTheDocument();
    expect(within(exclusion).getByText(/not protecting what it was written for/i))
      .toBeInTheDocument();
  });

  it("does not put that consequence on a grant, where it is not true", async () => {
    const grantTypo = 'rule "routine-radio" names plugin "cnmaestroo", which is not '
      + "mounted here, so it matches nothing";
    mount(policy({ warnings: [grantTypo] }));
    await screen.findByText("Grant — up to Low");

    const grant = rows().find((r) => within(r).queryByDisplayValue("routine-radio"))!;
    expect(within(grant).getByText(grantTypo)).toBeInTheDocument();
    expect(within(grant).queryByText(/not protecting what it was written for/i))
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

    expect(await screen.findByText(/The stored rules do not read/i))
      .toBeInTheDocument();
    expect(screen.queryByText("Exclusions — always ask")).not.toBeInTheDocument();
  });

  it("does not offer a reader a way out they cannot take", async () => {
    mount(unreadable(), "user");

    await screen.findByText(/The stored rules do not read/i);
    expect(screen.queryByRole("button", { name: /Replace them with no rules/ }))
      .toBeNull();
  });

  // Without this the page is a dead end: the editor cannot be drawn over a set
  // it could not read, and `PUT /api/settings` refuses this key on purpose, so
  // nothing else in the console can replace it.
  it("lets an administrator replace the whole set with none", async () => {
    const save = vi.spyOn(api, "saveApprovalPolicy")
      .mockResolvedValue(policy({ rules: [] }));
    vi.spyOn(window, "confirm").mockReturnValue(true);
    mount(unreadable());

    await userEvent.click(
      await screen.findByRole("button", { name: "Replace them with no rules" }),
    );

    await waitFor(() => expect(save).toHaveBeenCalledWith([]));
    expect(await screen.findByText("No grants")).toBeInTheDocument();
    expect(screen.queryByText(/The stored rules do not read/i)).not.toBeInTheDocument();
  });

  it("does nothing if the confirmation is declined", async () => {
    const save = vi.spyOn(api, "saveApprovalPolicy")
      .mockResolvedValue(policy({ rules: [] }));
    vi.spyOn(window, "confirm").mockReturnValue(false);
    mount(unreadable());

    await userEvent.click(
      await screen.findByRole("button", { name: "Replace them with no rules" }),
    );

    expect(save).not.toHaveBeenCalled();
    expect(screen.getByText(/The stored rules do not read/i)).toBeInTheDocument();
  });
});

/**
 * Asking the host what it would do is worth more than any amount of
 * explanatory copy: resolution is deterministic, so the answer is a fact.
 */
describe("asking whether a change would be authorised", () => {
  function ask() {
    return within(screen.getByRole("form", { name: "Would this be authorised?" }));
  }

  it("sends the change as asked and shows the server's own reason", async () => {
    const evaluate = vi.spyOn(api, "evaluateApprovalPolicy").mockResolvedValue({
      auto_approve: false,
      rule: EXCLUSION,
      reason: "rule never-reboot (*/device.reboot for *) excludes this from "
        + "automatic authorisation",
    });
    mount(policy());
    await screen.findByText("Grant — up to Low");

    await userEvent.type(ask().getByLabelText("Plugin"), "cnmaestro");
    await userEvent.type(ask().getByLabelText("Action"), "device.reboot");
    await userEvent.type(ask().getByLabelText("Principal"), "user:alice@example.com");
    await userEvent.click(ask().getByRole("button", { name: "Ask" }));

    await waitFor(() => expect(evaluate).toHaveBeenCalledWith({
      plugin: "cnmaestro",
      action: "device.reboot",
      principal: "user:alice@example.com",
      risk: "low",
      reversible: true,
    }));

    expect(await screen.findByText("A person would be asked")).toBeInTheDocument();
    expect(screen.getByText(/excludes this from automatic authorisation/))
      .toBeInTheDocument();
    expect(screen.getByText(/an exclusion, which authorises nothing/))
      .toBeInTheDocument();
  });

  it("will not ask about half a change", async () => {
    mount(policy());
    await screen.findByText("Grant — up to Low");

    await userEvent.type(ask().getByLabelText("Plugin"), "cnmaestro");
    expect(ask().getByRole("button", { name: "Ask" })).toBeDisabled();
  });

  // The endpoint reads what is stored. An answer that silently ignored the
  // edits on screen would be right about the host and wrong about the page.
  it("says the answer does not include unsaved edits", async () => {
    mount(policy({ rules: [GRANT] }));
    await screen.findByText("Grant — up to Low");

    expect(screen.queryByText(/have not been saved/i)).not.toBeInTheDocument();

    await userEvent.clear(within(rows()[0]!).getByLabelText("Principal"));
    await userEvent.type(within(rows()[0]!).getByLabelText("Principal"), "svc:chatgpt");

    expect(screen.getByText(/have not been saved/i)).toBeInTheDocument();
  });
});
