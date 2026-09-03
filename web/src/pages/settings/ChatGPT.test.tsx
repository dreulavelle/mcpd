import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, type ChatGPTAccount } from "@/lib/api";
import { renderWith } from "@/test/render";
import { ChatGPT } from "./ChatGPT";

function account(overrides: Partial<ChatGPTAccount> = {}): ChatGPTAccount {
  return {
    id: "acct_1",
    name: "Work",
    principal: "svc:chatgpt:work",
    role: "role_operator",
    role_name: "Operator",
    grants: [{ plugin: "*", level: "write" }],
    plugins: ["*"],
    rate_per_sec: 0,
    enabled: true,
    has_admin_key: true,
    organization_id: "org_123",
    can_manage: true,
    created_at: "2026-08-27T09:00:00Z",
    ...overrides,
  };
}

function stub(accounts: ChatGPTAccount[], plugins: string[] = ["echo", "graylog"]) {
  vi.spyOn(api, "chatgptAccounts").mockResolvedValue({ accounts, plugins });
}

describe("the ChatGPT accounts page", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    window.localStorage.clear();
    window.sessionStorage.clear();
  });

  // No accounts means no tunnel can connect at all, which is a different thing
  // from a page with nothing on it yet. It says what to add and why.
  it("says an empty list is why nothing can connect", async () => {
    stub([]);
    renderWith(<ChatGPT />);
    expect(await screen.findByText(/no tunnel can connect/i)).toBeInTheDocument();
  });

  /**
   * The whole reason accounts exist: two workspaces, two identities, and a
   * history that can say which of them made a call. An account that did not
   * show its identity would leave that unanswerable from the page.
   */
  it("shows each account's own identity and grant", async () => {
    stub([
      account(),
      account({
        id: "acct_2", name: "Support", principal: "svc:chatgpt:support",
        role: "role_operator", role_name: "Operator",
        grants: [{ plugin: "graylog", level: "read" }], plugins: ["graylog"], rate_per_sec: 2,
      }),
    ]);
    renderWith(<ChatGPT />);

    expect(await screen.findByText("Work")).toBeInTheDocument();
    const support = screen.getByText("Support").closest("tr")!;
    expect(support).toHaveTextContent("svc:chatgpt:support");
    expect(support).toHaveTextContent("graylog");
    expect(support).toHaveTextContent("2/sec");
  });

  /**
   * The role decides one thing: whether a plugin tool marked administrative
   * may be called (`plugins:write`). It is not the control over whether
   * ChatGPT can change your systems -- every account can propose a change,
   * and the conversation is where that is agreed. Each row shows its own
   * account's role by name, not a marker that collapses every role but one
   * into "ordinary".
   */
  it("shows each account's own role by name", async () => {
    stub([
      account({ id: "acct_1", name: "Work", role: "role_operator", role_name: "Operator" }),
      account({
        id: "acct_2", name: "Ops", principal: "svc:chatgpt:ops",
        role: "role_administrator", role_name: "Administrator",
      }),
    ]);
    renderWith(<ChatGPT />);

    const work = (await screen.findByText("Work")).closest("tr")!;
    expect(work).toHaveTextContent("Operator");
    const ops = screen.getByText("Ops").closest("tr")!;
    expect(ops).toHaveTextContent("Administrator");
  });

  // Zero is unlimited and is the ordinary answer. Rendering it as "0/sec"
  // would read as an account that may make no calls at all.
  it("shows no rate limit as absent rather than as zero", async () => {
    stub([account({ rate_per_sec: 0 })]);
    renderWith(<ChatGPT />);

    const row = (await screen.findByText("Work")).closest("tr")!;
    expect(row).not.toHaveTextContent("0/sec");
  });

  /**
   * The bug this guards: the page never reads a key back, so an edit that
   * changes only the rate limit carries no key. Sending an empty string would
   * erase the stored one and take every tunnel on the account offline.
   */
  it("omits the key from an edit that did not retype it", async () => {
    stub([account()]);
    const update = vi.spyOn(api, "updateChatGPTAccount")
      .mockResolvedValue(account({ rate_per_sec: 5 }));
    renderWith(<ChatGPT />);

    await userEvent.click(await screen.findByRole("button", { name: "Edit" }));
    const rate = await screen.findByLabelText("Rate limit");
    await userEvent.clear(rate);
    await userEvent.type(rate, "5");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(update).toHaveBeenCalled();
    const [, body] = update.mock.calls[0]!;
    expect(body).not.toHaveProperty("api_key");
    expect(body.rate_per_sec).toBe(5);
  });

  // The admin key follows the same rule. It used to be sent on every edit,
  // blank included, so changing a rate limit erased it and the Tunnels page
  // lost its Add form with nothing saying why.
  it("leaves the stored admin key alone on an edit that did not mention it", async () => {
    stub([account()]);
    const update = vi.spyOn(api, "updateChatGPTAccount")
      .mockResolvedValue(account({ rate_per_sec: 5 }));
    renderWith(<ChatGPT />);

    await userEvent.click(await screen.findByRole("button", { name: "Edit" }));
    const rate = await screen.findByLabelText("Rate limit");
    await userEvent.clear(rate);
    await userEvent.type(rate, "5");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    const [, body] = update.mock.calls[0]!;
    expect(body).not.toHaveProperty("admin_key");
  });

  // Clearing it is still possible, and has to be asked for by name.
  it("removes the stored admin key only when told to", async () => {
    stub([account()]);
    const update = vi.spyOn(api, "updateChatGPTAccount")
      .mockResolvedValue(account({ has_admin_key: false }));
    renderWith(<ChatGPT />);

    await userEvent.click(await screen.findByRole("button", { name: "Edit" }));
    await userEvent.click(await screen.findByLabelText("Remove the stored admin key"));
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    const [, body] = update.mock.calls[0]!;
    expect(body.admin_key).toBe("");
  });

  // An empty grant would reach nothing, which is never what leaving the field
  // blank meant.
  it("reads a blank reach as everything", async () => {
    stub([]);
    const add = vi.spyOn(api, "addChatGPTAccount").mockResolvedValue(account());
    renderWith(<ChatGPT />);

    await userEvent.click(await screen.findByRole("button", { name: "Add account" }));
    await userEvent.type(screen.getByLabelText("Name"), "Work");
    await userEvent.type(screen.getByLabelText("OpenAI key"), "sk-test");
    await userEvent.click(screen.getByRole("button", { name: "Add" }));

    expect(add).toHaveBeenCalled();
    expect(add.mock.calls[0]![0].grants).toEqual([{ plugin: "*", level: "write" }]);
  });

  // A new account with no key cannot run a tunnel, so the form does not offer
  // to save one -- the refusal belongs before the request, not after it.
  it("will not add an account with no key", async () => {
    stub([]);
    renderWith(<ChatGPT />);

    await userEvent.click(await screen.findByRole("button", { name: "Add account" }));
    await userEvent.type(screen.getByLabelText("Name"), "Work");
    expect(screen.getByRole("button", { name: "Add" })).toBeDisabled();
  });

  // An account with no admin key can still run tunnels whose ids were pasted
  // in. Saying so is what stops it reading as broken.
  it("says when an account cannot make tunnels", async () => {
    stub([account({ has_admin_key: false, organization_id: "", can_manage: false })]);
    renderWith(<ChatGPT />);
    expect(await screen.findByText(/no admin key/i)).toBeInTheDocument();
  });
});
