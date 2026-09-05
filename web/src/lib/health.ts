/**
 * What a plugin's health message carries, taken apart for display.
 *
 * A plugin writes one string: a status code from the upstream where there
 * was one, a reference the upstream's support can look up, and a diagnosis
 * that may run to a paragraph. A table wants the number; the plugin's page
 * wants the paragraph laid out. Neither should have to parse it twice.
 */
export interface Health {
  /** The upstream's HTTP status, when the message names one. */
  status?: number;
  /** A reference id the message quotes, when it does. */
  reference?: string;
  /** The first sentence. */
  title: string;
  /** Everything after it, or "". */
  body: string;
}

/**
 * An HTTP status as words a person can act on.
 *
 * A chip reading `HTTP 401` tells an ops manager nothing; "Refused" tells them
 * the far end said no. The number is still worth keeping, so every call site
 * puts it in a `title` rather than dropping it.
 *
 * Anything under 400 has its own answer. A plugin only quotes a status when
 * its check went wrong, so a 200 in that sentence means the far end answered
 * and the body was not what it should have been -- Observium does this -- and
 * a 3xx means it pointed somewhere the plugin did not follow. Neither is a
 * refusal, which is what falling through used to call them.
 */
export function statusWords(status: number): string {
  if (status < 400) return "Bad answer";
  if (status === 404) return "Not found";
  if (status === 408 || status === 504 || status === 524) return "No answer";
  // Being told to slow down is not being told no: the same call later works.
  if (status === 429) return "Rate limited";
  if (status >= 500) return "Their side failed";
  return "Refused";
}

/**
 * The tone that goes with statusWords, in one place so two pages showing the
 * same chip cannot disagree about it.
 *
 * Never "good". This is only reached from a health message, and a plugin only
 * writes one of those when something went wrong -- a green chip beside a
 * plugin that is not serving was the status being read on its own rather than
 * as part of the sentence quoting it.
 */
export function statusTone(status: number): "attention" | "problem" {
  // Answered, badly; asked to wait; or did not answer in time. Each is worth
  // looking at and none is the far end refusing.
  if (status < 400 || status === 408 || status === 429) return "attention";
  return "problem";
}

export function parseHealth(message: string): Health {
  const text = message.trim();
  const status = /\bHTTP (\d{3})\b/.exec(text);
  const reference = /\breference[:\s]+([A-Za-z0-9][\w-]{5,})/i.exec(text);
  const m = /^(.+?[.!?])(\s+|$)/.exec(text);
  const title = m ? m[1]! : text;
  const body = m ? text.slice(m[0].length).trim() : "";
  return {
    status: status ? Number(status[1]) : undefined,
    reference: reference ? reference[1] : undefined,
    title,
    body,
  };
}
