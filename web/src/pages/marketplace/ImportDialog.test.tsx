import { describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api } from "@/lib/api";
import { renderWith } from "@/test/render";
import { ImportDialog } from "./ImportDialog";

const DOCUMENT = {
  $schema: "https://static.modelcontextprotocol.io/schemas/2025-07-09/server.schema.json",
  name: "com.example/weather",
  version: "1.0.0",
  remotes: [{ type: "streamable-http", url: "https://weather.example/mcp" }],
};

/**
 * One way in, whether the document was pasted or came from the catalog.
 *
 * A catalogued server that skipped a step -- the JSON check here, the schema
 * check on the server, the settings derived from the document's inputs, the
 * tools waiting to be classified -- would be a server nobody read being served
 * because it came from a list that looked official. So there is one import,
 * and the catalog seeds its fields rather than having its own.
 */
describe("adding a remote MCP server", () => {
  it("sends a pasted document through, parsed", async () => {
    const importServer = vi.spyOn(api, "importMCPServer")
      .mockResolvedValue({ status: "imported" });
    renderWith(
      <ImportDialog open onOpenChange={() => {}} onImported={() => {}} />,
    );

    await userEvent.type(screen.getByLabelText("Name"), "weather");
    await userEvent.click(screen.getByLabelText("server.json"));
    await userEvent.paste(JSON.stringify(DOCUMENT));
    await userEvent.click(screen.getByRole("button", { name: "Add" }));

    await waitFor(() =>
      expect(importServer).toHaveBeenCalledWith("weather", DOCUMENT));
  });

  it("takes a catalogued document down the same path", async () => {
    const importServer = vi.spyOn(api, "importMCPServer")
      .mockResolvedValue({ status: "imported" });
    renderWith(
      <ImportDialog
        open onOpenChange={() => {}} onImported={() => {}}
        seedName="weather" seedDocument={DOCUMENT}
      />,
    );

    // Seeded, and still shown: the operator reads what they are adding rather
    // than agreeing to a name in a list.
    expect(screen.getByLabelText("Name")).toHaveValue("weather");
    expect(screen.getByLabelText("server.json"))
      .toHaveValue(JSON.stringify(DOCUMENT, null, 2));

    await userEvent.click(screen.getByRole("button", { name: "Add" }));

    await waitFor(() =>
      expect(importServer).toHaveBeenCalledWith("weather", DOCUMENT));
  });

  // Caught in the browser rather than sent: an error naming the character and
  // the position reads better than "the document could not be read".
  it("refuses a document that is not JSON before asking the server", async () => {
    const importServer = vi.spyOn(api, "importMCPServer");
    renderWith(
      <ImportDialog open onOpenChange={() => {}} onImported={() => {}} />,
    );

    await userEvent.type(screen.getByLabelText("Name"), "weather");
    await userEvent.click(screen.getByLabelText("server.json"));
    await userEvent.paste("{ not json");
    await userEvent.click(screen.getByRole("button", { name: "Add" }));

    expect(await screen.findByText(/not valid JSON/i)).toBeInTheDocument();
    expect(importServer).not.toHaveBeenCalled();
  });
});
