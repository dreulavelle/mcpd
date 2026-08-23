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
  address_taken:
    "An account here already uses that email address. Sign in with your password, "
    + "then link the provider from your profile — mcpd won't hand an account over "
    + "on the strength of a matching address.",
  registration_closed: "This host isn't accepting new accounts.",
  domain: "Accounts here are limited to particular email domains.",
  unclaimed: "Nobody has claimed this host yet. Create the first account below.",
  already_linked: "That provider account is already linked to an account here.",
  disabled: "That account has been switched off.",
  wrong_account: "You signed in as somebody else while that was in progress. Try again.",
};

// Held here rather than in the URL, because the parameter is stripped as it is
// read: a refresh should not bring back a message about a round trip that
// finished a while ago. Which screen shows it depends on whether the person
// ended up signed in, and that is not known when the URL is read.
let pending = "";
let taken = false;

/**
 * Reads the outcome out of the address bar and removes it. Idempotent: the
 * first call does the work and every later one gets the same answer.
 */
export function takeSSOOutcome(): string {
  if (taken || typeof window === "undefined") return pending;
  taken = true;

  const params = new URLSearchParams(window.location.search);
  const code = params.get("sso_error");
  if (!code) return "";

  params.delete("sso_error");
  const query = params.toString();
  window.history.replaceState(
    null, "", window.location.pathname + (query ? `?${query}` : ""));

  pending = SSO_MESSAGES[code] ?? SSO_MESSAGES.provider!;
  return pending;
}

/**
 * Takes the outcome for a screen that is about to render it, so navigating
 * away and back does not show it again.
 */
export function consumeSSOOutcome(): string {
  const message = takeSSOOutcome();
  pending = "";
  return message;
}

/** Forgets any held outcome. Exists so a test starts from a clean page. */
export function resetSSOOutcome() {
  pending = "";
  taken = false;
}
