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
