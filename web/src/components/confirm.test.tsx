import { describe, expect, it } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWith } from "@/test/render";
import { parseQuestion, useConfirm } from "./confirm";

function Asker({ onAnswer }: { onAnswer: (yes: boolean) => void }) {
  const confirm = useConfirm();
  return (
    <button type="button" onClick={async () => onAnswer(await confirm("Delete it? It is gone for good."))}>
      Delete
    </button>
  );
}

describe("asking before doing", () => {
  // A plain sentence is split where the question ends, so a call site can
  // keep its one string and still get a title and a description.
  it("reads a sentence as a question and what follows it", () => {
    expect(parseQuestion("Delete it? It is gone for good.")).toEqual({
      title: "Delete it?", description: "It is gone for good.",
    });
    expect(parseQuestion("Really")).toEqual({ title: "Really" });
  });

  it("resolves true only when the action is chosen", async () => {
    const answers: boolean[] = [];
    renderWith(<Asker onAnswer={(a) => answers.push(a)} />);

    await userEvent.click(screen.getByRole("button", { name: "Delete" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText("It is gone for good.")).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Delete" }));
    expect(answers).toEqual([true]);

    await userEvent.click(screen.getByRole("button", { name: "Delete" }));
    await userEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Cancel" }));
    expect(answers).toEqual([true, false]);
  });

  it("answers no when the dialog is dismissed with Escape", async () => {
    const answers: boolean[] = [];
    renderWith(<Asker onAnswer={(a) => answers.push(a)} />);
    await userEvent.click(screen.getByRole("button", { name: "Delete" }));
    await screen.findByRole("alertdialog");
    await userEvent.keyboard("{Escape}");
    expect(answers).toEqual([false]);
  });
});
