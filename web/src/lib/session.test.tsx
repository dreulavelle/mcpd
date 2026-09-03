import { describe, expect, it } from "vitest";
import { renderWith, sessionFor } from "@/test/render";
import { useCan } from "./session";

function Probe() {
  return (
    <ul>
      {(["approvals:read", "approvals:decide", "access:read", "access:write"] as const).map((p) => (
        <li key={p}>{p}: {useCan(p) ? "yes" : "no"}</li>
      ))}
    </ul>
  );
}

describe("what a session may do", () => {
  // A group can take permissions away from a role, and only the server
  // knows by how much. The console used to derive the set from the role,
  // which left a restricted administrator looking at controls every one of
  // which the server refused.
  it("is what the server reports, not what the role implies", () => {
    renderWith(<Probe />, {
      session: sessionFor("admin", { permissions: ["approvals:read"] }),
    });
    expect(document.body.textContent).toContain("approvals:read: yes");
    expect(document.body.textContent).toContain("approvals:decide: no");
    expect(document.body.textContent).toContain("access:write: no");
  });

  // An empty list is a real answer -- a group that permits nothing -- and
  // must not fall back to the role.
  it("holds nothing when the server says nothing", () => {
    renderWith(<Probe />, {
      session: sessionFor("admin", { permissions: [] }),
    });
    expect(document.body.textContent).not.toContain("yes");
  });

  it("holds nothing when signed out", () => {
    renderWith(<Probe />, { session: null });
    expect(document.body.textContent).not.toContain("yes");
  });
});
