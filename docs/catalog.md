# Your own catalogue

Settings → General → **Your own catalogue**.

The public catalogues answer *what exists in the world*. They cannot answer the
question you actually have to defend, which is *what are we allowed to run
here* — so mcpd can read a list you keep yourself, wherever you keep things
under review.

A list in a git repository has review, history and blame already attached. That
is the whole point of it; the fetching is incidental.

## Pointing at one

Set **Archive address** to a gzipped tar archive holding `server.json`
documents:

| Host | Address |
|---|---|
| GitHub | `https://api.github.com/repos/{owner}/{repo}/tarball/{ref}` |
| GitLab | `https://gitlab.com/{owner}/{repo}/-/archive/{ref}/{repo}-{ref}.tar.gz` |
| Gitea / Forgejo | `https://{host}/{owner}/{repo}/archive/{ref}.tar.gz` |
| Anything else | Any URL serving a `.tar.gz` |

A tarball rather than the git protocol, and no git dependency: every host
people use serves one over HTTPS, and implementing enough of git to clone a
repository in order to read a directory of JSON files would be a large
dependency bought for nothing.

For a private repository, set **Token**. It is sent as
`Authorization: Bearer`, and **dropped if the address redirects to another
host** — GitHub's tarball endpoint answers 302 with a signed URL on
`codeload.github.com`, that URL carries its own authority, and your token has
no business travelling to whatever a redirect names.

## What goes in the repository

`server.json` documents — the format mcpd already parses, validates and
imports. An mcpd-specific catalogue schema would mean maintaining your list in
a format only this program reads, and a second parser to keep in step with the
first.

```
your-catalogue/
├── README.md            ← ignored
├── renovate.json        ← ignored: JSON, but not a server document
└── servers/
    ├── weather.json     ← an entry
    └── tickets.json     ← an entry
```

Anything that is not a server document is skipped rather than refused. A
repository holds a README, a licence and a workflow beside its documents, and a
catalogue that failed to load because of a lint config would be useless.

Entries appear in the Marketplace beside the public ones, described the same
way — including whether importing one will ask you for a credential.

## Preference

Your list comes **first**, ahead of the official registry.

That is a statement about trust rather than freshness. Preference order decides
which copy of a server survives deduplication when several catalogues know the
same name, and a document somebody here put under review beats every third
party's description of the same thing — including the publisher's own
registration, because the question your list answers is not "what exists" but
"what are we allowed to run".

It is also independent of `catalog.enabled`, which governs whether this host
browses the public catalogues at all. Turning those off does not disable your
own list: an operator who switched off the public ones and then wrote their own
has answered that question, and silently disabling the thing they built on
purpose would be unexplainable from the dashboard.

## Refreshing

Read every 6 hours by default, on a schedule. Zero re-reads only on a restart.

The entries are served from memory and never fetched during a browse, so
opening the Marketplace does not depend on your git host being up. A fetch that
fails **leaves the previous list in place** — a host being briefly unreachable
must not empty your allowlist — and the failure is reported rather than
swallowed, so the page can say the list is not being confirmed.

An address you have just pasted is read within a minute rather than at the next
six-hour tick.

## Bounds

The archive is capped at 64 MiB compressed, 32 MiB expanded, and 2000
documents. Nothing is written to disk — the documents are held in memory — so
those are memory limits, and therefore this host's to set rather than the
party supplying the archive's.
