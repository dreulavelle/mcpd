import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, ApiError, type MCPTool } from "@/lib/api";
import { renderWith } from "@/test/render";
import { ClassifyDialog } from "./ClassifyDialog";

function toolFixture(overrides: Partial<MCPTool> = {}): MCPTool {
  return {
    name: "search_docs",
    descriptor: {
      name: "search_docs",
      description: "Searches the documentation.",
      inputSchema: { type: "object", properties: { q: { type: "string" } } },
      annotations: { readOnlyHint: true },
    },
    descriptor_hash: "aaaaaaaaaaaaaaaaaaaa1111",
    state: "pending",
    first_seen_at: "2026-08-01T10:00:00Z",
    last_seen_at: "2026-08-20T10:00:00Z",
    ...overrides,
  };
}

describe("classifying a remote tool", () => {
  beforeEach(() => {
    vi.spyOn(api, "classifyMCPTool").mockResolvedValue({ status: "saved" });
    vi.spyOn(api, "mcpServerTools").mockResolvedValue({ tools: [], count: 0 });
  });

  it("sends the hash of the descriptor that was on screen", async () => {
    const tool = toolFixture();
    renderWith(
      <ClassifyDialog
        server="weather" tool={tool} open
        onOpenChange={() => {}} onDone={() => {}}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: "Serve this tool" }));

    expect(api.classifyMCPTool).toHaveBeenCalledWith(
      "weather", "search_docs", "enabled", "aaaaaaaaaaaaaaaaaaaa1111",
    );
  });

  /**
   * Annotations are the far end's claims, and the specification says a client
   * must not rely on them. The dialog has to say so on the same screen as the
   * value, or it is presenting a claim as a finding.
   */
  it("presents the server's annotations as claims rather than facts", () => {
    renderWith(
      <ClassifyDialog
        server="weather" tool={toolFixture()} open
        onOpenChange={() => {}} onDone={() => {}}
      />,
    );

    expect(screen.getByText("What the server says about it")).toBeInTheDocument();
    expect(screen.getByText("Read-only: yes")).toBeInTheDocument();
    expect(
      screen.getByText(/must not rely on them/i),
    ).toBeInTheDocument();
  });

  it("refuses to offer a decision on a tool that cannot be served", () => {
    renderWith(
      <ClassifyDialog
        server="weather"
        tool={toolFixture({ problem: "its input schema is not an object" })}
        open onOpenChange={() => {}} onDone={() => {}}
      />,
    );

    expect(screen.getByRole("button", { name: "Serve this tool" })).toBeDisabled();
  });
});

/**
 * A 409 means the descriptor changed underneath the operator.
 *
 * The decision must not be recorded, because it would be a decision about a
 * description and a schema nobody looked at. The dialog has to say what
 * changed and make them read it again — never resend with the newer hash.
 */
describe("when the descriptor changed underneath", () => {
  const was = toolFixture();
  const now = toolFixture({
    descriptor: {
      ...was.descriptor,
      description: "Searches the documentation, and posts a summary.",
    },
    descriptor_hash: "bbbbbbbbbbbbbbbbbbbb2222",
  });

  beforeEach(() => {
    vi.spyOn(api, "classifyMCPTool").mockRejectedValue(
      new ApiError(409, "conflict", "the descriptor changed"),
    );
    vi.spyOn(api, "mcpServerTools").mockResolvedValue({ tools: [now], count: 1 });
  });

  it("says the tool changed, and does not record the decision", async () => {
    renderWith(
      <ClassifyDialog
        server="weather" tool={was} open
        onOpenChange={() => {}} onDone={() => {}}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: "Serve this tool" }));

    expect(
      await screen.findByText(/This tool changed while you were reading it/i),
    ).toBeInTheDocument();
  });

  it("shows both hashes so the operator can see it is a different thing", async () => {
    renderWith(
      <ClassifyDialog
        server="weather" tool={was} open
        onOpenChange={() => {}} onDone={() => {}}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: "Serve this tool" }));

    expect(await screen.findByText(/was aaaaaaaaaaaaaaaa/)).toBeInTheDocument();
    expect(screen.getByText(/now bbbbbbbbbbbbbbbb/)).toBeInTheDocument();
  });

  it("shows what actually differs", async () => {
    renderWith(
      <ClassifyDialog
        server="weather" tool={was} open
        onOpenChange={() => {}} onDone={() => {}}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: "Serve this tool" }));

    // Scoped to the diff's own <dt>: "Description" also labels the descriptor
    // the operator was reading, which is the point -- both are on screen.
    expect(
      await screen.findByText("Description", { selector: "dt" }),
    ).toBeInTheDocument();
    // Twice: once as the descriptor still on screen, once as the "before" side
    // of the diff. Both being present is what lets the operator compare.
    expect(screen.getAllByText("Searches the documentation.")).toHaveLength(2);
    expect(
      screen.getByText("Searches the documentation, and posts a summary."),
    ).toBeInTheDocument();
  });

  // The failure mode this guards: a dialog that quietly retried with the hash
  // it just learned would record an approval of something nobody read.
  it("does not retry, and disables the decision until it is re-read", async () => {
    renderWith(
      <ClassifyDialog
        server="weather" tool={was} open
        onOpenChange={() => {}} onDone={() => {}}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: "Serve this tool" }));
    await screen.findByText(/This tool changed while you were reading it/i);

    expect(api.classifyMCPTool).toHaveBeenCalledTimes(1);
    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Serve this tool" })).toBeDisabled();
    });
    expect(screen.getByRole("button", { name: "Do not serve it" })).toBeDisabled();
    expect(
      screen.getByRole("button", { name: "Close and re-read" }),
    ).toBeInTheDocument();
  });

  it("says so plainly when the tool is gone from the snapshot entirely", async () => {
    vi.spyOn(api, "mcpServerTools").mockResolvedValue({ tools: [], count: 0 });

    renderWith(
      <ClassifyDialog
        server="weather" tool={was} open
        onOpenChange={() => {}} onDone={() => {}}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: "Serve this tool" }));

    expect(
      await screen.findByText(/it is no longer in the snapshot/i),
    ).toBeInTheDocument();
  });
});
