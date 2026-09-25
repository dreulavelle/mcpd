import { describe, expect, it } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ToolCall } from "@/lib/api";
import { renderWith } from "@/test/render";
import { CallDetail } from "./call-detail";

const failed: ToolCall = {
  id: 7, at: "2026-09-25T16:00:00Z", principal: "svc:chatgpt", plugin: "graylog",
  tool: "search_messages", outcome: "error", duration_us: 1200,
  correlation_id: "c0ffee", reason: "graylog answered 503: the search backend is unavailable",
};

// A call list said "failed" and nothing else. Opening one says why, in the
// words the assistant was given, with the correlation id and a way into the
// logs under Technical details.
describe("a failed call's detail", () => {
  it("quotes the reason the assistant was given", async () => {
    renderWith(<CallDetail call={failed} />);
    expect(screen.getByText(/The tool ran and failed/)).toBeInTheDocument();
    expect(screen.getByText(failed.reason!)).toBeInTheDocument();
    await userEvent.click(screen.getByText("Technical details"));
    expect(screen.getByRole("link", { name: "Find it in the logs" }))
      .toHaveAttribute("href", "/logs?q=c0ffee");
  });

  it("says when no reason was kept, rather than showing nothing", () => {
    renderWith(<CallDetail call={{ ...failed, reason: undefined }} />);
    expect(screen.getByText(/No reason was kept for this call/)).toBeInTheDocument();
  });

  it("says a refusal is a refusal", () => {
    renderWith(<CallDetail call={{ ...failed, outcome: "denied", reason: "not authorized for graylog_search_messages" }} />);
    expect(screen.getByText(/Refused before it ran/)).toBeInTheDocument();
  });
});
