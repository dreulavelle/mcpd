# BookStack

Your knowledge base: search it, read it, and change it.

One instance is one BookStack. Two instances are two knowledge bases — a
staging copy, or a second business's.

## Setting it up

Settings → Plugins → BookStack:

| Field | Where from |
|---|---|
| **Address** | Where BookStack is served: `kb.example.com`, `10.0.0.5:8080`, or a full URL. A bare host is treated as `http`, because these are usually internal. |
| **Token ID** | A BookStack user's profile page → API Tokens. The public half. |
| **Token secret** | The private half, shown once when the token is made. Stored encrypted. |

**The token is the ceiling.** BookStack applies the permissions of the user the
token belongs to, so what this integration can see and change is exactly what
that person could — no more. That is worth choosing deliberately rather than
reaching for an admin token: an instance given an editor's token can write
pages and cannot touch users or roles, and BookStack enforces that rather than
mcpd.

The token pair is sent as one header on every request rather than exchanged for
a session, so revoking it stops every read and every change at once, with no
grace period. It is never logged, never put in a URL, and never returned by a
tool.

## Reading

Twenty-two read tools. The ones worth knowing about first:

**`search_content` is the way in.** It is the only read that matches on what
pages *say* rather than what they are called, and it takes BookStack's own
syntax: `[customer=Acme]` for tagged content, `{updated_after:2026-01-01}` for
recent changes, `"exact phrase"` for a literal string, `-word` to exclude.

**Listings are deliberately shallow, and say so.** `list_pages` returns titles
and locations, never page text — pulling every body to answer "what do we know
about X" would fill a context window with the wrong pages. Every listing
carries a `notes` field saying what it cannot tell you and which tool to call
next. Some of those only appear when they apply: a count of drafts (unpublished
and visible only to their author, which is usually the answer to "I wrote that
up, why can nobody find it"), a pointer to `list_pages` when a book has no
chapters, and what to do when a listing stopped short.

**You can paste a URL.** `get_page`, `get_book`, `get_chapter` and `get_shelf`
take an `id`, a `slug`, or a `url` copied out of a browser. A page URL carries
its book, which matters: **slugs are unique per book, not per instance**, so a
bare slug matching two pages is refused with both named rather than guessed at.
A book link handed to `get_page` is refused rather than resolved to whatever
page shares the slug.

**Listings sort, and the sort is checked here.** Default is `-updated_at`, most
recently changed first, which matters when a listing is truncated: twenty of
147 pages is an answer if they are the twenty most recently touched and noise
otherwise. BookStack silently ignores a sort it does not recognise and answers
200, so an unrecognised one is refused here rather than quietly returning a
different order.

The people and permission reads — `list_users`, `get_user`, `list_roles`,
`get_role`, `get_content_permissions`, `list_recycle_bin`, `list_audit_events`
— are capability-gated to `admin`. Nothing there changes anything; a user
listing is every colleague's email address and a role is the map of what the
knowledge base protects, and those are reads where seeing the answer is itself
the privilege.

## Changing things

Every change is a **mutation**, not a tool: planned, shown in full, approved,
applied exactly once, then confirmed by re-reading the target. Reading happens
straight away; nothing is written until somebody says so.

| | Reversible | Risk |
|---|---|---|
| `page.create` `book.create` `chapter.create` `shelf.create` | yes | low |
| `page.update` | yes | medium |
| `book.update` `chapter.update` `shelf.update` | yes | low |
| `page.delete` `shelf.delete` | yes — recycle bin | medium |
| `book.delete` `chapter.delete` | yes — recycle bin | high |
| `recycle_bin.restore` | yes | low |
| **`recycle_bin.destroy`** | **no** | critical |
| `comment.create` `comment.update` | yes | low |
| **`comment.delete`** | **no** | medium |
| `attachment.create` `attachment.update` | yes | low |
| **`attachment.delete`** | **no** | medium |
| `user.create` `user.update` | yes | high |
| **`user.delete`** | **no** | critical |
| `role.create` | yes | high |
| `role.update` | yes | critical |
| **`role.delete`** | **no** | critical |
| `content_permissions.update` | yes | high |

### Reversible is not documentation

Deleting a shelf, book, chapter or page sends it to the recycle bin, and
`recycle_bin.restore` puts it back. That is a real way back, so those mutations
declare `Reversible: true` honestly.

Everything in bold above destroys. BookStack keeps no revision and no recycle
bin entry for a comment, an attachment, an image, a user or a role, and
emptying the recycle bin is final by definition. Those declare `Reversible:
false`, and **the host refuses to let a standing rule auto-approve an
irreversible mutation however broadly the rule is written** — so those always
wait for a person. There is a test asserting each one is on the side it belongs
on, because getting it wrong is a change that happens with nobody watching.

### Drift

Every entity carries `updated_at`, and a page also carries `revision_count`.
Both go into the preconditions, because a page saved twice within one second
moves its revision count without moving its timestamp.

The host re-plans immediately before executing and compares. A proposal built
against a page somebody has since edited is **refused** rather than applied,
which is the failure that actually happens on a shared knowledge base —
quietly discarding a colleague's edit, rather than anything dramatic.

### What an approver sees

The diff carries the page text itself, old and new, not a summary of it. A
change described as "4,182 characters becomes 4,530" is not something anybody
can meaningfully approve. Past 8KB per side the diff says where it was cut and
how much there was.

Deleting a book or chapter counts the pages that go with it first, because that
number is what changes somebody's mind and it is not visible from the name.
Changing a role's permissions names what is granted and what is taken away
rather than counting them. Setting a shelf's books, a role's permissions, or an
item's role overrides **replaces the whole set** rather than adding to it, and
every diff that does so says as much.

### Two things that are refused rather than diffed

Replacing an **uploaded file** attachment with a link would destroy the file,
so `attachment.update` refuses it and says to delete and re-attach if that is
genuinely the intent.

Sending both `html` and `markdown` for a page is a caller who has not decided
which BookStack should keep.

Writing markdown to a page BookStack holds as WYSIWYG *converts* it and
discards the formatting. That is not refused — sometimes it is what you want —
but it appears in the diff as its own change (`editor: wysiwyg → markdown`) so
it never happens quietly.

## Things this BookStack does that a reader would not expect

**Comments are polymorphic.** A comment carries `commentable_id` and
`commentable_type`, not `page_id`. `filter[page_id]` is accepted, **silently
ignored**, and answers with every comment in the instance — so a listing built
on the old field name is wrong rather than empty. This plugin filters on
`commentable_id`, and there is a live test that would catch it if that changed.

**A comment listing carries no text.** Only `get_comment` returns the `html`.
The listing says so in its notes.

**`created_by` changes shape.** It is a bare id in a listing and an object with
a name in a single read.

**A page written in the WYSIWYG editor has no markdown.** `get_page(format:
markdown)` falls back to HTML and reports `format: html` rather than answering
with an empty body.

**An unknown query parameter is ignored, not refused.** BookStack answers 200
for a filter or sort it does not recognise, which is why this plugin validates
sort orders itself.

## The endpoint guard

`transport.go` holds the complete list of paths this integration may reach,
default-deny, checked on the transport rather than at the call sites — so a
redirect nobody wrote is checked too, and the address is pinned so nothing
leaves for another host carrying the token.

It guarantees something narrower than in the read-only integrations, and the
difference is worth stating. It cannot be "read-only" here: writing is the
point. What it guarantees is that no request reaches an endpoint nobody meant
it to — a mistyped path, a URL built from a caller's string. **Approval guards
changes; this guards URLs.** Both are needed and neither covers the other.

## What is not here

* **File and image uploads.** `attachment.create` attaches links. A tool call
  cannot carry a file, so uploading one is done in BookStack.
* **ZIP imports.** They are a three-step upload-then-run flow around a file
  this integration cannot supply.
* **PDF and ZIP exports.** Binary, and nothing useful reaches a model through a
  JSON tool result. HTML, markdown and plaintext are available through
  `get_page`.

## Testing against a real instance

```bash
# Reads only. Changes nothing.
BOOKSTACK_TEST_HOST=http://10.0.0.1 BOOKSTACK_TEST_TOKEN_ID=… \
  BOOKSTACK_TEST_TOKEN_SECRET=… \
  go test ./internal/plugins/bookstack/ -run Integration -v

# The write half. Creates a book, works inside it, and destroys it —
# including out of the recycle bin — so the instance is left as found.
… BOOKSTACK_TEST_WRITES=1 go test ./internal/plugins/bookstack/ -run "IntegrationWrites|IntegrationDrift" -v
```

The writes are behind their own variable because somebody running the read
tests has not agreed to have their knowledge base changed. Everything happens
inside a book the test creates, and cleanup runs even when the test fails — if
it cannot, it says so and names the book to remove by hand.
