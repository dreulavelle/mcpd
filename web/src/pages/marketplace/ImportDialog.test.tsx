import { describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, ApiError } from "@/lib/api";
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

/**
 * The suggested name collides far more often than it looks like it should.
 *
 * A great many published servers are named `something/mcp`, so the catalogue
 * suggests `mcp` for all of them and the second one an operator adds is
 * refused. The refusal is correct and the timing is not: it costs a round trip
 * and reads as a failure rather than as a field to change.
 */
describe("a name already in use", () => {
  it("is refused in the form, before anything is sent", async () => {
    const importServer = vi.spyOn(api, "importMCPServer");
    renderWith(
      <ImportDialog
        open onOpenChange={() => {}} onImported={() => {}}
        seedName="mcp" seedDocument={DOCUMENT}
        taken={new Set(["mcp"])}
      />,
    );

    expect(screen.getByText(/is already here/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add" })).toBeDisabled();
    expect(importServer).not.toHaveBeenCalled();
  });

  it("clears once a free name is typed", async () => {
    renderWith(
      <ImportDialog
        open onOpenChange={() => {}} onImported={() => {}}
        seedName="mcp" seedDocument={DOCUMENT}
        taken={new Set(["mcp"])}
      />,
    );

    await userEvent.type(screen.getByLabelText("Name"), "-linear");
    expect(screen.queryByText(/is already here/)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add" })).toBeEnabled();
  });

  /**
   * The page cannot know every name -- a built-in plugin holds one too -- so
   * the server still refuses some. Its sentence goes next to the box it is
   * about, and takes the cursor with it.
   */
  it("puts the server's refusal on the field, not at the top of the dialog", async () => {
    vi.spyOn(api, "importMCPServer").mockRejectedValue(
      new ApiError(400, "bad_request", 'a plugin named "weather" already exists'),
    );
    renderWith(
      <ImportDialog
        open onOpenChange={() => {}} onImported={() => {}}
        seedName="weather" seedDocument={DOCUMENT}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: "Add" }));

    expect(await screen.findByText(/already exists/)).toBeInTheDocument();
    await waitFor(() => expect(screen.getByLabelText("Name")).toHaveFocus());
  });
});

/**
 * Where the detail went.
 *
 * Version, transport, endpoint, credential, catalogue and date used to be on
 * every card in the listing. They are here instead: one person, one server,
 * having decided to look. The credential line is the one they act on --
 * needing an API key is better learned before pressing Add than after.
 *
 * None of it is imported. What is imported is the text in the box, by the same
 * call a pasted document makes.
 */
describe("the detail behind a catalogued entry", () => {
  const ENTRY = {
    name: "com.example/weather",
    suggested_name: "weather",
    title: "Weather",
    description: "Forecasts and observations.",
    version: "1.0.0",
    transport: "streamable-http",
    url: "https://weather.example/mcp",
    updated_at: "2026-08-01T10:00:00Z",
    addable: true,
    auth: "api_key",
    source: "registry.modelcontextprotocol.io",
  };

  it("shows what the card no longer does", async () => {
    renderWith(
      <ImportDialog
        open onOpenChange={() => {}} onImported={() => {}}
        seedName="weather" seedDocument={DOCUMENT} seedEntry={ENTRY}
      />,
    );

    expect(screen.getByText("1.0.0")).toBeInTheDocument();
    expect(screen.getByText("streamable-http")).toBeInTheDocument();
    expect(screen.getByText("https://weather.example/mcp")).toBeInTheDocument();
    expect(screen.getByText("registry.modelcontextprotocol.io")).toBeInTheDocument();
    // The one an operator has to do something about.
    expect(screen.getByText("Needs an API key")).toBeInTheDocument();
  });

  it("says plainly when nothing has to be found first", async () => {
    renderWith(
      <ImportDialog
        open onOpenChange={() => {}} onImported={() => {}}
        seedName="weather" seedDocument={DOCUMENT}
        seedEntry={{ ...ENTRY, auth: "none" }}
      />,
    );

    expect(screen.getByText("No credential")).toBeInTheDocument();
  });

  /**
   * Smithery versions a deployment rather than a release and Docker versions
   * an image, so both leave the version blank. A dash there would read as a
   * value somebody chose.
   */
  it("leaves out what the catalogue did not fill in", async () => {
    renderWith(
      <ImportDialog
        open onOpenChange={() => {}} onImported={() => {}}
        seedName="weather" seedDocument={DOCUMENT}
        seedEntry={{ ...ENTRY, version: "", auth: "" }}
      />,
    );

    expect(screen.queryByText("Version")).not.toBeInTheDocument();
    expect(screen.queryByText("Credential")).not.toBeInTheDocument();
    expect(screen.getByText("Transport")).toBeInTheDocument();
  });

  it("shows none of it for a pasted document, which has no catalogue entry", async () => {
    renderWith(<ImportDialog open onOpenChange={() => {}} onImported={() => {}} />);

    expect(screen.queryByText("Transport")).not.toBeInTheDocument();
    expect(screen.queryByText("Catalogue")).not.toBeInTheDocument();
  });

  it("imports the document rather than anything shown beside it", async () => {
    const importServer = vi.spyOn(api, "importMCPServer")
      .mockResolvedValue({ status: "imported" });
    renderWith(
      <ImportDialog
        open onOpenChange={() => {}} onImported={() => {}}
        seedName="weather" seedDocument={DOCUMENT} seedEntry={ENTRY}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: "Add" }));
    await waitFor(() =>
      expect(importServer).toHaveBeenCalledWith("weather", DOCUMENT));
  });
});
