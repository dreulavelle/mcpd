import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import {
  isOpenAIReason, OpenAIPermissionDialog,
} from "@/components/openai-permission";

describe("OpenAI refusals", () => {
  it("recognises the reasons the server sends, and nothing else", () => {
    expect(isOpenAIReason("openai_tunnels_manage_required")).toBe(true);
    expect(isOpenAIReason("openai_admin_key_rejected")).toBe(true);
    expect(isOpenAIReason("openai_org_id_rejected")).toBe(true);
    // A refusal this page has no explanation for must fall through to a toast
    // rather than open an empty dialog.
    expect(isOpenAIReason("upstream_refused")).toBe(false);
    expect(isOpenAIReason("tunnel_failed")).toBe(false);
  });

  // The fact that decides what somebody does next is that another key will not
  // help. It led the old message too, but a toast flattened the whole thing
  // into one line and buried it.
  it("leads with the reason a new key will not help", () => {
    render(
      <OpenAIPermissionDialog
        reason="openai_tunnels_manage_required"
        detail="That admin key is not allowed to manage tunnels."
        onClose={() => {}}
      />,
    );
    expect(screen.getByText(/Making another key will not help/)).toBeInTheDocument();
    // The permission named the way OpenAI's own settings name it. It appears
    // twice on purpose: once as the step, once inside the request to forward.
    expect(screen.getAllByText(/Tunnels: Read and Manage/).length).toBeGreaterThan(0);
    // And a link straight to where it is granted, rather than a path to read.
    const link = screen.getByRole("link", { name: /Organization → People → Roles/ });
    expect(link).toHaveAttribute(
      "href", "https://platform.openai.com/settings/organization/people/roles");
  });

  // The reader is frequently not the person who can grant it, so the request
  // is pre-written rather than left for them to compose.
  it("offers a request to hand to whoever can grant it", () => {
    render(
      <OpenAIPermissionDialog reason="openai_tunnels_manage_required" onClose={() => {}} />,
    );
    expect(screen.getByText(/Send this to your organisation owner/)).toBeInTheDocument();
    expect(screen.getByText(/api\.organization\.tunnel\.write/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Copy request" })).toBeInTheDocument();
  });

  it("explains an admin key confused with a runtime key", () => {
    render(<OpenAIPermissionDialog reason="openai_admin_key_rejected" onClose={() => {}} />);
    expect(screen.getByText(/not the same thing as the runtime key/)).toBeInTheDocument();
  });

  it("says what an organization ID looks like", () => {
    render(<OpenAIPermissionDialog reason="openai_org_id_rejected" onClose={() => {}} />);
    expect(screen.getByText(/begins with org_/)).toBeInTheDocument();
  });
});
