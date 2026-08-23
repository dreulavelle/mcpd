import { beforeEach, describe, expect, it } from "vitest";
import { consumeSSOOutcome, resetSSOOutcome, takeSSOOutcome } from "./sso";

/**
 * A refused provider round trip has nowhere to say so but the address bar, and
 * a message that stays there comes back on every refresh — describing a round
 * trip that finished ten minutes ago.
 */
describe("the outcome a provider round trip leaves behind", () => {
  beforeEach(() => {
    resetSSOOutcome();
    window.history.replaceState(null, "", "/");
  });

  it("says nothing when nothing came back refused", () => {
    expect(takeSSOOutcome()).toBe("");
  });

  it("turns a code into the sentence written for it", () => {
    window.history.replaceState(null, "", "/?sso_error=address_taken");
    expect(takeSSOOutcome()).toMatch(/already uses that email address/i);
  });

  // A code this build does not know still has to say something, and the
  // honest fallback is that the provider did not finish.
  it("falls back for a code it does not recognise", () => {
    window.history.replaceState(null, "", "/?sso_error=something-new");
    expect(takeSSOOutcome()).toMatch(/didn't finish/i);
  });

  it("removes the parameter and keeps the rest of the address", () => {
    window.history.replaceState(null, "", "/profile?sso_error=state&tab=links");
    takeSSOOutcome();
    expect(window.location.pathname).toBe("/profile");
    expect(window.location.search).toBe("?tab=links");
  });

  it("drops the query entirely when that was the only parameter", () => {
    window.history.replaceState(null, "", "/profile?sso_error=state");
    takeSSOOutcome();
    expect(window.location.search).toBe("");
  });

  // App reads it before React renders, and the screen that shows it renders
  // later. The answer has to survive that gap, and the parameter must not be
  // read twice from an address bar it has already been removed from.
  it("gives the same answer to a second reader", () => {
    window.history.replaceState(null, "", "/profile?sso_error=already_linked");
    const first = takeSSOOutcome();
    expect(takeSSOOutcome()).toBe(first);
  });

  it("is spent once a screen has shown it", () => {
    window.history.replaceState(null, "", "/profile?sso_error=already_linked");
    takeSSOOutcome();
    expect(consumeSSOOutcome()).toMatch(/already linked/i);
    expect(consumeSSOOutcome()).toBe("");
  });
});
