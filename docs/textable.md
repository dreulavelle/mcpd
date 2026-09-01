# Textable

Reads a [Textable](https://textable.app) business-SMS instance over its v2 REST
API, as a **service account**: the tenants on the instance, the organizations
and users inside them, and individual users, organizations and contacts by id.

Read-only. The guarantee is enforced by an allow-list in `transport.go` rather
than implied by the tools that happen to exist.

## The tools

Two groups, split by where an answer comes from rather than by what it is about.

**Directory** — how a caller finds an id at all:

| tool | what it answers |
|---|---|
| `list_tenants` | every tenant, **with the id every other tool takes**, plus support contact and carrier |
| `get_tenant` | one tenant's licence allocation and its organizations |
| `list_users` | users with name, email, phone, licence, and whether soft-deleted |
| `list_organizations` | a tenant's organizations, with the id `get_organization` takes |

**Detail** — each reads one thing by id:

| tool | what it answers |
|---|---|
| `get_organization` | one organization in full: users, plan, admins, consent policy, disabled state |
| `get_contact` | one contact, including whether they have opted out |

Six tools, about 8.5 kB of tool list.

There is deliberately **no `get_user`**. See below — it is a finding, not an
omission.

## Why a service account, and not a user key

Textable issues two kinds of long-lived credential, and they are not
interchangeable.

A **user token**, written `accountUid:apiKey`, authenticates as one account.
What it may read is that account's own — contacts, drip campaigns, canned
responses — widening only when the account happens to be an admin, at which
point it reaches everything on the instance with no way to say so.

A **service account token** authenticates as itself and carries explicit
scopes. This integration takes that one.

This plugin was first built against a user token and rebuilt. The reasons are
worth recording, because they are the reasons not to go back:

- **One instance per account.** A user key sees one account, so a host serving
  several tenants would need one plugin instance per tenant, each with its own
  credential to rotate.
- **The instance-wide questions could not be asked.** "Which tenants exist",
  "who is in this one" — unanswerable without an admin key.
- **Admin is all-or-nothing.** The only way to widen a user key was to make its
  account an admin, which grants everything at once. A service account's powers
  are written down as scopes instead.

Grant only the read scopes:

```
read-all-tenants  read-all-users  read-all-organizations  read-contacts
```

`sync-billing` is not a read — nothing here calls it. The allow-list refuses a
destructive endpoint whatever the token carries, but a credential an assistant
can reach should not hold `delete-tenant` or `revoke-tenant-admin` in the first
place.

`Config.Validate` refuses a value shaped like a user token (`something:something`),
because sending one to a v2 endpoint returns `Invalid API Credentials` — which
is also what a *revoked* service token returns, with nothing to tell them apart.

## The specification is wrong in three places

Every tool here was first written from the published OpenAPI document. Running
them against a live instance found three things wrong with it that no unit test
could have caught, because a stub written from the document agrees with the
document.

**1. The billing path is misspelled in the document, not in the deployment.**

| path | result |
|---|---|
| `/api/v2/biling/tenantReport` (as documented) | **404** |
| `/api/v2/billing/tenantReport` | **200** |

Copying the spec verbatim — normally the safe move with a typo like this —
produced an integration where every directory call failed.

**2. `GET /api/v2/users/{id}` does not accept a service account.**

The document lists `SystemToken` among the credentials it accepts. Against a
live instance it answers **401** to a valid service token, with a real user id
and every read scope granted. Not 403 — 401, i.e. the route rejects the
credential *kind*.

So there is no per-user read for this credential, and `get_user` does not exist.
What can be known about a user is what `list_users` reports. The endpoint is
also absent from the allow-list, so a future maintainer reading the spec cannot
reach it by accident.

**3. `GET /api/v2/tenants` is not documented as a GET at all.**

The document describes only `POST /api/v2/tenants`. The GET exists, works, and
is **the only source of Textable's internal tenant id** — which
`/api/v2/organizations?tenantId=` and `/api/v2/billing/tenantReport/{id}` both
require. Without it the two tenant identifiers never meet and half the tools are
uncallable.

An `integration_test.go` pins all three, and is written to **fail if Textable
fixes any of them** — at which point the endpoint gets added back deliberately.

## The billing report is the user directory

`/api/v2` is addressed by id and lists almost nothing: no user listing, no
contact listing. `/api/v2/billing/tenantReport` fills the gap. It returns every
tenant, and inside each one every organization, and inside each of those every
user.

### Joining the two breakdowns

The report describes the same people twice, and neither half is sufficient:

| | carries | missing |
|---|---|---|
| `OrganizationBreakdown[].users[]` | **id**, email | name, phone, licence |
| `UserBreakdown[]` | name, phone, licence, account type, `isSoftDeleted` | **id** |

`list_users` joins them on email, case-folded, because that is the only field
both have. A user present in `UserBreakdown` but in no organization is still
listed, without an id — "who is on this tenant" is answered by the name, and
silently omitting somebody is worse than listing them with less detail. On the
instance this was verified against, 31 of 32 listed users had both halves.

`list_users` with a `tenant_id` fetches that tenant's report rather than
filtering the full one out of memory: asking about one customer should not walk
every other customer.

### An identifier that is two types

`TenantExternalId` is documented as a number, is a number on some tenants, and
is the empty **string** on others. Decoding it as either fails on the other, and
the failure takes the whole report with it rather than the one field — so it
goes through a small `flexID` type that accepts both. It must also never render
as `2.0681e+04`, which is what a `float64` does to an identifier.

## Three user counts, and why they disagree

A model reading these side by side will report an inconsistency unless it is
told. They are three different questions:

| number | source | means |
|---|---|---|
| `billable_users` (get_tenant) | billing report | what this organization is billed for **now** |
| `users_listed` (get_tenant) | billing report | how many user records came with it |
| `user_records` (get_organization) | organization document | records attached, including ones no longer in use |

Measured on a live instance:

```
organization              billable   listed   user_records   disabled
Fabrikam LLC              0          0        41             true
Northwind LLC             0          0        21             true
Acme Services             3          3        4              false
Litware Inc               4          4        9              false
CONTOSO SOLUTIONS INC     4          4        4              false
```

A **disabled** organization has people and bills for none of them — that is the
large gap, and it is correct. The smaller gaps on enabled organizations are
users attached to an organization without a billable licence.

The field on `get_organization` is named `user_records` rather than `users` for
exactly this reason. Its description now also says which number to answer with —
`list_users` or `billable_users` for "how many people", `user_records` as an
upper bound — because naming the distinction was not enough on its own: a model
given both still could not choose, and reported the pair as a contradiction.

It deliberately does **not** assert *why* the extras exist. The disabled and
deleted organizations make history the obvious guess, and the API never says so,
so the description stops at what was measured.

### The one that *was* a bug

`list_users` reported 32 users where the tenant reported 31.

Email is the join key between the two breakdowns, and a blank email cannot be
one — two nameless users would match each other, so blanks were skipped. A real
user, "Acme Emergency SMS", has **no email address**: `null` on the
organization side, `""` on the billing side. It matched nothing, and was
therefore listed twice — once with an id and no name, once with a name and no
id.

Blank-email records are now paired within their organization, taken in order and
consumed, so two nameless users in one organization pair one-to-one rather than
both binding to the first. Live count after the fix: **31 of 31, all with names
and ids.**

The lesson is in the logging. The integration test had been printing
`31 have names, 31 have ids` of 32 rows since the first live run, and that was
read as a curiosity rather than as two halves of one person.

## Contacts: readable one at a time, never listable

`GET /api/v2/contacts/{id}` is the only contact read a service account has, and
no listing exists to obtain an id from. `get_contact` says so in its description
and in its refusal, rather than naming a tool that would supply one.

It is the **one tool not verified against a live instance**, because verifying
it needs a contact id and there is no way to obtain one. Given that the
per-user read is documented as accepting a service account and does not, this
one may turn out the same; its description says to report a credential error
rather than retry.

This is not only an API gap. **The v1 listing does not complete on a large
account either.** Measured against a production tenant of over a million
contacts:

```console
$ curl -w 'http=%{http_code} ttfb=%{time_starttransfer}s\n' … /api/contacts
http=408 ttfb=30.3s
{"_errType":"TXBDEV_API_ERROR_V1","message":"Request timeout","referenceCode":"6ac1e5d3-…"}
```

That is Textable's own thirty-second timeout. No pagination parameter is
honoured — `limit`, `per_page`, `pageSize`, `page`, `count` and `take` were each
tried. Repeated attempts coincided with the backend process restarting, so the
408 is explained as **permanent, with an explicit "do not retry"**: a model told
merely "timed out" retries, and each attempt costs the instance another thirty
seconds it cannot finish.

So there is no contact enumeration anywhere in this API, for an account of any
size, by any credential. This has been raised with Textable.

## Errors, and the two that were wrong

A tool error here is a *result* a model acts on, not a failure it reports. Two
of these were actively misleading, and both were found by having a model
deliberately call every tool with bad input and say what it would do next.

**A 5xx carrying HTML is not a wrong address.** `summarise` used to give the
same sentence for any HTML body — "the address may be reaching a web server, a
proxy or a sign-in page rather than the API". That is right for a 401 or a
redirect, where a gateway really may be standing where the API should be. It is
wrong for a 502, which is the gateway in front of a *working* API reporting that
something behind it failed. A model shown it concluded there was a Textable
outage; the actual cause was the argument it had passed.

**A read by id answers 502, not 404, when the id does not exist.** Measured:

```
GET /api/v2/organizations/organization-does-not-exist   502  (nginx HTML)
GET /api/v2/contacts/contact-does-not-exist             502  (nginx HTML)
GET /api/v2/billing/tenantReport/tenant-does-not-exist  404  {"error":"Requested tenant does not exist"}
```

The tenant path gets it right; the other two do not. So a 5xx on a by-id path
now says to check the id before concluding the service is down — hedged, because
a real outage looks identical from one response.

**An empty organization listing is two answers.** `GET /api/v2/organizations`
with a tenant id that does not exist returns `{"organizations":[]}` — the same
bytes as a real tenant with none. (Inconsistently: the same request has also
been seen to answer 502.) A caller shown an empty list would report "that
customer has no organizations". `list_organizations` now checks the id against
the tenant listing, which is cached, so the check costs nothing and only runs
when the answer was empty. An unknown id becomes an error naming `list_tenants`;
a real one gets a note saying the emptiness is genuine.

### What was *not* wrong

A model testing this reported that zero-match `list_users` searches were broken,
having seen a 502 for a query matching nothing. They are not: a query is
filtered in Go after the fetch, so the upstream call for `zzzzznotarealname` is
byte-identical to the one for a name that matches. Re-running it returned 200.
The 502s in that sweep were the backend being intermittently unwell — the same
symptom this API shows under load elsewhere in this document.

Worth recording because the reasoning generalises: when a report blames a code
path, check whether that path exists before changing anything.

## The read-only guarantee

`transport.go` refuses a request unless its **method and its path** are both
named in the allow-list. Six entries, all GETs.

A method check would be wrong here for two reasons, both from the credential. A
service account may hold `edit-contacts` or `edit-organizations` because the
same token is useful elsewhere, so this plugin cannot assume its credential is
powerless. And a method check is one edit from permitting everything — widening
to "GET, or DELETE" admits `DELETE /api/v2/users/{id}`,
`DELETE /api/v2/organizations/{id}` and `DELETE /api/v2/users/{id}/token/{tokenId}`
in the same breath.

Patterns are anchored at both ends. `^/api/v2/users/[^/]+$` must not also cover
`/api/v2/users/{id}/token`, which **mints a long-lived credential** for that
user, or `/changePassword`. Paths are normalised before matching, so a request
cannot arrive past an anchored pattern by spelling. Redirects are not followed:
one to a different host would carry the token somewhere the operator never
named.

## Two calls at startup

`/health` first, unauthenticated: it proves the address resolves, TLS works and
the thing answering is Textable rather than a gateway.

It accepts **200 or 503**. This endpoint follows the convention where the status
code carries the verdict, and a 503 still returns the whole report — with a
status that is often `warn`, meaning degraded but serving. Treating the code as
a failure took the plugin's startup down on the day it shipped: a momentary
wobble on the far end left it reported as broken on the dashboard while every
one of its tools answered normally. The body decides; the code only says whether
to expect one. Anything that is neither 200 nor 503 is a gateway or a wrong
address, and still fails.

Then `GET /api/v2/tenants`, which is right on all three counts a probe is judged
on: cheap (a few hundred bytes), proves both the credential and the
`read-all-tenants` scope every other tool depends on, and is the first call the
directory makes anyway. A `403` names the scopes to grant, because the fix is a
scope rather than a new token.

It deliberately does **not** probe `GET /api/v2/users/{id}` — that endpoint
401s a valid service token, so probing with it would report every healthy
installation as having a rejected credential. An earlier version did exactly
that.

No identifier for the credential is logged. Unlike a user key, whose account uid
sits in front of a colon and is safe to print, a service token is one opaque
string all the way through.

## Errors carry a reference code, in two shapes

The documented envelope is `{_errType, message, referenceCode, reason}`.
`referenceCode` is a UUID unique to that one failure and is quoted into every
error this plugin raises — it is the only string somebody on a support call can
give Textable.

`message` is the class of failure and is often the same for causes needing
different fixes. `reason` separates them and is present only sometimes.

There is also a **second, undocumented envelope**:

```json
{"errors":["User must be admin to access this endpoint."]}
```

Returned as **400**, where the same refusal elsewhere is a **403**. Both are
read, and a 400 carrying an admin refusal is reported as a credential problem
rather than a malformed request — otherwise it sends somebody looking at their
arguments for something that is in their token.

## Timeouts and caching

The client timeout is **40 seconds, deliberately longer than Textable's 30**.
When they were equal the two raced, and half the time the client gave up first
and produced `context deadline exceeded`, which names nothing and cannot be
quoted. Losing that race on purpose buys the far end's diagnosis and its
reference code.

Held for `cache_seconds` (default five minutes): the tenant listing, the tenant
report, the organization listing, and one tenant or organization by id.

Never held: **a contact** — whether somebody has opted out is acted on, and it
is a legal position rather than a preference, so a stale answer has consequences
outside the conversation — and **`/health`**, since a liveness check answered
from memory is not one.

## Logging

Debug says what was asked and how much came back: method, path, status, byte
count, duration. Never a response body, which here is a contact's name and phone
number or every user on the instance. Never the token. There is a test that
fails if either appears.

## Testing against a real instance

```bash
# Reachability and version, no credential needed.
curl -s https://your-instance.textable.app/health | jq '{status, version}'

# Does the token authenticate, and can it see tenants? This is the startup probe.
curl -s -H "Authorization: Bearer $TXB_TOKEN" \
  https://your-instance.textable.app/api/v2/tenants \
  | jq '.tenants[] | {id, tenantName, provider}'

# The user directory. Note "billing", not the spec's "biling".
curl -s -H "Authorization: Bearer $TXB_TOKEN" \
  https://your-instance.textable.app/api/v2/billing/tenantReport \
  | jq '[.[] | {TenantName, UserQuantity, orgs: (.OrganizationBreakdown|length)}]'
```

Or run the plugin itself against it:

```bash
TEXTABLE_TEST_URL=https://your-instance.textable.app \
TEXTABLE_TEST_TOKEN=… \
go test ./internal/plugins/textable/ -run '^TestIntegration_' -v
```

## What is not here, and why

Most of this is not a coverage decision. It is the API not exposing these to any
credential mcpd can hold. Verified by calling each one with the service token:

| endpoint | result | means |
|---|---|---|
| `GET /api/drips` | **401** | v1 — user tokens only |
| `GET /api/canned-responses` | **401** | v1 — user tokens only |
| `GET /api/contacts` | **401** | v1 — user tokens only (and 408s at scale anyway) |
| `GET /api/export` | **401** | v1 — user tokens only |
| `GET /blasts/{id}` | **404** | not routed on this deployment; documented as browser-session only |

And these do not exist in the API at all, for anyone:

- **Reading messages or conversations.** There is no GET anywhere.
  `POST /api/send` sends, `/receive` is an inbound webhook, and
  `POST /deleteConversation` is browser-session only. Message history, delivery
  status and campaign analytics have no read endpoint to build on.
- **Listing or searching contacts.** See above — nothing works at any scale.
- **Opt-out reporting across contacts.** Needs contact enumeration, so it
  inherits that impossibility. `get_contact` reports `opted_out` for one contact
  at a time.

So the messaging and campaign gap is real, and closing it needs Textable to
publish endpoints rather than mcpd to call different ones.

Two things are genuinely available and deliberately left out:

- **Drip campaigns and canned responses** — readable, but only with a *user*
  token, which is one account's credential. Bringing them back means a second
  plugin instance per account, which is the arrangement this design replaced.
  Worth doing if per-account campaign visibility matters more than the
  instance-wide directory.
- **Integrations** (`/api/v2/integrations/installed?userId=`). This one works —
  a service account may call it, and the `userId` is required rather than
  optional as documented. `available` returns a 5.5 kB catalogue of ten platform
  definitions, the same for every user. `installed` is the useful half, and on
  this instance **all 31 users have none**, so the shape of a non-empty response
  cannot be observed. Building a tool on a schema nobody has seen is how the
  three spec errors above got in; it is a half hour's work once one user has an
  integration installed.

- **A per-user read.** Not skipped — unavailable. `GET /api/v2/users/{id}` 401s
  a service account. See above.
