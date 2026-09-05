/**
 * What a provider round trip left behind in the address bar.
 *
 * The callback is a browser redirect, so a refusal has nowhere to go but the
 * URL. The server sends a short code rather than its own error text: what
 * comes back from a provider is a third party's prose, and it would end up in
 * an address bar, a browser history and whatever sits in front of the host.
 * The sentences live here, where they can be written for the person reading
 * them.
 */
export const SSO_MESSAGES: Record<string, string> = {
  state: "That sign-in link had expired, or had already been used. Try again.",
  provider: "The provider didn't finish the sign-in. Try again.",
  no_email: "That account has no verified email address, so mcpd can't use it.",
  // An account with a password gets the connect screen instead of this, so
  // what is left here is one that signs in through some other provider. It has
  // no password to be told to use: linking is an act by the signed-in account,
  // from the profile page.
  address_taken:
    "An account here already uses that email address, and it signs in with a "
    + "different provider. Sign in with that one, then add this provider from "
    + "your profile.",
  registration_closed: "This host isn't accepting new accounts.",
  domain: "Accounts here are limited to particular email domains.",
  unclaimed: "Nobody has claimed this host yet. Create the first account below.",
  already_linked: "That provider account is already linked to an account here.",
  disabled: "That account has been switched off.",
  wrong_account: "You signed in as somebody else while that was in progress. Try again.",
  other_identity:
    "That account is already connected to a different account at this provider. "
    + "Sign in with the one it uses, or ask an administrator.",
  invite_expired:
    "That invitation has run out. Ask an administrator to invite you again.",
};

/**
 * Codes that are not a refusal, and so have no sentence.
 *
 * `link_password` says a screen is waiting rather than that something went
 * wrong: the address belongs to an account here, and the offer to connect this
 * provider to it is held against a cookie. Showing a message for it would put
 * a red notice above a form that is working exactly as intended.
 */
const SILENT = new Set(["link_password"]);

/** A finished round trip: the code the server sent, and what to say about it. */
export interface SSOOutcome {
  code: string;
  /** Empty for a code that is not a refusal. */
  message: string;
}

// Held here rather than in the URL, because the parameter is stripped as it is
// read: a refresh should not bring back a message about a round trip that
// finished a while ago. Which screen shows it depends on whether the person
// ended up signed in, and that is not known when the URL is read.
let pending: SSOOutcome = { code: "", message: "" };
let taken = false;

/**
 * Reads the outcome out of the address bar and removes it. Idempotent: the
 * first call does the work and every later one gets the same answer.
 *
 * The code comes back beside the sentence because one of them now decides
 * which screen to draw rather than what to say above it.
 */
export function takeSSOOutcome(): SSOOutcome {
  if (taken || typeof window === "undefined") return pending;
  taken = true;

  const params = new URLSearchParams(window.location.search);
  const code = params.get("sso_error");
  if (!code) return pending;

  params.delete("sso_error");
  const query = params.toString();
  window.history.replaceState(
    null, "", window.location.pathname + (query ? `?${query}` : ""));

  pending = {
    code,
    message: SILENT.has(code) ? "" : SSO_MESSAGES[code] ?? SSO_MESSAGES.provider!,
  };
  return pending;
}

/**
 * Takes the sentence for a screen that is about to render it, so navigating
 * away and back does not show it again.
 *
 * The code is not cleared with it. Which screen to draw is not a thing to be
 * shown once and forgotten -- the connect screen has to survive its own
 * renders -- and it is spent by the screen finishing rather than by being
 * read.
 */
export function consumeSSOOutcome(): string {
  const { message } = takeSSOOutcome();
  pending = { ...pending, message: "" };
  return message;
}

/** Forgets any held outcome. Exists so a test starts from a clean page. */
export function resetSSOOutcome() {
  pending = { code: "", message: "" };
  taken = false;
}
