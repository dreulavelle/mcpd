# Smithery registry fixtures

Verbatim responses from <https://registry.smithery.ai>, captured 2026-08-23.
Every server object is copied byte for byte from what the API returned; the
only edits are which objects were kept and, in `list-page1.json`, a
`pagination` block restored to the real page-size-100 values so the fixture
describes the window it was cut from.

| file | what it is |
|---|---|
| `list-page1.json` | eight rows of `GET /servers?pageSize=100&page=1` |
| `list-page-beyond-cap.json` | `GET /servers?pageSize=100&page=6` — empty, with `totalPages` still 5 |
| `search-postgres.json` | five rows of `GET /servers?pageSize=20&page=1&q=postgres`, all of them outside the browsable window |
| `detail-brave.json` | `GET /servers/brave` |
| `detail-not-remote.json` | `GET /servers/gautamgb/mcpindex` |

The rows in `list-page1.json` were chosen to cover the branches the
translation has:

| entry | why it is here |
|---|---|
| `brave` | hosted and deployed, a bare qualifiedName |
| `gmail` | hosted and deployed, one of Smithery's own |
| `onesignal/onesignal` | hosted and deployed, a qualifiedName carrying a slash — the case the document name and the gateway path both have to survive |
| `gautamgb/mcpindex` | `remote: false` — something to run yourself, listed with a reason and not offered |
| `exa`, `googlesheets`, `subwayinfo`, `theagenttimes/news` | ordinary hosted rows, so a page is a page rather than four special cases |

## What was measured, and when

Captured 2026-08-23 against the live API.

**The listing stops at 500 rows.** `totalCount` was 10,498 and `totalPages` 5
at `pageSize=100`; page 6 came back with an empty `servers` array. At
`pageSize=3` `totalPages` was 167, which is the same 500 rows. So the cap is on
rows and does not move with the page size. `search-postgres.json` is the
evidence for the other half of that: 19 of its 20 hits were servers no listing
page returns, so `q=` genuinely queries upstream and is the only route to the
other ten thousand.

**The listing repeats itself.** The 500 rows of the browse window held 269
distinct `qualifiedName`s; pages one and two alone shared 39. Page one is
byte-stable when refetched, so this is not jitter — Smithery orders by
popularity and the order is not a total one. `fetchWindow` deduplicates for
this reason.

**`remote: true` with `isDeployed: false` was not observed.** Across 2,175
distinct servers reached by search, no row had one without the other, while 178
rows had `remote: false` with `isDeployed: true`. So `remote` is the flag that
currently discriminates. Both are still checked, because they are two claims
and the pair is what means "there is an address behind this right now"; the
test for the undeployed branch builds the row rather than citing a fixture,
since there is no honest fixture to cite.

**`remote=true` is a real upstream filter** (`totalCount` 6,684, against 10,498
unfiltered) and is deliberately not used. Entries this host cannot reach are
listed with the reason rather than filtered out, which is the rule the other
sources follow.

## Terms

`registry.smithery.ai/robots.txt` publishes `User-agent: *`, `Allow: /`, and
`Content-Signal: search=yes,ai-train=no,use=reference`. `search=yes` is that
signal's own wording for "building a search index and providing search
results", which is what this source does and the whole of it.
