/**
 * Ranking for the command palette: a substring beats a subsequence, and a
 * match at the start of a word beats one in the middle. Small enough to read
 * in one sitting, which is the whole reason it is not a library.
 *
 * A query is read as words rather than as one string, because people type the
 * words they remember in the order they think of them. Scoring the whole query
 * at once made word order load-bearing: "settings keys" scraped the bottom
 * bucket -- every character of it does appear in "Settings › Keys" in order,
 * with the space landing in the gap -- and "keys settings" scored nothing at
 * all. Neither is a near miss to somebody who typed it.
 */
export function score(haystack: string, query: string): number {
  const h = haystack.toLowerCase();
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);

  // Nothing typed matches everything, weakly.
  if (words.length === 0) return 1;

  let total = 0;
  for (const word of words) {
    const s = scoreWord(h, word);
    // Every word has to be in there somewhere. Anything else lets a second
    // word widen the result instead of narrowing it, which is the opposite of
    // what typing more is for.
    if (s === 0) return 0;
    total += s;
  }

  // The mean, so a query is scored on how well it matched rather than on how
  // many words it had, and a two-word query stays comparable with a one-word
  // one when both are ranked in the same list.
  return Math.round(total / words.length);
}

/** One word against the whole haystack. */
function scoreWord(h: string, word: string): number {
  const at = h.indexOf(word);
  if (at === 0) return 100;
  if (at > 0) return h[at - 1] === " " || h[at - 1] === "/" || h[at - 1] === "_" ? 80 : 60;
  // Every character of the word in order, with gaps.
  let i = 0;
  for (const ch of h) if (ch === word[i]) i++;
  return i === word.length ? 20 : 0;
}
