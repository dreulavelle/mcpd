import { beforeEach, describe, expect, it, vi } from "vitest";
import { useState } from "react";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api } from "@/lib/api";
import { renderWith } from "@/test/render";
import { WorkspacePicker } from "./WorkspacePicker";

function Harness({ initial = [], initialDefault = "" }: { initial?: string[]; initialDefault?: string }) {
  const [value, setValue] = useState(initial);
  const [def, setDef] = useState(initialDefault);
  return (
    <>
      <WorkspacePicker accountId="acct_1" canLookUp value={value} onChange={setValue}
                       defaultWorkspace={def} onDefault={setDef} />
      <output data-testid="state">{JSON.stringify({ value, def })}</output>
    </>
  );
}

const state = () => JSON.parse(screen.getByTestId("state").textContent ?? "{}");

describe("the workspace picker", () => {
  beforeEach(() => vi.restoreAllMocks());

  // OpenAI lists no workspaces, so a person had to find an id they could not
  // see. The ones the organisation's tunnels already sit in are offered, most
  // used first, and none is kept until the account is saved and verified.
  it("offers the workspaces the organisation already uses", async () => {
    vi.spyOn(api, "workspaceCandidates").mockResolvedValue({ workspaces: [
      { workspace_id: "ws_saved", tunnels: 4, saved: true, default: false },
      { workspace_id: "ws_busy", tunnels: 3, saved: false, default: false },
    ] });
    renderWith(<Harness initial={["ws_saved"]} />);
    await userEvent.click(screen.getByRole("button", { name: "Find workspaces" }));
    const select = await screen.findByLabelText("Workspaces this organisation uses");
    // The one already saved is not offered again.
    expect(screen.queryByRole("option", { name: /ws_saved/ })).toBeNull();
    await userEvent.selectOptions(select, "ws_busy");
    await userEvent.click(screen.getByRole("button", { name: "Add the selected workspace" }));
    expect(state().value).toEqual(["ws_saved", "ws_busy"]);
  });

  it("takes a typed id, and the default goes with a removed workspace", async () => {
    renderWith(<Harness initial={["ws_a"]} />);
    await userEvent.type(screen.getByLabelText("Workspace id"), "ws_b");
    await userEvent.click(screen.getByRole("button", { name: "Add the typed workspace" }));
    await userEvent.click(screen.getByLabelText("Make ws_b the default"));
    expect(state()).toEqual({ value: ["ws_a", "ws_b"], def: "ws_b" });

    await userEvent.click(screen.getByRole("button", { name: "Remove ws_b" }));
    expect(state()).toEqual({ value: ["ws_a"], def: "" });
  });
});
