import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import { principalWords } from "@/lib/format";
import { useCan } from "@/lib/session";

/**
 * Who did something, named the way a person would name them.
 *
 * `principalWords` alone gets every caller to something readable with no
 * request at all -- "ChatGPT (work)", "a standing rule", the local part of an
 * address. This adds the one thing it cannot know: the display name somebody
 * chose, and what a key is called. That needs the accounts, which only
 * `access:read` may have, so most sessions never make the call and read the
 * fallback instead. Nothing here is access control; the server authorises
 * every request again.
 *
 * A hook rather than a component, and one per page: a `<Principal>` drawn
 * once per row would be one pair of requests per row.
 */
export function usePrincipalNames(): (actor: string) => string {
  const mayRead = useCan("access:read");
  const [names, setNames] = useState<Map<string, string>>(() => new Map());

  // Once, not polled: a display name does not change while a page is open.
  useEffect(() => {
    if (!mayRead) return;
    let live = true;

    // Either half failing leaves the other half's names in place, and anything
    // unresolved falls back to words that were already fit to show.
    void Promise.allSettled([api.users(), api.keys()]).then(([users, keys]) => {
      const found = new Map<string, string>();
      if (users.status === "fulfilled") {
        for (const u of users.value.users ?? []) {
          const name = u.name?.trim();
          if (name) found.set(`user:${u.email}`, name);
        }
      }
      if (keys.status === "fulfilled") {
        for (const k of keys.value.keys ?? []) {
          const name = k.name?.trim();
          if (name) found.set(`key:${k.id}`, `the ${name} key`);
        }
      }
      if (live && found.size > 0) setNames(found);
    });

    return () => { live = false; };
  }, [mayRead]);

  return useCallback(
    (actor: string) => names.get(actor) ?? principalWords(actor),
    [names],
  );
}
