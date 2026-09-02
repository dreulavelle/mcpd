/**
 * Ranking for the command palette: a substring beats a subsequence, and a
 * match at the start of a word beats one in the middle. Small enough to read
 * in one sitting, which is the whole reason it is not a library.
 */
export function score(haystack: string, query: string): number {
  const h = haystack.toLowerCase();
  const q = query.toLowerCase().trim();
  if (q === "") return 1;
  const at = h.indexOf(q);
  if (at === 0) return 100;
  if (at > 0) return h[at - 1] === " " || h[at - 1] === "/" || h[at - 1] === "_" ? 80 : 60;
  // Every character of the query in order, with gaps.
  let i = 0;
  for (const ch of h) if (ch === q[i]) i++;
  return i === q.length ? 20 : 0;
}
