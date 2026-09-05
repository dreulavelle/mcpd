import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import { principalWords } from "@/lib/format";
import { useCan } from "@/lib/session";

/**
 * Who did something, named the way a person would name them.
 *
 * Three sources, in order. The server already resolves accounts for every
 * session -- `requested_by_name` and `approved_by_name` on the operation --
 * so a person's chosen name arrives with the record and costs nothing. It
 * falls back to the identifier itself, which is why a name equal to the
 * identifier is treated as no answer rather than as an answer.
 *
 * Keys are the gap: nothing server-side resolves `key:…`, so that one lookup
 * stays here, behind `access:read`. Everything else lands on
 * `principalWords`, which already has words for every caller and makes no
 * request at all.
 *
 * A hook rather than a component, and one per page: a `<Principal>` drawn once
 * per row would be one request per row.
 */
export function usePrincipalNames(): (actor: string, resolved?: string) => string {
  const mayRead = useCan("access:read");
  const [keys, setKeys] = useState<Map<string, string>>(() => new Map());

  // Once, not polled: a key's name does not change while a page is open.
  useEffect(() => {
    if (!mayRead) return;
    let live = true;

    api.keys().then(
      (page) => {
        const found = new Map<string, string>();
        for (const k of page.keys ?? []) {
          const name = k.name?.trim();
          // "the api key key" is what appending the noun to a name that
          // already ends in it produces.
          if (name) found.set(`key:${k.id}`, /key$/i.test(name) ? `the ${name}` : `the ${name} key`);
        }
        if (live && found.size > 0) setKeys(found);
      },
      // A name is a convenience. Failing to fetch one leaves the words that
      // were already fit to show.
      () => undefined,
    );

    return () => { live = false; };
  }, [mayRead]);

  return useCallback(
    (actor: string, resolved?: string) => {
      const server = resolved?.trim();
      if (server && server !== actor) return server;
      return keys.get(actor) ?? principalWords(actor);
    },
    [keys],
  );
}
