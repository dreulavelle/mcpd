# The customer roster

**Status:** proposed, and deliberately not started. Written 2026-09-04, to be
picked up once there are enough plugins for it to have something to join.

Do not implement this yet. It is written down so the shape stops being
re-derived from memory every time somebody asks how it would work, and so the
plugins built between now and then do not accidentally make it harder.

---

## The question

An MSP's customer exists in several systems at once. The same business is a
customer in the PSA, a folder in the credential vault, a set of numbers at one
carrier or another, and a phone system somewhere. Nobody holds all four
mappings in their head, so the first two minutes of most questions are spent
working out which record is which, and the answer to "what is Acme's PBX
address" lives in a different tab from "who is their account manager".

mcpd already reaches every one of those systems. What it cannot do is say that
the thing it reached in one is the same business as the thing it reached in
another.

## What it is not

The obvious build is a table with each customer's phone system, vault path,
numbers and open tickets in it. That is the wrong thing, and it is worth being
explicit about why, because the pull towards it is strong.

A copy is a second authority. `config.yaml` seeds the settings store once and
is then not consulted, precisely so two places cannot disagree about a value
somebody changed; a roster that stores Acme's PBX address alongside the 3CX
instance that also stores it reintroduces exactly that failure, and does it for
data that changes without anybody telling us. Every field copied in is a field
that will one day be wrong in a way nobody notices, and the wrongness will be
narrated confidently by an assistant.

So the roster stores identity and pointers. Nothing else. Everything a person
actually asks for is fetched live from the system that owns it, at the moment
they ask.

## The shape

Two tables, arriving as the next numbered migration — forward-only, like every
other.

```
customers        id, name, aliases[], status, created_*, updated_*

customer_links   customer_id, instance, external_id, external_name,
                 source, confirmed_by, confirmed_at, last_seen_at
```

`instance`, not plugin type. Two 3CX instances can exist — the type's own
documentation recommends splitting customers across instances when some people
should see only some of them — so a link points at one instance's record, and
holding a link is not the same as being allowed to read what it points at.
Permission is still checked against the instance when the read happens.

`external_id` is opaque and belongs to the far system: a NetSapiens domain, a
PSA customer id, a 3CX customer row id. `external_name` is what that system
calls the business, cached for display and for proposing matches, dated by
`last_seen_at` so a stale one is visible as stale rather than presented as
current.

`source` records how the link came to exist — `imported`, `matched`,
`manual` — because "a human confirmed this" and "a string comparison suggested
this" must not be indistinguishable later.

## Where the identities come from

Two ways to do it.

**The PSA is the spine.** Every customer is a PSA customer and everything else
hangs off that id. It is tempting because the billing relationship genuinely
does live there, and it needs no new list. It also makes one integration
structurally load-bearing, cannot start before that integration exists, and
gives no honest place to put a business that has a phone system with us and no
PSA record — which, in practice, several will.

**mcpd owns the id.** The roster mints its own customer ids and the PSA is one
link among several: the richest source for *proposing* rows, and structurally
no different from the others. This works before any particular plugin exists,
survives replacing the PSA, and treats the customer that appears in only two
systems as an ordinary case rather than an exception.

Take the second, and seed it with an import from the PSA that proposes rows
rather than creating them.

## Names are the worst available key

The difficulty everybody names first is naming: the same business is "Cooli" in
one system and "Cooli AI" in another, and a rebrand turns "Rapid Fuel" into
"Swift Fuel" in one system a year before the others catch up. That is real, and
it is also the wrong thing to build the matcher on.

**Phone numbers are the join key.** A DID appears at the carrier as a TN, on
the phone system as an inbound rule, and usually in the PSA as the customer's
main line. Numbers survive rebrands, and two businesses do not share one.
Domains are the second key: the phone system's hostname, the email domain, the
website field in the PSA.

Names are for *ranking a proposal a person then confirms*. A similarity score
proposes a link. It never makes one. This is the same posture the 3CX
integration already takes when a customer argument is ambiguous — refuse, and
name the candidates — and for the same reason.

## How a plugin takes part

One optional interface, beside `Checker` and `HealthReporter`, implemented by
the plugins that can answer it:

```go
// Directory is implemented by an instance that knows which businesses it
// serves. It is how the roster learns what there is to link to; a plugin
// that does not implement it can still be linked by hand.
type Directory interface {
    Subjects(ctx context.Context) ([]Subject, error)
}

type Subject struct {
    ID      string   // opaque, stable, this instance's own key
    Name    string
    Aliases []string
    Numbers []string // E.164 — the join keys
    Domains []string
}
```

Nothing in the plugin learns that a roster exists. It answers "who do I serve",
which is a question it can already answer for its own purposes.

The two shapes this comes in are worth naming, because they are different
amounts of work:

- **Configured subjects.** 3CX serves the customers somebody typed into its
  `customers` collection. `Subjects` reads rows that are already in memory and
  costs nothing.
- **Discovered subjects.** A carrier or a hosted platform is one credential
  covering many tenants, and the tenant list is an API call. `Subjects` is a
  read, wants a short cache, and can fail — which is why it returns an error
  and why the roster must render a customer whose links could not all be
  refreshed rather than failing the whole card.

## The direction that is not solved

Given a link, the roster has to ask the plugin for that subject's data, and the
plugins do not agree on how a subject is named. 3CX takes a `customer`
argument. A carrier has no such argument at all — the customer is a site
filter here, a sub-account there, a list of numbers somewhere else.

Resist inventing a uniform `scope` parameter across every plugin. It would be
a lowest common denominator that fits none of them and has to be threaded
through every tool. The two candidates worth weighing when this is picked up:

1. The roster's card tool knows, per plugin type, how to ask. Concentrated
   knowledge, easy to write, and one more place to edit when a plugin changes.
2. A plugin declares which of its tools are subject-scoped and under what
   argument name, the way it already declares its settings. More machinery,
   but the knowledge lives with the thing it describes.

The second is more in keeping with how the rest of the plugin surface works.
Neither should be chosen without a second discovered-subject plugin in hand to
check it against.

## Why a link is a write somebody sees

Today, reaching a customer's phone system means typing something that resolves
to exactly one configured row, and a name that matches two customers is a
configuration error refused at validation. The roster changes the arithmetic:
**one name comes to unlock every system that business is in.**

So a wrong link is not a wrong answer. It is one customer's ticket history
returned under another customer's name, to an assistant with no way to tell,
with the credential vault and the call recordings one question further along
the same path. It is a cross-customer disclosure wearing the shape of a
successful lookup.

That sets the posture:

- Resolution never guesses. Ambiguity refuses and names the candidates.
- Proposing links may be a tool. **Confirming one is a mutation** with a
  `MutationSpec`, reversible, going through the ordinary approval path, so that
  it appears in the audit trail with a person's name against it.
- `source` and `confirmed_by` are kept for the life of the link, because the
  question "who decided these were the same business" gets asked after
  something has already gone wrong.
- Unlinking is cheap and safe, and should be easy enough that people do it
  rather than living with a link they doubt.

## What it would expose

- `search_customers` — a name, alias, number or domain in; the one matching
  customer and the systems it is present in out. Several matches refuse and
  list them.
- `get_customer` — the card: every linked system, fetched live, each part
  labelled with where it came from and marked when it could not be reached.
  A partial card is the normal case and must read as one.
- `list_unlinked` — subjects present in some system with no customer. This is
  the tool that earns the feature: it answers "which businesses are on the
  carrier but have no phone system record", which is an estate question nobody
  can currently answer at all.
- `propose_links` — the matcher's suggestions, with the evidence for each
  (shared number, shared domain, name similarity) so a person confirming them
  is reading evidence rather than a score.
- `link_customer` / `unlink_customer` — the mutations.

## Where it lives, and on what

**In mcpd.** A separate application would have to re-acquire the plugin
instances, the credentials, the permission model, the tunnel identity and the
audit trail, and would then have to talk to mcpd anyway to read anything. The
roster is two tables and a resolver over machinery that already exists.

**On SQLite.** This is a few thousand identity rows, read-mostly, single
writer, holding no copies of upstream records. Postgres buys concurrent writers
and more than one node, and neither is in prospect. If the roster ever appears
to justify Postgres, the likely cause is that it started warehousing records —
which is the design mistake above, not growth.

## A note on the word

`AggregateServer` already means the endpoint that exposes every plugin at once.
Whatever this ends up called, it cannot be "the aggregate" without two
different things sharing a name in the same codebase. **Roster** is used
throughout here; **directory** is the other candidate, slightly spoiled by the
carrier sense of the word.

## Before it is picked up

It needs something to join. With one plugin that knows about customers there is
nothing to link; the feature only starts paying at three or four. The
sequencing is therefore plugins first, roster second, and the doc exists so the
plugins written in between can be checked against it.

The one thing to keep true in the meantime: a plugin that knows which
businesses it serves should be able to say so cheaply. Keeping tenant identity
— the name, the aliases, the numbers, the stable id — reachable inside a plugin
rather than buried in a per-tool response shape is what makes `Subjects` a
small addition later instead of a refactor.

## Open questions

1. Does the PSA expose a customer id stable enough to link against, or only
   names? If it is names, the import is much weaker and manual linking carries
   more of the weight.
2. Is linking a settings-page activity or a model-driven one? A Customers page
   with proposed matches to confirm is the safer build; a model that proposes
   links mid-conversation is more useful and needs the approval path from the
   first commit.
3. Does a customer need more than one link per instance? A business with two
   phone systems, or a merged customer keeping both carriers' records, says
   yes; the schema above allows it, but the tools would have to stop assuming
   one.
