import { describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
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

  // This said the opposite once: that the permission comes from the creator's
  // role and another key would not help. That is wrong -- admin keys carry
  // their own scopes, an owner can issue a key weaker than themselves, and
  // regenerating is OpenAI's own advice. Telling an owner not to do the thing
  // that fixes it is worse than saying nothing.
  it("names both gates, and does not tell an owner a new key is pointless", () => {
    render(
      <OpenAIPermissionDialog
        reason="openai_tunnels_manage_required"
        detail="That admin key is not allowed to manage tunnels."
        onClose={() => {}}
      />,
    );
    // Both gates named, and the key's own scopes first, because that is the
    // one an owner has not already satisfied.
    expect(screen.getByText(/permissions chosen for the key itself/)).toBeInTheDocument();
    expect(screen.getByText(/Restricted without the tunnel permissions/)).toBeInTheDocument();
    expect(screen.getByText(/make the key again/)).toBeInTheDocument();
    // The claim that got this wrong must not come back.
    expect(screen.queryByText(/Making another key will not help/)).toBeNull();
    // Links straight to where each is changed, rather than a path to read.
    expect(screen.getAllByRole("link", { name: /Organization → Admin keys/ })[0])
      .toHaveAttribute("href", "https://platform.openai.com/settings/organization/admin-keys");
    expect(screen.getByRole("link", { name: /Organization → People → Roles/ }))
      .toHaveAttribute("href", "https://platform.openai.com/settings/organization/people/roles");
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

  // The dashboard is served over plain HTTP on purpose, so navigator.clipboard
  // does not exist on any ordinary install reached by LAN address. The button
  // silently did nothing there, which is worse than having no button: the
  // reader cannot tell it from a broken one.
  it("still reports something when the clipboard API is unavailable", () => {
    const clipboard = navigator.clipboard;
    Object.defineProperty(navigator, "clipboard", { value: undefined, configurable: true });
    Object.defineProperty(window, "isSecureContext", { value: false, configurable: true });
    // jsdom implements neither, which is exactly the situation being tested.
    document.execCommand = undefined as unknown as typeof document.execCommand;

    render(
      <OpenAIPermissionDialog reason="openai_tunnels_manage_required" onClose={() => {}} />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Copy request" }));

    // The text is selected and the button says so, rather than pretending.
    expect(screen.getByText(/press .* to copy/i)).toBeInTheDocument();

    Object.defineProperty(navigator, "clipboard", { value: clipboard, configurable: true });
  });

  // The request has to be selectable for the fallback to be usable at all.
  it("puts the request somewhere it can be selected", () => {
    render(
      <OpenAIPermissionDialog reason="openai_tunnels_manage_required" onClose={() => {}} />,
    );
    const box = screen.getByLabelText("Request to send to your organisation owner");
    expect(box).toHaveAttribute("readonly");
    expect((box as HTMLTextAreaElement).value).toMatch(/api\.organization\.tunnel\.write/);
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
