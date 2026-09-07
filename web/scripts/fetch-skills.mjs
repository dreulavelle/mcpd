/**
 * Refreshes the Skills snapshot from skills.sh.
 *
 * Runs in CI, never in the binary. mcpd sits on somebody else's hardware, so
 * the dashboard reads a file committed to this repository rather than calling
 * a ranking service from a customer's host: the page costs no outbound traffic
 * they did not ask for, and the list that shipped is the list in git.
 *
 * The numbers arrive in each leaderboard page's streamed payload rather than
 * from an endpoint. skills.sh does have an API, and it is deliberately not
 * used: it accepts only a Vercel OIDC token, which Vercel mints for its own
 * workloads, so reaching it would mean whoever maintains this repository first
 * keeping a Vercel project alive to hold a credential. It also carries no
 * weekly history and no official flag -- the two things every trend line and
 * badge on the page is drawn from -- so it would cost setup and lose detail.
 * Anyone wanting the full catalogue is better sent to skills.sh itself, which
 * the page links to.
 *
 * That makes this a scrape, and a scrape breaks quietly, so every failure here
 * is loud: a run that cannot find what it expects writes nothing and leaves
 * the committed snapshot standing.
 */
import { writeFileSync } from "node:fs";
import { argv } from "node:process";

const ORIGIN = "https://www.skills.sh";

/**
 * How many rows a board keeps. The pages ship 600; this stops short of that
 * because the last hundred are noise nobody scrolls to, and every row is bytes
 * in a bundle compiled into the binary.
 */
const KEEP = 500;

/**
 * The three boards, which rank different things and count different windows.
 * `installs` is all-time on one, the last day's on the next and today's on the
 * last; nothing downstream may treat them as the same number.
 */
const BOARDS = [
  { key: "top", path: "/" },
  { key: "trending", path: "/trending" },
  { key: "hot", path: "/hot" },
];

/**
 * Decodes the flight payload back into ordinary text.
 *
 * Each chunk arrives as `self.__next_f.push([1,"..."])` where the second
 * element is a JS string literal. Handing that literal to JSON.parse is what
 * unescapes it -- doing it by hand means reimplementing string escapes, and
 * the first `\n` inside a description breaks it.
 */
function decodeFlight(html) {
  const chunks = [];
  const opener = 'self.__next_f.push([1,"';
  for (let at = html.indexOf(opener); at >= 0; at = html.indexOf(opener, at + 1)) {
    const from = at + opener.length - 1; // the opening quote
    let i = from + 1;
    for (; i < html.length; i++) {
      if (html[i] === "\\") { i++; continue; }
      if (html[i] === '"') break;
    }
    try {
      chunks.push(JSON.parse(html.slice(from, i + 1)));
    } catch {
      // A chunk that will not parse is a chunk we do not need; the marker
      // check below is what decides whether the run actually failed.
    }
  }
  return chunks.join("");
}

/** Pulls one JSON array out of decoded text by balancing brackets. */
function arrayAfter(text, marker) {
  const at = text.indexOf(marker);
  if (at < 0) throw new Error(`no ${marker} found -- the page shape changed`);

  const from = text.indexOf("[", at);
  let depth = 0, inString = false, end = -1;
  for (let i = from; i < text.length; i++) {
    const c = text[i];
    if (inString) {
      if (c === "\\") i++;
      else if (c === '"') inString = false;
      continue;
    }
    if (c === '"') inString = true;
    else if (c === "[") depth++;
    else if (c === "]" && --depth === 0) { end = i + 1; break; }
  }
  if (end < 0) throw new Error(`${marker} array never closes`);
  return JSON.parse(text.slice(from, end));
}

/** Keeps the fields the page draws, and drops a row missing any of them. */
function clean(raw) {
  const rows = [];
  for (const s of raw) {
    if (typeof s.source !== "string" || typeof s.skillId !== "string") continue;
    if (typeof s.installs !== "number") continue;
    // A fork republishing somebody else's skill is noise in a ranking of what
    // people install, and skills.sh has already worked out which those are.
    if (s.isDuplicate) continue;

    const row = {
      source: s.source,
      id: s.skillId,
      name: typeof s.name === "string" ? s.name : s.skillId,
      installs: s.installs,
    };
    const weekly = s.weeklyInstalls;
    if (Array.isArray(weekly) && weekly.length >= 2 && weekly.every((n) => typeof n === "number")) {
      row.weekly = weekly;
    }
    if (typeof s.installsYesterday === "number") row.yesterday = s.installsYesterday;
    if (typeof s.change === "number") row.change = s.change;
    if (s.isOfficial) row.official = true;
    rows.push(row);
  }
  return rows;
}

/** A whole board from the leaderboard page it is drawn on. */
async function fromPage(path) {
  const response = await fetch(ORIGIN + path, {
    headers: { "user-agent": "mcpd-skills-snapshot (+https://github.com/spoked/mcpd)" },
  });
  if (!response.ok) throw new Error(`${ORIGIN}${path} answered ${response.status}`);
  return clean(arrayAfter(decodeFlight(await response.text()), '"initialSkills":'));
}

const boards = {};
for (const { key, path } of BOARDS) {
  const rows = await fromPage(path);
  if (rows.length < KEEP) {
    throw new Error(`${path} gave ${rows.length} usable rows, wanted ${KEEP}`);
  }
  // Each board arrives already ranked by what it ranks on. Re-sorting here
  // would mean guessing that rule, so the order that came back is the order.
  boards[key] = rows.slice(0, KEEP);
  console.log(`${path} -> ${boards[key].length} rows`);
}

const out = argv[2] ?? new URL("../src/data/skills.json", import.meta.url).pathname;
writeFileSync(out, JSON.stringify({
  // Whole days only. A timestamp to the second rewrites this file on every
  // run and turns a no-change refresh into a diff.
  captured: new Date().toISOString().slice(0, 10),
  source: ORIGIN,
  boards,
}, null, 2) + "\n");
console.log(`wrote ${out}`);
