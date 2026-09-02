import { describe, expect, it } from "vitest";
import { renderWith, sessionFor } from "@/test/render";
import { useCan } from "./session";

function Probe() {
  return (
    <ul>
      {(["read", "propose", "approve", "admin"] as const).map((c) => (
        <li key={c}>{c}: {useCan(c) ? "yes" : "no"}</li>
      ))}
    </ul>
  );
}

describe("what a session may do", () => {
  // A group can take capabilities away from a role, and only the server
  // knows by how much. The console used to derive the set from the role,
  // which left a restricted administrator looking at controls every one of
  // which the server refused.
  it("is what the server reports, not what the role implies", () => {
    renderWith(<Probe />, {
      session: sessionFor("admin", { capabilities: ["read"] }),
    });
    expect(document.body.textContent).toContain("read: yes");
    expect(document.body.textContent).toContain("approve: no");
    expect(document.body.textContent).toContain("admin: no");
  });

  // An empty list is a real answer -- a group that permits nothing -- and
  // must not fall back to the role.
  it("holds nothing when the server says nothing", () => {
    renderWith(<Probe />, {
      session: sessionFor("admin", { capabilities: [] }),
    });
    expect(document.body.textContent).not.toContain("yes");
  });

  it("holds nothing when signed out", () => {
    renderWith(<Probe />, { session: null });
    expect(document.body.textContent).not.toContain("yes");
  });
});
