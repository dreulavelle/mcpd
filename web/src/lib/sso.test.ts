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
    expect(takeSSOOutcome()).toEqual({ code: "", message: "" });
  });

  it("turns a code into the sentence written for it", () => {
    window.history.replaceState(null, "", "/?sso_error=address_taken");
    const { code, message } = takeSSOOutcome();
    expect(code).toBe("address_taken");
    expect(message).toMatch(/already uses that email address/i);
  });

  // A code this build does not know still has to say something, and the
  // honest fallback is that the provider did not finish.
  it("falls back for a code it does not recognise", () => {
    window.history.replaceState(null, "", "/?sso_error=something-new");
    expect(takeSSOOutcome().message).toMatch(/didn't finish/i);
  });

  // `link_password` is the one code that is not a refusal: it says a screen is
  // waiting. A sentence for it would put a red notice above a form that is
  // working exactly as intended.
  it("gives link_password a code and no sentence", () => {
    window.history.replaceState(null, "", "/?sso_error=link_password");
    expect(takeSSOOutcome()).toEqual({ code: "link_password", message: "" });
  });

  // The account it names has no password, so it cannot be told to use one.
  it("tells a passwordless account to use the provider it has", () => {
    window.history.replaceState(null, "", "/?sso_error=address_taken");
    expect(takeSSOOutcome().message).toMatch(/signs in with a different provider/i);
  });

  // An invited account has never been signed in to and has no password, so
  // both of the other sentences would name a credential and a page that do not
  // exist for the person reading them.
  it("sends an invited person to the provider they were invited with", () => {
    window.history.replaceState(null, "", "/?sso_error=invite_other_provider");
    expect(takeSSOOutcome().message).toMatch(/invited to sign in with a different provider/i);
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
    expect(takeSSOOutcome()).toEqual(first);
  });

  it("is spent once a screen has shown it", () => {
    window.history.replaceState(null, "", "/profile?sso_error=already_linked");
    takeSSOOutcome();
    expect(consumeSSOOutcome()).toMatch(/already linked/i);
    expect(consumeSSOOutcome()).toBe("");
  });

  // The code outlives the sentence. Which screen to draw is not a thing to be
  // shown once and forgotten: the connect screen has to survive its own
  // renders, and it is spent by finishing rather than by being read.
  it("keeps the code after the sentence has been shown", () => {
    window.history.replaceState(null, "", "/?sso_error=already_linked");
    consumeSSOOutcome();
    expect(takeSSOOutcome().code).toBe("already_linked");
  });
});
