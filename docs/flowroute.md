# Flowroute

Your customers' Flowroute accounts: numbers, inbound routes, emergency
addresses, caller-ID names and port orders. Read-only.

One instance serves many customers, one row per business.

## Customers

Settings → Plugins → your Flowroute instance → **Customers**. One row per
business: a name, any aliases people use for it, and **that account's own API
key**.

The credential is per customer because Flowroute makes it so. There *is* a
parent/child account relationship, but the API does not draw that line:

* A key is scoped to a single account when you create it — the screen lets you
  pick which, one at a time.
* `GET /numbers` is documented as returning the numbers on **your** account,
  and takes no account argument.
* There is no sub-account endpoint, and no documented way to pivot a parent's
  key onto a child.

So a parent account's key reads the parent's own inventory, not its children's.
Thirty customers on Flowroute means thirty key pairs, and the tenancy lives
here rather than upstream.

Every tool takes a `customer`, resolved by name or alias. An instance serving
one customer resolves it without being told. A name that fits two customers is
**refused** with both named — never guessed at, because the wrong guess answers
correctly about the wrong business. `list_customers` has the names, the
aliases, and whether the last call to each account worked.

Access is per instance: anyone who can reach it reaches every customer on it.
Split customers across instances if some people should see only some.

## The credential

Flowroute Manage → **Preferences → API Control**, for each customer's account.
An API key is a pair: an **Access Key** of eight characters and a **Secret Key**
of thirty-two. Both go in that customer's row; the secret is stored encrypted.

The API authenticates with HTTP Basic — the access key as the username, the
secret as the password. There is no token exchange and nothing expires, which
has two consequences worth knowing:

* **The credential travels on every single request.** It is never logged, never
  put in a URL, and never returned by a tool, and the transport refuses any
  request addressed to a host other than `api.flowroute.com` so that a redirect
  cannot carry it somewhere else.
* **Rotating a key stops every read for that customer at once.** There is no
  grace period and no cached token that keeps working; the next call is a 401.
  The plugin says so in those words rather than reporting a bare status code,
  and names the customer — one rotated key degrades that row, not the instance,
  and the other customers keep answering.

If the two values get put in the wrong boxes — Manage shows them one above the
other — the plugin refuses to start and says which customer, rather than failing
later with a 401 that reads as a wrong password. Two customers sharing one
access key is refused for the same reason: a key belongs to one Flowroute
account, so one of those rows is pointed at the other's.

## The read-only guarantee

**A Flowroute API key is not scoped.** There is no read-only credential to ask
for: the same access key and secret that read a number can release it, repoint
the route it rings on, or replace the address emergency services are given for
it. So the guarantee lives in this integration rather than in the credential.

`transport.go` holds the complete list of paths this plugin may read. Every
entry is a GET, everything else is refused before it reaches the network, and
the list is checked on the transport rather than at each call site — so a
redirect nobody wrote is checked too. Adding a read means adding a line to that
list, which is the amount of friction the decision deserves against a live
carrier account.

The refusal is a real one, not a convention. There is a test that builds a
`DELETE` against a real number and asserts it never leaves the process.

## What it reads

Every tool below takes `customer`, and every answer carries the business it is
about so it can never be read as another customer's.

| Tool | Answers |
|---|---|
| `list_customers` | The businesses this instance serves, their aliases, and whether the last call to each worked. `check` tries them all now. |
| `list_numbers` | Every number on the account, with alias, type, rate centre, status. `starts_with` narrows to a country or area code. |
| `get_number` | One number in full: alias and note, messaging and CNAM lookup, costs, and the routes it rings on. |
| `list_routes` | The inbound routes — the hosts, URIs and numbers calls can be sent to. |
| `list_edge_strategies` | The regions Flowroute sends traffic from, with the firewall rules and NAPTR record each needs. |
| `list_e911_addresses` | The emergency-calling addresses on the account. |
| `get_e911_address` | One of them in full. |
| `list_cnam_records` | Caller-ID name records, with whether each was approved and why one was rejected. |
| `list_port_orders` | Port orders and where each has got to. |
| `get_port_order` | One port order, with when its status last changed. |
| `list_cdr_exports` | The call-detail export jobs and their state. |

### Two things `get_number` says that no field says

A number with **no primary route** rings nowhere. Nothing in the record is set
to an error state; the relationship is simply null, and the number looks
healthy in every listing. `get_number` says so in a note.

A number with **no emergency address** places a 911 call carrying no location.
Same shape: a null relationship, invisible in a listing, and the failure only
appears on the day it matters. Also a note.

### What a number is for

Which *business* a number belongs to is answered by the customer row it was
read under. What the number is *for* — which site, which department, whose desk
— is not something a Flowroute account knows.

In practice it is written in the number's **alias** or **note**, by whoever
bought it. Both are returned on every row, including when they are empty,
because an empty alias is itself the answer to "why can nobody tell what this
number is for". `list_numbers` can filter on `alias`, but Flowroute matches it
exactly — "Acme" will not find "Acme front desk".

## Numbers are eleven digits

Flowroute keys a number as digits with the country code and no plus:
`12065550100`. Tools accept that, a bare ten-digit number, or anything a person
would type — `+1 (206) 555-0100` — and normalise it. A number that is neither
ten nor eleven digits is passed through as its digits rather than guessed at,
so a mistake comes back as a 404 naming the number instead of a lookup of some
other country's subscriber.

Answers carry both forms: `number` for quoting back to the API, `formatted` for
reading.

## JSON:API, as Flowroute speaks it

Every response is [JSON:API](https://jsonapi.org): the entity is under `data`,
its fields under `data.attributes`, and related entities arrive in a sibling
`included` array rather than nested. Reading a number's route means looking
there — which is why `get_number` resolves the `primary_route` relationship
against `included` rather than expecting the route inside the number.

Listings page with `limit` and `offset`, up to 200 a page, and carry
`links.next` while more remain. This plugin computes its own offsets rather
than following that link, so a URL the API composed cannot decide what this
package requests.

**An id is not always a string.** JSON:API says it is, and Flowroute sends one
for every entity except an edge strategy, whose id arrives as the bare number
`1`. A string-only field fails that response outright. This was found by
running against a live account, which is what that test is for.

## A 404 means two different things

```
{"errors":[{"detail":"No such port order","status":404,"title":"Resource not found"}]}
{"errors":[{"status":"404 Not Found: The requested URL was not found on the server…"}]}
```

The first is an **answer**: the thing is not there. The second means the URL is
one Flowroute has never served — a bug in this package, not a missing record.
The tell is the type of `status`: a number with a title, or the whole HTTP
status line as a string.

They must not be collapsed. Reporting a mistyped path as "not found" sends
somebody looking for a port order that was never missing. `errors.go` tells
them apart, and `get_number` reports the first as "not on this account" and the
second as a path the API does not serve.

An empty listing takes the first shape too — an account with no port orders
answers 404 rather than an empty array — so `list_port_orders` returns an empty
list rather than an error.

## Call detail records are an export, not a query

There is no endpoint that answers "the calls between these dates". An export
job is requested, built in the background, and downloaded when ready.
Requesting one is a `POST`, so this integration does not make one:
`list_cdr_exports` says which jobs exist and where each has got to, and
starting one is done in Flowroute Manage.

## What is not here yet

* **Messages.** The messaging API is on a different base path (`/v2.2/`) and
  answers message detail records. Left out deliberately; add it if the
  questions start being about SMS.
* **Purchasable numbers.** Searching Flowroute's inventory is a read, and it is
  a read in service of buying. Out of scope for an integration that does not
  buy.
* **Number portability check.** `POST /v2/portorders/portability` answers
  whether a number can be ported. It is a read in everything but its HTTP
  method, and the guard is GET-only on purpose, so it is not here.

## Two shapes read from the documentation rather than a response

Port orders and CDR export jobs could not be checked against a live account —
the account this was built on has neither. The documented listing shape for
port orders is a nested envelope rather than the flat array everything else
uses, and the sample in Flowroute's documentation is not valid JSON, so both
shapes are accepted.

A port-order field arriving under a name this plugin does not map is **dropped**
and its name — never its value — is reported in `unmapped_fields`. A shape that
has moved therefore shows up as a named gap somebody can fix, rather than as a
confidently empty answer.

## Testing against a real account

```bash
FLOWROUTE_TEST_ACCESS_KEY=… FLOWROUTE_TEST_SECRET_KEY=… \
  go test ./internal/plugins/flowroute/ -run Integration -v
```

One account's key, mounted as a single customer. Skipped without those, so it
costs nothing in CI. It reads each tool, asserts the shapes, and asserts that a
`DELETE` built by hand is refused before it reaches the network. It prints
counts only — no number, name or address goes into the test output.
