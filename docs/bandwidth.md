# Bandwidth

Calls, messages, number inventory, porting, 10DLC registration and E911 on one
Bandwidth account. Read-only.

## The credential

Bandwidth issues API credentials as an **OAuth2 client id and secret**, made in
the Bandwidth console under API credentials. mcpd exchanges them for a
short-lived bearer token, caches it, and renews it about a minute before it
lapses. Nothing here ever sends the secret to a product API.

Three things about that credential are worth knowing before you make one,
because none of them can be changed afterwards.

**Its accounts are fixed at creation.** A credential is scoped to one or more
account numbers, and the token it issues says which — so mcpd already knows
what it may read, and there is nothing for you to repeat in settings.

One instance answers for all of them. Every tool takes an optional `account`,
and `list_accounts` is where an agent finds out what the options are. That is
deliberately not the shape the other integrations take — two Observiums are two
instances — and the difference is in the credential rather than in taste. It is
also the shape the question takes: *are any of our ports stuck* is about the
estate, and somebody asking it should not have to know how many accounts there
are, nor get four answers to add up.

An account the credential does not cover is refused here, naming both sides,
rather than sent upstream to return a 404 that explains nothing.

**Its roles are fixed at creation too**, and Bandwidth's roles are *not* split
into read and write. "Campaign management" grants creating a campaign as well
as reading one; "Ordering" grants placing an order. There is no role that
grants looking without touching. So a credential scoped for the reads below can
also write, and the read-only guarantee lives in this plugin's transport rather
than in the credential. See below.

**Its secret has an expiry you choose**, and the API does not report it.
Nothing here can warn you before it lapses, so write the date down. An expired
secret is refused exactly like a wrong one; a new secret is issued from the
same credential and the client id does not change.

### Which roles

Bandwidth answers a missing role with a bare 403 that does not say which of the
thirteen is missing. This plugin guesses, from the path, and says so — a guess
that turns a search among thirteen into one edit. Start with the fewest that
cover what you want to read:

| To read | Role |
|---|---|
| calls, conferences, recordings, transcriptions | Basic access |
| account call statistics | Reporting |
| messages and media | Messaging insights |
| toll-free verification | Campaign management |
| endpoints | Configuration |
| number lookup results | TN lookup |

## What it reads

Thirty-one tools across five addresses. Bandwidth serves one product per host
rather than one API under one root, and the Dashboard API — which is most of
what a telephony operator actually asks about — is reached through the gateway
under `/api/v2`:

| Address | Format | What |
|---|---|---|
| `voice.bandwidth.com` | JSON | calls, conferences, recordings, transcriptions, statistics |
| `messaging.bandwidth.com` | JSON | messages, stored media |
| `api.bandwidth.com` | JSON | toll-free verification, endpoints, number lookup |
| `api.bandwidth.com/api/v2` | **XML** | numbers, orders, sites, SIP peers, applications, porting, E911 |
| `api.bandwidth.com/api/…/campaignManagement/10dlc` | XML | 10DLC campaigns and brands |
| `insights.bandwidth.com` | JSON | voice traffic aggregates |

The Dashboard half speaks XML and the rest speaks JSON. That is a fact about
Bandwidth, not a choice worth propagating into twenty-nine tools, so XML is
decoded into the same shape JSON decodes to and nothing above that line knows
the difference.

### Porting

The tools most likely to answer a ticket:

* **`list_port_ins`** — port-in orders, by status, date or a number on them.
* **`get_port_in`** — one order, and on request its **status history**, its
  **notes**, and whether a letter of authorisation is on file. This is the one
  that answers *why has this not moved*: the order itself carries a single word
  of status, and the reason a losing carrier rejected something is written in
  the notes. Asking for all three is one call.
* **`list_bulk_port_ins`** / **`get_bulk_port_in`** — bulk orders, which carry
  many numbers under one request and are tracked separately. A number missing
  from `list_port_ins` may be on one of these.
* **`list_tollfree_port_validations`** — a toll-free number is validated before
  it can port, and a failed validation is the usual reason a toll-free port
  never starts.

An enrichment that fails does not fail the order: `get_port_in` returns what it
read and names what it could not, because a partial answer presented as a
complete one is worse than the failure.

### Aggregates

`aggregate_calls` answers *how much* and *is it getting worse* — minutes of
use, completed and failed call counts, connection rates, average durations,
sliced by time and narrowable by number, direction or call type. `list_calls`
cannot answer either question: it returns individual calls.

Insights keeps **one year**. A window reaching further back comes back empty
rather than refused, and "no traffic" reads identically to "not kept", so the
answer says which it is.

Its filters are deepObject-style — the comparison is part of the parameter
name, `timestamp[gte]=…` rather than a value. A mistyped name there is ignored
rather than refused, so the answer comes back unfiltered and looks right, which
is why there is a test pinning them.

### Everything else

`list_accounts` (what this credential reaches, and what an unqualified question
means), `list_numbers` (in service or disconnected, whole account or one site or one
SIP peer, with a totals-only mode), `search_available_numbers`,
`list_orders` (purchases, disconnects, or number_options),
`list_sites`, `list_sip_peers`, `list_applications`, `list_e911_locations`,
`list_campaigns` and `list_brands`.

Two absences are deliberate.

**No audio, ever.** Bandwidth will hand back the bytes of a recording or an MMS
attachment, and those endpoints are left off the allow-list. A media file is
not a read anybody asked for, and putting one into a model's context is a
mistake that is expensive before it is useless.

**No message bodies**, because Bandwidth does not store them. `search_messages`
answers "did that text arrive" from status and error code, which is the
question people actually have.

**No letter-of-authorisation documents.** `get_port_in` will say whether one is
on file; it will not fetch it. An LOA is a scan, usually of somebody's
signature.

## The read-only guarantee

Every request this plugin makes is checked against a named allow-list in
`transport.go` before it leaves. A path that is not on it is refused here, not
upstream — including a redirect the plugin did not write, and including a bug
in this package that builds the wrong URL.

This matters more here than it does for an integration whose credential can be
scoped to reads, because Bandwidth's cannot. `POST /calls` places a real
telephone call with the same credential these reads use. The allow-list is what
stands between those two facts, which is why adding a read means adding a line
to it on purpose.

## Which account a question is about

In order: the `account` on the call, then the **default account** in settings,
then the only account the credential covers if there is exactly one.

With several accounts in scope and none of those settling it, the call is
**refused and told to pick**. That is the important case. Answering about
whichever account happened to come first would be worse than failing: *no
port-ins are stuck* reads exactly the same whether it is true of the estate or
true of one account nobody meant, and there is nothing in the answer to tell
them apart.

The default is optional and only decides what an unqualified question means.
Set it where an estate has an obvious main account and the rest are incidental;
leave it empty and an agent will be asked which one.

## Error codes

`get_error_reason` turns the `error_code` on a failed message into words: what
it means, who refused it — Bandwidth or the carrier beyond it — and whether
sending the same message again might work.

It is answered from a table compiled into the binary rather than from the API,
because Bandwidth publishes these on a documentation page and serves them
nowhere. That makes the lookup free and available when the API is not, and it
goes stale when Bandwidth adds a code. So a code the table does not hold says
*that*, points at the page, and still answers the useful half from the range:
4000s are Bandwidth refusing, 4700s a carrier refusing, 5000s and 5600s are
service failures worth retrying.

## One thing XML cannot say

XML has no way to mark a list, so a collection with one member is
indistinguishable from a single value: `<TelephoneNumber>` appearing once
decodes to a map and appearing twice decodes to a slice. Code written against
the two-member case then breaks on the account that happens to have one number
— silently, in production, because the fixture had two.

Nothing can fix that from the document; the information is genuinely absent. So
it is fixed at the call site, where the element name is known, by `listOf`.
Every collection here goes through it and comes back a slice whether it held
none, one, or nine.

The same applies to failure: the Dashboard answers some refusals with **200 and
an error inside the body**. Left alone that reads as an empty result, which is
the worst possible outcome — a model told nothing is there will say so with
confidence. Every Dashboard read checks for it.

## 10DLC was reading the wrong API entirely

Worth recording, because the failure pointed away from itself for weeks.

Every 10DLC read returned:

```
403  {"errors":[{"type":"forbidden",
     "description":"Account 9000003 is not enabled for the Registration Center"}]}
```

That reads as an entitlement to go and buy, and it is a real Bandwidth product
— so the obvious conclusion was that the account needed enabling. It did not.

This package was calling `/api/v2/accounts/{id}/tendlc/campaigns`, which is the
**Registration Center**, a different 10DLC product these accounts do not use.
Campaign management is served at
`/api/accounts/{id}/campaignManagement/10dlc/campaigns` — same host, under
`/api` rather than `/api/v2`. The tell was that *all four* accounts failed
identically while a campaign plainly existed; a per-account entitlement would
not be uniform.

Correcting the path produced a second failure hiding behind the first:

```
406  (no body)
```

Campaign management answers **XML**, like the rest of the Dashboard, and the
10DLC reads were asking for JSON. While the path was wrong the request never
got far enough to be refused for its `Accept` header, so the two defects
concealed each other.

Both are now fixed and verified against a live account.

### Where a rejection actually lives

The fields worth reading on a campaign that is not working:

| field | what it says |
|---|---|
| `Status` | The Campaign Registry's view — often `ACTIVE` even when a carrier is refusing |
| `MnoStatusList` | each carrier's own status, independent of the above |
| `SharingStatus` / `SecondaryDcaSharingStatus` | whether the downstream aggregator accepted it |
| `SecondaryDcaDeclineReason` | the carrier's own rejection text, with codes |

The independence matters. A live campaign read during this work was `ACTIVE` at
TCR and `REGISTERED` with ATT, TMO, CLS and USC, while
`SecondaryDcaSharingStatus` was `DECLINED` with:

```
Rejection Code 2139: Invalid Call To Action - The provided opt-in form does not
contain any phone number input field.
Rejection Code 2132: Invalid Call To Action - The instructions provided for a
customer to give SMS opt-in are not sufficient.
```

Anything that reduced that to "Declined" would have thrown away the entire
answer, so the raw text is passed through untouched. `list_campaigns`' own
description points a model at these fields, because a campaign showing `ACTIVE`
reads as healthy otherwise.

### What has no endpoint

`with_history` is gone from both tools. Campaign management offers Create,
Update, Fetch, Fetch List, Fetch List By Brand and Deactivate, and no status
history — the per-carrier state in `MnoStatusList` is what replaces it. Brand
vetting is `/vetting`, singular, and exists only where vetting was purchased; a
brand's own `IdentityStatus` is the ordinary answer.

## What is not here yet

Nothing of the read surface. Writes are out of scope by design, and the
remaining reads are ones nobody has asked for: SIP credentials, per-number
feature orders, and the `serviceActivation` endpoints.
